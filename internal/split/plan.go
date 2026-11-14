// Package split cuts a Play-All track into episodes with mkvmerge and proves
// the result. Every cut is checked against the source's real keyframes before
// anything is written, and every part is checked against the source after.
package split

import (
	"context"
	"errors"
	"fmt"
	"math"
	"math/big"
	"strconv"
	"strings"

	"github.com/reclyptor/mediaops/kit/ffprobe"
	"github.com/reclyptor/mediaops/kit/frames"
)

const (
	// DefaultTol is one frame at 24 fps. In chapter mode it is how far past the
	// chapter start the keyframe a cut lands on may be; in --at mode it is how
	// far either side of the requested time the nearest keyframe may be.
	DefaultTol = 0.042

	// keyframeSlack is how far before a chapter start a keyframe still counts
	// as "at" it (chapter mode only). The Matroska timestamp scale is a
	// millisecond.
	keyframeSlack = 0.001

	// probeBefore and probeAfter bound the packets read around a cut.
	probeBefore = 2.0
	probeAfter  = 4.0
)

// Mode is how the cuts were given.
type Mode string

const (
	ModeChapters   Mode = "chapters"
	ModeTimestamps Mode = "timestamps"
)

// Cut is one planned boundary: where it was asked for and the keyframe it
// lands on.
type Cut struct {
	N         int     `json:"n"`
	Chapter   int     `json:"chapter,omitempty"`
	Requested float64 `json:"requested"`
	Keyframe  float64 `json:"keyframe"`
	DeltaMS   float64 `json:"delta_ms"`
	// LeadingPictures counts the packets after the keyframe, before the next
	// one, that are shown before it: the signature of an open GOP.
	LeadingPictures int `json:"leading_pictures"`

	// arg is the keyframe's exact timestamp as mkvmerge reads it, built from
	// the packet's integer pts so no rounding of the request can move the cut.
	arg string
}

// PlanFinding is a reason a cut cannot be made.
type PlanFinding struct {
	Cut     int    `json:"cut,omitempty"`
	Problem string `json:"problem"`
}

var errNoVideo = errors.New("the source has no video stream")

// parseChapterCuts reads a comma list of 1-based chapter numbers, strictly
// increasing and within [2, count].
func parseChapterCuts(list string, count int) ([]int, error) {
	var cuts []int
	for _, field := range strings.Split(list, ",") {
		field = strings.TrimSpace(field)
		n, err := strconv.Atoi(field)
		if err != nil {
			return nil, fmt.Errorf("the chapter list must be integers, got %q", field)
		}
		if n < 2 || n > count {
			return nil, fmt.Errorf("chapter %d is out of range: the source has %d chapters and a cut can be at 2 to %d", n, count, count)
		}
		if len(cuts) > 0 && n <= cuts[len(cuts)-1] {
			return nil, fmt.Errorf("chapter %d does not follow chapter %d: the list must be strictly increasing", n, cuts[len(cuts)-1])
		}
		cuts = append(cuts, n)
	}
	return cuts, nil
}

// parseTimeCuts reads a comma list of seconds, strictly increasing and inside
// (0, duration).
func parseTimeCuts(list string, duration float64) ([]float64, error) {
	var cuts []float64
	for _, field := range strings.Split(list, ",") {
		field = strings.TrimSpace(field)
		t, err := strconv.ParseFloat(field, 64)
		if err != nil || math.IsNaN(t) || math.IsInf(t, 0) {
			return nil, fmt.Errorf("the --at list must be seconds, got %q", field)
		}
		if t <= 0 || t >= duration {
			return nil, fmt.Errorf("%s s is outside the source: a cut must be after 0 and before %.3f s", field, duration)
		}
		if len(cuts) > 0 && t <= cuts[len(cuts)-1] {
			return nil, fmt.Errorf("%s s does not follow %s s: the list must be strictly increasing", field, formatSeconds(cuts[len(cuts)-1]))
		}
		cuts = append(cuts, t)
	}
	return cuts, nil
}

func formatSeconds(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }

// parseTimeBase reads ffprobe's "1/1000".
func parseTimeBase(s string) (frames.Rational, error) {
	num, den, ok := strings.Cut(s, "/")
	a, errA := strconv.ParseInt(num, 10, 64)
	b, errB := strconv.ParseInt(den, 10, 64)
	if !ok || errA != nil || errB != nil || a <= 0 || b <= 0 {
		return frames.Rational{}, fmt.Errorf("%q is not a time base", s)
	}
	return frames.Rational{Num: a, Den: b}, nil
}

// timestampArg writes pts in the time base tb as seconds, exactly, for
// mkvmerge's --split timestamps: ("1425.050s"). Going through the integer pts
// keeps the cut on the keyframe whatever the float rounding of its pts_time.
func timestampArg(pts int64, tb frames.Rational) string {
	s := new(big.Rat).SetFrac64(pts*tb.Num, tb.Den).FloatString(9)
	s = strings.TrimRight(s, "0")
	if dot := strings.IndexByte(s, '.'); len(s)-dot-1 < 3 {
		s += strings.Repeat("0", 3-(len(s)-dot-1))
	}
	return s + "s"
}

