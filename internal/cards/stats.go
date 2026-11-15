// Package cards locates held title cards: by the colour a held frame keeps
// (find), or by ffmpeg's own freeze detection plus the colour of what froze
// (freeze). Both read ffmpeg's filter output line by line, and neither writes
// anything.
package cards

import (
	"context"
	"errors"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"

	"github.com/reclyptor/mediaops/kit/proc"
)

// Sample is one frame's signalstats figures. T is the frame's time in the
// decoded window, in seconds, before the window's start is added.
type Sample struct {
	T                float64
	YAvg, UAvg, VAvg float64
	YMax             float64
}

// Freeze is one freezedetect result. Start is relative to the decoded window.
type Freeze struct {
	Start, Duration float64
}

var (
	framePTS   = regexp.MustCompile(`\bframe:\d+\s+pts:\S+\s+pts_time:(\S+)\s*$`)
	statsLine  = regexp.MustCompile(`lavfi\.signalstats\.([A-Z]+)=(\S+)\s*$`)
	freezeLine = regexp.MustCompile(`lavfi\.freezedetect\.(freeze_start|freeze_duration|freeze_end):\s*(\S+)\s*$`)
)

// StatsParser reads metadata=print output for signalstats, matching every
// figure by its key: YAVG is never mistaken for YMAX, and a frame that lacks
// one of the four figures is an error rather than a silently shorter list.
type StatsParser struct {
	samples []Sample
	cur     *Sample
	seen    map[string]bool
	err     error
}

// Feed consumes one line of ffmpeg output.
func (p *StatsParser) Feed(line string) {
	if p.err != nil {
		return
	}
	if m := framePTS.FindStringSubmatch(line); m != nil {
		p.flush()
		t, err := parseFinite(m[1])
		if err != nil {
			p.err = fmt.Errorf("frame time %q: %w", m[1], err)
			return
		}
		p.cur, p.seen = &Sample{T: t}, map[string]bool{}
		return
	}
	m := statsLine.FindStringSubmatch(line)
	if m == nil || p.cur == nil {
		return
	}
	var dst *float64
	switch m[1] {
	case "YAVG":
		dst = &p.cur.YAvg
	case "UAVG":
		dst = &p.cur.UAvg
	case "VAVG":
		dst = &p.cur.VAvg
	case "YMAX":
		dst = &p.cur.YMax
	default:
		return
	}
	v, err := parseFinite(m[2])
	if err != nil {
		p.err = fmt.Errorf("frame at %s: %s %q: %w", fmtNum(p.cur.T), m[1], m[2], err)
		return
	}
	*dst = v
	p.seen[m[1]] = true
}

func (p *StatsParser) flush() {
	if p.cur == nil {
		return
	}
	for _, k := range []string{"YAVG", "UAVG", "VAVG", "YMAX"} {
		if !p.seen[k] {
			p.err = fmt.Errorf("frame at %ss has no %s", fmtNum(p.cur.T), k)
			p.cur = nil
			return
		}
	}
	p.samples = append(p.samples, *p.cur)
	p.cur = nil
}

// Samples ends the input and returns the frames read, or the first parse error.
func (p *StatsParser) Samples() ([]Sample, error) {
	if p.err == nil {
		p.flush()
	}
	if p.err != nil {
		return nil, p.err
	}
	return p.samples, nil
}

// FreezeParser reads freezedetect's log lines. Each freeze_start is paired with
// the freeze_duration that follows it; a start with no duration by the end of
// the input is a freeze still running, and is reported as Open.
type FreezeParser struct {
	freezes []Freeze
	pending *float64
	err     error
}

// Feed consumes one line of ffmpeg output.
func (p *FreezeParser) Feed(line string) {
	if p.err != nil {
		return
	}
	m := freezeLine.FindStringSubmatch(line)
	if m == nil || m[1] == "freeze_end" {
		return
	}
	v, err := parseFinite(m[2])
	if err != nil {
		p.err = fmt.Errorf("%s %q: %w", m[1], m[2], err)
		return
	}
	if m[1] == "freeze_start" {
		p.pending = &v
		return
	}
	if p.pending != nil {
		p.freezes = append(p.freezes, Freeze{Start: *p.pending, Duration: v})
		p.pending = nil
	}
}

// Freezes returns the completed freezes in order and, when the input ended
// inside a freeze, the start of that one.
func (p *FreezeParser) Freezes() (done []Freeze, open *float64, err error) {
	if p.err != nil {
		return nil, nil, p.err
	}
	return p.freezes, p.pending, nil
}

func parseFinite(s string) (float64, error) {
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, err
	}
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return 0, errors.New("not a finite number")
	}
	return v, nil
}

// fmtNum is how every number reaches an ffmpeg argv or a message.
func fmtNum(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }

func round3(v float64) float64 { return math.Round(v*1000) / 1000 }

// decodeArgv is ffmpeg reading dur seconds of the first video stream from lo,
// with filter applied and the result discarded.
func decodeArgv(path string, lo, dur float64, filter string) []string {
	return []string{"ffmpeg", "-nostdin", "-hide_banner", "-nostats", "-v", "info",
		"-ss", fmtNum(round3(lo)), "-t", fmtNum(round3(dur)), "-i", path,
		"-map", "0:v:0", "-vf", filter, "-f", "null", "-"}
}

const statsFilter = "fps=1,signalstats,metadata=print"

// tail keeps the last few lines of a child's output, for the message when it
// fails.
type tail struct{ lines []string }

func (t *tail) add(line string) {
	if line = strings.TrimSpace(line); line == "" {
		return
	}
	t.lines = append(t.lines, line)
	if len(t.lines) > 5 {
		t.lines = t.lines[1:]
	}
}

func (t *tail) String() string { return strings.Join(t.lines, " | ") }

// fileFailure is a failure that belongs to one file: ffmpeg exited non-zero or
// printed something unreadable. It is reported in that file's entry, where any
// other error from a Runner ends the command.
type fileFailure struct{ msg string }

func (e *fileFailure) Error() string { return e.msg }

// failure splits err into the message to report against the file, when the
// failure is the file's own, and the error that must end the command otherwise.
func failure(err error) (msg string, fatal error) {
	var ff *fileFailure
	if errors.As(err, &ff) {
		return ff.msg, nil
	}
	return "", err
}

func failf(format string, args ...any) error { return &fileFailure{fmt.Sprintf(format, args...)} }

// stream runs argv through r, feeding every line to feed. A non-zero exit is a
// fileFailure carrying the end of the output.
func stream(ctx context.Context, r proc.Runner, argv []string, feed func(string)) error {
	var tl tail
	code, err := r.Stream(ctx, argv, func(line string) {
		tl.add(line)
		feed(line)
	})
	if err != nil {
		return err
	}
	if code != 0 {
		return failf("ffmpeg exited %d: %s", code, &tl)
	}
	return nil
}

// sampleStats decodes dur seconds from lo at one frame per second and returns
// the signalstats of each frame, with times in the file's own timeline.
func sampleStats(ctx context.Context, r proc.Runner, path string, lo, dur float64) ([]Sample, error) {
	var p StatsParser
	if err := stream(ctx, r, decodeArgv(path, lo, dur, statsFilter), p.Feed); err != nil {
		return nil, err
	}
	samples, err := p.Samples()
	if err != nil {
		return nil, failf("reading signalstats output: %v", err)
	}
	for i := range samples {
		samples[i].T = round3(samples[i].T + lo)
	}
	return samples, nil
}
