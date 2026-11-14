package split

import (
	"context"
	"fmt"
	"math"
	"reflect"
	"strings"
	"sync"

	"github.com/reclyptor/mediaops/kit/ffprobe"
	"github.com/reclyptor/mediaops/kit/frames"
)

const (
	// minBytesPercent and maxBytesPercent bound the parts' total size against
	// the source's. One set of headers becomes N and the Play-All's chapter
	// table is dropped, so a small shortfall is expected and a large one is not.
	minBytesPercent = 99.0
	maxBytesPercent = 100.5

	// timingToleranceMS is how far a packet's shifted pts may sit from its
	// source's.
	timingToleranceMS = 2.0

	// cutToleranceMS is how far the actual cut may sit from its planned
	// keyframe.
	cutToleranceMS = 1.0

	// seamLeadIn and seamLeadOut bound the source decode around a cut, in
	// seconds. The lead-in must hold the longest tail (120 frames) at 24 fps.
	seamLeadIn  = 6.0
	seamLeadOut = 3.0

	// headFrames and tailFrames are how many decoded frames of each part the
	// seam check keeps.
	headFrames = 48
	tailFrames = 120

	maxLeadingShown = 8
)

// knownOpenGOPNotices are decoder complaints that come with starting a part
// on a keyframe whose GOP is open; they are accepted only on a part that has
// leading pictures.
var knownOpenGOPNotices = []string{
	"mmco: unref short failure",
	"number of reference frames (0+4) exceeds max",
}

// Finding is a check that failed. Part is 1-based; Stream is the source's
// stream index. Either is absent when the problem is not about one.
type Finding struct {
	Part    *int   `json:"part,omitempty"`
	Stream  *int   `json:"stream,omitempty"`
	Problem string `json:"problem"`
}

// PartReport is what verify measured on one part.
type PartReport struct {
	Path     string  `json:"path"`
	Bytes    int64   `json:"bytes"`
	Duration float64 `json:"duration"`
	// Cut is the source pts of the part's first video packet, null for the
	// first part.
	Cut             *float64 `json:"cut"`
	FirstKeyframe   bool     `json:"first_keyframe"`
	LeadingPictures int      `json:"leading_pictures"`
	DecodedFrames   int      `json:"decoded_frames"`
	DecodeErrors    []string `json:"decode_errors"`
}

// StreamReport is the packet accounting of one stream across the parts.
type StreamReport struct {
	Index     int    `json:"index"`
	Type      string `json:"type"`
	Codec     string `json:"codec"`
	Packets   int    `json:"packets"`
	Accounted int    `json:"accounted"`
	Identical bool   `json:"identical"`
	// Compared is "packets" for a one-for-one packet comparison and "bytes"
	// for PCM, which mkvmerge re-frames: its raw sample bytes are compared.
	Compared string `json:"compared"`
	// Bytes and PartBytes are the PCM byte counts of the source and of each part.
	Bytes     int64   `json:"bytes,omitempty"`
	PartBytes []int64 `json:"part_bytes,omitempty"`
	// AUDRemoved counts the delimiter NAL units dropped from the source's
	// packets before comparing.
	AUDRemoved        int     `json:"aud_removed"`
	MaxPTSDeviationMS float64 `json:"max_pts_deviation_ms"`
}

// SeamReport is one cut as seen by V7.
type SeamReport struct {
	AfterPart     int     `json:"after_part"`
	Cut           float64 `json:"cut"`
	LostFrames    int     `json:"lost_frames"`
	LostIdentical bool    `json:"lost_identical"`
}

// VerifyReport is verify's one JSON value.
type VerifyReport struct {
	Source       string         `json:"source"`
	Bytes        int64          `json:"bytes"`
	Duration     float64        `json:"duration"`
	Parts        []PartReport   `json:"parts"`
	Streams      []StreamReport `json:"streams"`
	Seams        []SeamReport   `json:"seams"`
	BytesPercent float64        `json:"bytes_percent"`
	Findings     []Finding      `json:"findings"`
	Notes        []string       `json:"notes"`
	Clean        bool           `json:"clean"`
}