// countLeading counts the packets after pkts[kf] in file order, up to the next
// keyframe, whose pts is lower than the keyframe's.
func countLeading(pkts []ffprobe.Packet, kf int) int {
	n := 0
	for i := kf + 1; i < len(pkts) && !pkts[i].Keyframe; i++ {
		if pkts[i].HasPTS && pkts[i].PTS < pkts[kf].PTS {
			n++
		}
	}
	return n
}

// planCut decides one cut from the video packets read around t.
//
// Chapter mode (nearest false): mkvmerge is given the chapter, and starts a
// part at the first keyframe at or after it, so that is the keyframe the cut
// lands on and it must be within tol after t. A keyframe more than a
// millisecond before the chapter start would put the cut a whole GOP late, so
// it is a finding, not a near miss.
//
// --at mode (nearest true): mkvmerge is given the keyframe's own timestamp, so
// that hazard cannot happen and the nearest keyframe either side of t is
// taken if it is within tol (the earlier on a tie), as mkv-split-ts.py did.
func planCut(pkts []ffprobe.Packet, tb frames.Rational, t, tol float64, allowOpenGOP, nearest bool) (c Cut, problems, notes []string) {
	c.Requested = t
	kf, kfBefore := -1, -1
	for i, p := range pkts {
		if !p.Keyframe || !p.HasPTS {
			continue
		}
		if p.PTSTime >= t-keyframeSlack {
			if kf < 0 || p.PTS < pkts[kf].PTS {
				kf = i
			}
		} else if kfBefore < 0 || p.PTS > pkts[kfBefore].PTS {
			kfBefore = i
		}
	}
	if nearest && kfBefore >= 0 && (kf < 0 || t-pkts[kfBefore].PTSTime <= pkts[kf].PTSTime-t) {
		kf = kfBefore
	}
	if kf < 0 {
		return c, []string{fmt.Sprintf("no keyframe at or after %.3f s in the video packets read around it", t)}, nil
	}

	c.Keyframe = pkts[kf].PTSTime
	c.DeltaMS = (c.Keyframe - t) * 1000
	c.arg = timestampArg(pkts[kf].PTS, tb)
	c.LeadingPictures = countLeading(pkts, kf)

	off := c.Keyframe - t
	if nearest {
		off = math.Abs(off)
	}
	if off > tol+1e-9 {
		msg := fmt.Sprintf("%.3f s is not a keyframe: the nearest ones are %s (tolerance %.0f ms)", t, neighbours(pkts, t, kf), tol*1000)
		problems = append(problems, msg)
	}
	if c.LeadingPictures > 0 {
		msg := fmt.Sprintf("the keyframe at %.3f s opens a GOP: %d pictures after it are shown before it", c.Keyframe, c.LeadingPictures)
		if allowOpenGOP {
			notes = append(notes, msg+" (allowed by --allow-open-gop)")
		} else {
			problems = append(problems, msg+" (open GOP); pass --allow-open-gop to accept them")
		}
	}
	return c, problems, notes
}

// neighbours names the nearest keyframe on each side of t, always including
// the one the cut would land on.
func neighbours(pkts []ffprobe.Packet, t float64, chosen int) string {
	before, after := math.Inf(-1), math.Inf(1)
	for _, p := range pkts {
		if !p.Keyframe || !p.HasPTS {
			continue
		}
		if p.PTSTime < t {
			before = math.Max(before, p.PTSTime)
		} else {
			after = math.Min(after, p.PTSTime)
		}
	}
	var parts []string
	if !math.IsInf(before, 0) {
		parts = append(parts, fmt.Sprintf("%.3f s before", before))
	}
	if !math.IsInf(after, 0) {
		parts = append(parts, fmt.Sprintf("%.3f s after", after))
	}
	if len(parts) == 0 {
		return fmt.Sprintf("%.3f s", pkts[chosen].PTSTime)
	}
	return strings.Join(parts, " and ")
}

// precheck plans every cut against the source's own packets. Nothing is
// written. A failing ffprobe is an error; a cut that cannot be made is a
// finding. chapters is nil in timestamp mode.
func precheck(ctx context.Context, ex Tools, src string, info ffprobe.Info, times []float64, chapters []int, tol float64, allowOpenGOP bool) (cuts []Cut, findings []PlanFinding, notes []string, err error) {
	var video *ffprobe.Stream
	for i := range info.Streams {
		if info.Streams[i].CodecType == "video" {
			video = &info.Streams[i]
			break
		}
	}
	if video == nil {
		return nil, nil, nil, errNoVideo
	}
	tb, err := parseTimeBase(video.TimeBase)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("the video stream of %s: %w", src, err)
	}

	for i, t := range times {
		lo := math.Max(0, t-probeBefore)
		pkts, err := ffprobe.Packets(ctx, ex, src, "v:0", formatSeconds(lo)+"%"+formatSeconds(t+probeAfter))
		if err != nil {
			return nil, nil, nil, err
		}
		c, problems, cutNotes := planCut(pkts, tb, t, tol, allowOpenGOP, chapters == nil)
		c.N = i + 1
		if chapters != nil {
			c.Chapter = chapters[i]
		}
		for _, p := range problems {
			findings = append(findings, PlanFinding{Cut: c.N, Problem: p})
		}
		for _, n := range cutNotes {
			notes = append(notes, fmt.Sprintf("cut %d: %s", c.N, n))
		}
		cuts = append(cuts, c)
	}
	return cuts, findings, notes, nil
}