func (r *VerifyReport) find(part, stream *int, format string, args ...any) {
	r.Findings = append(r.Findings, Finding{Part: part, Stream: stream, Problem: fmt.Sprintf(format, args...)})
}

func (r *VerifyReport) note(format string, args ...any) {
	r.Notes = append(r.Notes, fmt.Sprintf(format, args...))
}

func ref(n int) *int { return &n }

// bytesPercent is the parts' total as a percentage of the source, and whether
// it is inside the accepted window, edges included. The window is judged in
// integers so a total exactly on an edge is not moved by rounding.
func bytesPercent(total, source int64) (pct float64, ok bool) {
	pct = 100 * float64(total) / float64(source)
	ok = total*1000 >= source*990 && total*200 <= source*201
	return pct, ok
}

// Verify proves that parts are a clean split of src, reading only. planned is
// the keyframe time of every cut when called from mkv-split --go, nil
// otherwise. A part that fails a check is a finding; an error is for being
// unable to run the checks at all.
func Verify(ctx context.Context, ex Tools, src string, parts []string, planned []float64) (*VerifyReport, error) {
	rep := &VerifyReport{
		Source: src, Parts: []PartReport{}, Streams: []StreamReport{}, Seams: []SeamReport{},
		Findings: []Finding{}, Notes: []string{},
	}

	srcSize, err := statRegular(src)
	if err != nil {
		return nil, fmt.Errorf("source: %w", err)
	}
	rep.Bytes = srcSize
	srcInfo, err := ffprobe.Probe(ctx, ex, src)
	if err != nil {
		return nil, err
	}
	rep.Duration = srcInfo.Format.Duration

	// V1: the parts exist and have a duration.
	if len(parts) < 2 {
		rep.find(nil, nil, "a split has at least two parts, got %d", len(parts))
	}
	infos := make([]ffprobe.Info, len(parts))
	sizes := make([]int64, len(parts))
	var total int64
	for i, p := range parts {
		rep.Parts = append(rep.Parts, PartReport{Path: p, DecodeErrors: []string{}})
		size, err := statRegular(p)
		if err != nil {
			rep.find(ref(i+1), nil, "%v", err)
			continue
		}
		info, err := ffprobe.Probe(ctx, ex, p)
		if err != nil {
			rep.find(ref(i+1), nil, "cannot be read as media: %v", err)
			continue
		}
		sizes[i], infos[i] = size, info
		total += size
		rep.Parts[i].Bytes, rep.Parts[i].Duration = size, info.Format.Duration
	}
	if len(rep.Findings) > 0 {
		return rep.finish(), nil
	}

	// V8: the byte window is cheap and needs only sizes.
	pct, inWindow := bytesPercent(total, srcSize)
	rep.BytesPercent = math.Round(pct*1000) / 1000
	if !inWindow {
		rep.find(nil, nil, "the parts total %.3f%% of the source's bytes, outside %.1f-%.1f%%", pct, minBytesPercent, maxBytesPercent)
	}

	// V2: the same streams, in the same order.
	layoutBad := false
	for i, info := range infos {
		if d := layoutDiff(srcInfo.Streams, info.Streams); d != "" {
			rep.find(ref(i+1), nil, "stream layout differs from the source: %s", d)
			layoutBad = true
		}
	}
	if layoutBad {
		return rep.finish(), nil
	}

	vIdx := -1
	filters := map[int]frames.NALFilter{}
	for _, s := range srcInfo.Streams {
		if s.CodecType == "video" && vIdx < 0 {
			vIdx = s.Index
		}
		filters[s.Index] = payloadFilter(s)
	}
	if vIdx < 0 {
		return nil, errNoVideo
	}

	// File i of this list is the source for 0 and part i-1 after it.
	loaded := make([]*facts, len(parts)+1)
	err = parallel(ctx, len(loaded), func(ctx context.Context, i int) (err error) {
		if i == 0 {
			loaded[0], err = load(ctx, ex, src, srcSize, filters)
		} else {
			loaded[i], err = load(ctx, ex, parts[i-1], sizes[i-1], filters)
		}
		return err
	})
	if err != nil {
		return nil, err
	}
	srcFacts, partFacts := loaded[0], loaded[1:]

	pcm := map[int]*pcmResult{}
	for _, s := range srcInfo.Streams {
		if isPCM(s) {
			if pcm[s.Index], err = comparePCM(ctx, ex, src, parts, s.Index); err != nil {
				return nil, err
			}
		}
	}
	acc := account(rep, srcFacts, partFacts, pcm)
	timing(rep, srcFacts, partFacts, acc, vIdx)

	starts := acc.videoStarts
	cuts := make([]float64, 0, len(parts)-1)
	for i := 1; i < len(parts); i++ {
		cut := math.NaN()
		if starts != nil && starts[i] < len(srcFacts.hashes.Packets[vIdx]) {
			cut = srcFacts.pts(vIdx, starts[i])
			rep.Parts[i].Cut = &cut
		}
		cuts = append(cuts, cut)
	}

	outcomes := make([]partOutcome, len(parts))
	err = parallel(ctx, len(parts), func(ctx context.Context, i int) (err error) {
		outcomes[i], err = checkPart(ctx, ex, &rep.Parts[i], i, parts[i], partFacts[i], vIdx)
		return err
	})
	if err != nil {
		return nil, err
	}
	heads, tails := make([][]string, len(parts)), make([][]string, len(parts))
	for i, o := range outcomes {
		rep.Findings = append(rep.Findings, o.findings...)
		rep.Notes = append(rep.Notes, o.notes...)
		heads[i], tails[i] = o.head, o.tail
	}

	if err := seams(ctx, ex, rep, src, cuts, heads, tails); err != nil {
		return nil, err
	}

	if planned != nil {
		checkPlanned(rep, planned, cuts)
	}
	return rep.finish(), nil
}

func (r *VerifyReport) finish() *VerifyReport {
	r.Clean = len(r.Findings) == 0
	return r
}

// layout is the part of a stream V2 compares.
type layout struct {
	Index, Channels, Width, Height int
	Type, Codec, Lang, Title       string
	Default                        bool
}

func layoutOf(streams []ffprobe.Stream) []layout {
	out := make([]layout, len(streams))
	for i, s := range streams {
		out[i] = layout{
			Index: s.Index, Type: s.CodecType, Codec: s.CodecName, Channels: s.Channels,
			Width: s.Width, Height: s.Height, Lang: s.Tags["language"], Title: s.Tags["title"], Default: s.Default,
		}
	}
	return out
}

// layoutDiff names the first difference between two stream lists, "" when they
// match.
func layoutDiff(want, got []ffprobe.Stream) string {
	w, g := layoutOf(want), layoutOf(got)
	if len(w) != len(g) {
		return fmt.Sprintf("%d streams, the source has %d", len(g), len(w))
	}
	for i := range w {
		if !reflect.DeepEqual(w[i], g[i]) {
			return fmt.Sprintf("stream %d is %+v, the source's is %+v", w[i].Index, g[i], w[i])
		}
	}
	return ""
}

// accounting is what V3 learned that later checks reuse.
type accounting struct {
	// starts[s][p] is the index in the source's packet list of stream s where
	// part p's packets begin; nil for a stream whose accounting failed.
	starts map[int][]int
	// videoStarts is starts for the first video stream.
	videoStarts []int
	// pcm holds the PCM streams that were compared as bytes and matched.
	pcm map[int]pcmAccount
}

// pcmAccount places each part's first PCM byte in the source stream, in
// seconds of source time.
type pcmAccount struct {
	bytesPerSecond int
	offsets        []int64
	problem        string
}

// account is V3: every stream's packets across the parts, laid end to end,
// equal the source's one for one.
func account(rep *VerifyReport, src *facts, parts []*facts, pcm map[int]*pcmResult) accounting {
	acc := accounting{starts: map[int][]int{}, pcm: map[int]pcmAccount{}}
	vFirst := true
	for _, s := range src.info.Streams {
		if !hashedStream(s) {
			continue
		}
		want := src.seqs[s.Index]
		sr := StreamReport{Index: s.Index, Type: s.CodecType, Codec: s.CodecName, Packets: len(want), AUDRemoved: src.aud[s.Index], Compared: "packets"}
		if r := pcm[s.Index]; r != nil {
			accountPCM(rep, src, s, r, &sr, &acc)
			rep.Streams = append(rep.Streams, sr)
			continue
		}

		starts := make([]int, len(parts))
		offset, diverged := 0, false
		for p, f := range parts {
			starts[p] = offset
			got := f.seqs[s.Index]
			for j, d := range got {
				switch {
				case offset+j >= len(want):
					rep.find(ref(p+1), ref(s.Index), "packet %d of the part is past the end of the source's %d packets (extra packets)", j, len(want))
					diverged = true
				case d != want[offset+j]:
					rep.find(ref(p+1), ref(s.Index), "packet %d of the part differs from source packet %d (dropped, duplicated or altered)", j, offset+j)
					diverged = true
				}
				if diverged {
					break
				}
				sr.Accounted++
			}
			if diverged {
				break
			}
			offset += len(got)
		}
		if !diverged && offset != len(want) {
			rep.find(ref(len(parts)), ref(s.Index), "the parts hold %d packets, the source has %d: the split is short", offset, len(want))
			diverged = true
		}
		sr.Identical = !diverged
		rep.Streams = append(rep.Streams, sr)

		if !diverged {
			acc.starts[s.Index] = starts
			if s.CodecType == "video" && vFirst {
				acc.videoStarts = starts
			}
		}
		if s.CodecType == "video" {
			vFirst = false
		}
	}
	return acc
}

// accountPCM is V3 for a PCM stream: the parts' sample bytes laid end to end
// equal the source's.
func accountPCM(rep *VerifyReport, src *facts, s ffprobe.Stream, r *pcmResult, sr *StreamReport, acc *accounting) {
	sr.Compared = "bytes"
	sr.Packets = len(src.hashes.Packets[s.Index])
	sr.Bytes, sr.PartBytes = r.srcBytes, r.partBytes
	sr.Identical = r.identical()
	if !sr.Identical {
		rep.find(nil, ref(s.Index), "the PCM bytes of the parts laid end to end (%d bytes: %v per part) differ from the source's %d bytes",
			r.total(), r.partBytes, r.srcBytes)
		return
	}
	sr.Accounted = sr.Packets
	pa := pcmAccount{offsets: r.offsets()}
	var err error
	if pa.bytesPerSecond, err = pcmBytesPerSecond(s.CodecName, s.SampleRate, s.Channels); err != nil {
		pa.problem = err.Error()
	}
	acc.pcm[s.Index] = pa
}

// timing is V4: after shifting each part by the difference between the
// source's earliest video pts over the part's span and the part's own, every
// packet of every accounted stream lands within the tolerance of its source
// pts.
func timing(rep *VerifyReport, src *facts, parts []*facts, acc accounting, vIdx int) {
	vStarts := acc.starts[vIdx]
	if vStarts == nil {
		return
	}
	for p, f := range parts {
		n := len(f.hashes.Packets[vIdx])
		if n == 0 {
			continue
		}
		srcMin, partMin := math.Inf(1), math.Inf(1)
		for j := 0; j < n; j++ {
			srcMin = math.Min(srcMin, src.pts(vIdx, vStarts[p]+j))
			partMin = math.Min(partMin, f.pts(vIdx, j))
		}
		shift := srcMin - partMin

		for si := range rep.Streams {
			sr := &rep.Streams[si]
			if pa, ok := acc.pcm[sr.Index]; ok {
				pcmTiming(rep, src, f, p, sr, pa, shift)
				continue
			}
			starts := acc.starts[sr.Index]
			if starts == nil {
				continue
			}
			reported := false
			for j := range f.hashes.Packets[sr.Index] {
				dev := math.Abs(src.pts(sr.Index, starts[p]+j)-shift-f.pts(sr.Index, j)) * 1000
				sr.MaxPTSDeviationMS = math.Max(sr.MaxPTSDeviationMS, dev)
				if dev > timingToleranceMS && !reported {
					rep.find(ref(p+1), ref(sr.Index), "packet %d lands %.1f ms from its source pts after the part's shift, over the %.0f ms tolerance", j, dev, timingToleranceMS)
					reported = true
				}
			}
		}
	}
	for i := range rep.Streams {
		rep.Streams[i].MaxPTSDeviationMS = math.Round(rep.Streams[i].MaxPTSDeviationMS*1000) / 1000
	}
}

// pcmTiming is V4 for a PCM stream, whose packets are not the source's: only
// the part's first packet is placed, at the source time of its first byte.
func pcmTiming(rep *VerifyReport, src, part *facts, p int, sr *StreamReport, pa pcmAccount, shift float64) {
	if pa.problem != "" {
		if p == 0 {
			rep.find(nil, ref(sr.Index), "the audio timing cannot be checked: %s", pa.problem)
		}
		return
	}
	if len(part.hashes.Packets[sr.Index]) == 0 || len(src.hashes.Packets[sr.Index]) == 0 {
		return
	}
	at := src.pts(sr.Index, 0) + float64(pa.offsets[p])/float64(pa.bytesPerSecond)
	dev := math.Abs(at-shift-part.pts(sr.Index, 0)) * 1000
	sr.MaxPTSDeviationMS = math.Max(sr.MaxPTSDeviationMS, dev)
	if dev > timingToleranceMS {
		rep.find(ref(p+1), ref(sr.Index), "the first packet lands %.1f ms from its source time after the part's shift, over the %.0f ms tolerance", dev, timingToleranceMS)
	}
}

// partOutcome is what checkPart found about one part, kept apart so parts can
// be checked concurrently and reported in order.
type partOutcome struct {
	findings   []Finding
	notes      []string
	head, tail []string
}

func (o *partOutcome) find(part, stream *int, format string, args ...any) {
	o.findings = append(o.findings, Finding{Part: part, Stream: stream, Problem: fmt.Sprintf(format, args...)})
}

// checkPart runs V5 and V6 on part i and keeps the decoded frame MD5s V7 needs.
func checkPart(ctx context.Context, ex Tools, pr *PartReport, i int, path string, f *facts, vIdx int) (partOutcome, error) {
	var o partOutcome
	part := ref(i + 1)

	// V5: it opens on a keyframe, and its leading pictures are counted.
	pkts, err := ffprobe.Packets(ctx, ex, path, "v:0", "%+#64")
	if err != nil {
		return o, err
	}
	if len(pkts) == 0 {
		o.find(part, ref(vIdx), "no video packets")
		return o, nil
	}
	pr.FirstKeyframe = pkts[0].Keyframe
	if !pr.FirstKeyframe {
		o.find(part, ref(vIdx), "the first video packet is not a keyframe: the part was cut mid-GOP")
	} else if pr.LeadingPictures = countLeading(pkts, 0); pr.LeadingPictures > 0 {
		o.notes = append(o.notes, fmt.Sprintf("part %d starts with %d leading pictures (pts %s)", i+1, pr.LeadingPictures, leadingList(pkts)))
	}

	// V6: a full decode of the part.
	dec, err := frames.DecodeHashes(ctx, ex, path, frames.DecodeOptions{Maps: []string{"0:v:0", "0:a?"}})
	if err != nil {
		return o, err
	}
	video := dec.Packets[0]
	pr.DecodedFrames = len(video)
	if want := len(f.hashes.Packets[vIdx]) - pr.LeadingPictures; len(video) != want {
		o.find(part, ref(vIdx), "decoded %d video frames from %d video packets and %d leading pictures", len(video), len(f.hashes.Packets[vIdx]), pr.LeadingPictures)
	}
	for _, line := range dec.Errors {
		if pr.LeadingPictures > 0 && isOpenGOPNotice(line) {
			o.notes = append(o.notes, fmt.Sprintf("part %d: known open-GOP decoder notice: %s", i+1, line))
			continue
		}
		pr.DecodeErrors = append(pr.DecodeErrors, line)
		o.find(part, nil, "decoder reported: %s", line)
	}

	md5s := make([]string, len(video))
	for k, h := range video {
		md5s[k] = h.MD5
	}
	o.head = md5s[:min(headFrames, len(md5s))]
	o.tail = md5s[max(0, len(md5s)-tailFrames):]
	return o, nil
}

func isOpenGOPNotice(line string) bool {
	for _, n := range knownOpenGOPNotices {
		if strings.Contains(line, n) {
			return true
		}
	}
	return false
}

func leadingList(pkts []ffprobe.Packet) string {
	var times []string
	for i := 1; i < len(pkts) && !pkts[i].Keyframe; i++ {
		if pkts[i].HasPTS && pkts[i].PTS < pkts[0].PTS {
			if len(times) == maxLeadingShown {
				times = append(times, "...")
				break
			}
			times = append(times, fmt.Sprintf("%.3f", pkts[i].PTSTime))
		}
	}
	return strings.Join(times, ", ")
}

// seamOutcome is one cut's V7 result.
type seamOutcome struct {
	checked bool
	problem string
	report  SeamReport
	lost    int
	same    bool
}

// seams is V7: at each cut, the source's own decode shows which frames sit
// between the outgoing part's last frame and the incoming part's first, and a
// frame that is not simply the held last one is a picture lost to the cut.
func seams(ctx context.Context, ex Tools, rep *VerifyReport, src string, cuts []float64, heads, tails [][]string) error {
	out := make([]seamOutcome, len(cuts))
	err := parallel(ctx, len(cuts), func(ctx context.Context, k int) error {
		cut := cuts[k]
		if math.IsNaN(cut) || len(tails[k]) == 0 || len(heads[k+1]) == 0 {
			return nil
		}
		from, to := math.Max(0, cut-seamLeadIn), cut+seamLeadOut
		dec, err := frames.DecodeHashes(ctx, ex, src, frames.DecodeOptions{
			CopyTS: true, SeekFrom: &from, To: &to, Maps: []string{"0:v:0"},
		})
		if err != nil {
			return err
		}
		window := make([]string, len(dec.Packets[0]))
		for i, h := range dec.Packets[0] {
			window[i] = h.MD5
		}
		out[k].checked = true
		lost, err := alignSeam(window, tails[k], heads[k+1])
		if err != nil {
			out[k].problem = err.Error()
			return nil
		}
		same := lostIdentical(lost, tails[k][len(tails[k])-1])
		out[k].report = SeamReport{AfterPart: k + 1, Cut: cut, LostFrames: len(lost), LostIdentical: same}
		out[k].lost, out[k].same = len(lost), same
		return nil
	})
	if err != nil {
		return err
	}
	for k, o := range out {
		after := k + 1
		switch {
		case !o.checked:
			rep.note("seam after part %d not checked: the cut or a part's decoded frames are unavailable", after)
		case o.problem != "":
			rep.find(ref(after+1), nil, "seam at %.3f s: %s", cuts[k], o.problem)
		default:
			rep.Seams = append(rep.Seams, o.report)
			if !o.same {
				rep.find(ref(after+1), nil, "seam at %.3f s: %d frames are lost between the parts and not all repeat the outgoing part's last frame", cuts[k], o.lost)
			}
		}
	}
	return nil
}

// maxParallel bounds the children verify runs at once: each is a decode or a
// full read of a file, so more than a few only fights over the disk.
const maxParallel = 4

// parallel runs fn for 0..n-1, at most maxParallel at a time. The first error
// cancels the rest and is returned.
func parallel(ctx context.Context, n int, fn func(ctx context.Context, i int) error) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var (
		wg    sync.WaitGroup
		mu    sync.Mutex
		first error
	)
	sem := make(chan struct{}, maxParallel)
	for i := 0; i < n; i++ {
		sem <- struct{}{}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			if err := fn(ctx, i); err != nil {
				mu.Lock()
				if first == nil {
					first = err
					cancel()
				}
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	return first
}

// checkPlanned is V9: each actual cut sits on the keyframe that was planned.
func checkPlanned(rep *VerifyReport, planned, cuts []float64) {
	if len(planned) != len(cuts) {
		rep.find(nil, nil, "%d cuts were planned and the parts have %d", len(planned), len(cuts))
		return
	}
	for i, want := range planned {
		if math.IsNaN(cuts[i]) {
			continue
		}
		if d := math.Abs(cuts[i]-want) * 1000; d > cutToleranceMS {
			rep.find(ref(i+2), nil, "the cut is at %.3f s, %.1f ms from the planned keyframe at %.3f s", cuts[i], d, want)
		}
	}
}
