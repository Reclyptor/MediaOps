package split

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/reclyptor/mediaops/kit/ffprobe"
	"github.com/reclyptor/mediaops/kit/frames"
)

var ms = frames.Rational{Num: 1, Den: 1000}

// pkt is a video packet at pts milliseconds.
func pkt(ms int64, key bool) ffprobe.Packet {
	return ffprobe.Packet{PTS: ms, DTS: ms, HasPTS: true, HasDTS: true, PTSTime: float64(ms) / 1000, Keyframe: key}
}

func TestBytesPercentWindowEdges(t *testing.T) {
	cases := []struct {
		total int64
		pct   float64
		ok    bool
	}{
		{989, 98.9, false},
		{990, 99.0, true},
		{1000, 100, true},
		{1005, 100.5, true},
		{1006, 100.6, false},
	}
	for _, c := range cases {
		pct, ok := bytesPercent(c.total, 1000)
		if ok != c.ok || fmt.Sprintf("%.1f", pct) != fmt.Sprintf("%.1f", c.pct) {
			t.Errorf("%d of 1000: %.3f %v, want %.1f %v", c.total, pct, ok, c.pct, c.ok)
		}
	}
	if _, ok := bytesPercent(99_000_000_000, 100_000_000_000); !ok {
		t.Error("exactly 99% of a large source is outside the window")
	}
}

func seq(prefix string, from, to int) []string {
	var out []string
	for i := from; i < to; i++ {
		out = append(out, prefix+strconv.Itoa(i))
	}
	return out
}

func TestAlignSeamUnique(t *testing.T) {
	window := seq("f", 0, 200)
	lost, err := alignSeam(window, window[10:58], window[60:100])
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(lost, []string{"f58", "f59"}) {
		t.Fatalf("lost %v", lost)
	}
	if lost, err := alignSeam(window, window[10:58], window[58:100]); err != nil || len(lost) != 0 {
		t.Fatalf("adjacent: %v %v", lost, err)
	}
}

func TestAlignSeamAmbiguousTailResolvedByLongerOne(t *testing.T) {
	block := seq("r", 0, 48)
	var window []string
	window = append(window, block...)
	window = append(window, seq("q", 0, 48)...)
	window = append(window, block...)
	window = append(window, seq("h", 0, 24)...)
	tail := append(seq("q", 0, 48), block...)

	if got := occurrences(window, tail[48:]); len(got) != 2 {
		t.Fatalf("the 48 frame tail should be ambiguous, found at %v", got)
	}
	lost, err := alignSeam(window, tail, seq("h", 0, 24))
	if err != nil || len(lost) != 0 {
		t.Fatalf("lost %v, err %v", lost, err)
	}
}

func TestAlignSeamAmbiguousForever(t *testing.T) {
	block := seq("r", 0, 120)
	window := slices.Concat(block, block, seq("h", 0, 24))
	if _, err := alignSeam(window, block, seq("h", 0, 24)); err == nil || !strings.Contains(err.Error(), "more than one place") {
		t.Fatalf("err %v", err)
	}
}

func TestAlignSeamNotFound(t *testing.T) {
	window := seq("f", 0, 200)
	if _, err := alignSeam(window, seq("x", 0, 48), window[60:100]); err == nil || !strings.Contains(err.Error(), "tail") {
		t.Fatalf("err %v", err)
	}
	if _, err := alignSeam(window, window[10:58], seq("x", 0, 24)); err == nil || !strings.Contains(err.Error(), "head") {
		t.Fatalf("err %v", err)
	}
}

func TestAlignSeamOverlap(t *testing.T) {
	window := seq("f", 0, 200)
	_, err := alignSeam(window, window[10:58], window[50:90])
	if err == nil || !strings.Contains(err.Error(), "8 frames before") {
		t.Fatalf("err %v", err)
	}
}

func TestLostFrames(t *testing.T) {
	window := slices.Concat(seq("f", 0, 100), []string{"held", "held"}, seq("g", 0, 100))
	tail := append(seq("f", 52, 100), "held")
	// The tail's last frame is the first "held"; the second is lost and
	// identical to it, which nobody can see.
	lost, err := alignSeam(window, tail, window[102:126])
	if err != nil || !slices.Equal(lost, []string{"held"}) || !lostIdentical(lost, "held") {
		t.Fatalf("lost %v, err %v", lost, err)
	}

	window = slices.Concat(seq("f", 0, 100), []string{"held", "other"}, seq("g", 0, 100))
	lost, err = alignSeam(window, tail, window[102:126])
	if err != nil || !slices.Equal(lost, []string{"other"}) || lostIdentical(lost, "held") {
		t.Fatalf("lost %v, err %v", lost, err)
	}
}

func TestLeadingPictures(t *testing.T) {
	// A keyframe at 100 ms, two pictures shown before it, one after, then the
	// next GOP whose own earlier picture does not count.
	pkts := []ffprobe.Packet{
		pkt(100, true), pkt(80, false), pkt(90, false), pkt(140, false),
		pkt(200, true), pkt(180, false),
	}
	if n := countLeading(pkts, 0); n != 2 {
		t.Fatalf("leading %d, want 2", n)
	}
	if n := countLeading(pkts, 4); n != 1 {
		t.Fatalf("leading %d, want 1", n)
	}
	if n := countLeading(pkts[:1], 0); n != 0 {
		t.Fatalf("leading %d, want 0", n)
	}
}

func TestPlanCutOpenGOPSwitch(t *testing.T) {
	pkts := []ffprobe.Packet{pkt(3000, true), pkt(3040, false), pkt(5000, true), pkt(4960, false), pkt(4980, false), pkt(5040, false), pkt(9000, true)}

	c, problems, notes := planCut(pkts, ms, 5, DefaultTol, false, false)
	if c.LeadingPictures != 2 || len(problems) != 1 || !strings.Contains(problems[0], "open GOP") || len(notes) != 0 {
		t.Fatalf("%+v %v %v", c, problems, notes)
	}
	c, problems, notes = planCut(pkts, ms, 5, DefaultTol, true, false)
	if c.LeadingPictures != 2 || len(problems) != 0 || len(notes) != 1 || !strings.Contains(notes[0], "--allow-open-gop") {
		t.Fatalf("%+v %v %v", c, problems, notes)
	}
}

func TestPlanCutKeyframeRule(t *testing.T) {
	pkts := []ffprobe.Packet{pkt(0, true), pkt(5000, true), pkt(6000, true)}
	cases := []struct {
		t        float64
		chapter  bool
		keyframe float64
		refused  bool
	}{
		{5.000, true, 5, false},
		{5.0005, true, 5, false}, // inside the 1 ms slack
		{4.995, true, 5, false},  // the chapter is 5 ms before the keyframe
		{4.950, true, 5, true},   // 50 ms before it: outside the tolerance
		{5.005, true, 6, true},   // keyframe 5 ms BEFORE the chapter: mkvmerge starts a GOP late
		{2.5, true, 5, true},
		{5.000, false, 5, false},
		{4.995, false, 5, false},
		{5.0005, false, 5, false},
		{5.005, false, 5, false}, // --at: the earlier keyframe, mkvmerge gets its exact time
		{5.040, false, 5, false}, // 40 ms after: inside the tolerance
		{5.050, false, 5, true},  // 50 ms after: outside it
		{4.950, false, 5, true},
		{2.5, false, 0, true}, // a tie goes to the earlier keyframe
	}
	for _, c := range cases {
		cut, problems, _ := planCut(pkts, ms, c.t, DefaultTol, false, !c.chapter)
		if cut.Keyframe != c.keyframe || (len(problems) > 0) != c.refused {
			t.Errorf("t=%v chapter=%v: keyframe %v problems %v, want %v refused=%v", c.t, c.chapter, cut.Keyframe, problems, c.keyframe, c.refused)
		}
	}
	for _, chapter := range []bool{true, false} {
		_, problems, _ := planCut(pkts, ms, 5.5, DefaultTol, false, !chapter)
		if len(problems) != 1 || !strings.Contains(problems[0], "5.000 s before") || !strings.Contains(problems[0], "6.000 s after") {
			t.Errorf("chapter=%v: the finding should name both neighbours: %v", chapter, problems)
		}
	}
	// A tie goes to the earlier keyframe in --at mode.
	tie := []ffprobe.Packet{pkt(5000, true), pkt(5080, true)}
	if cut, _, _ := planCut(tie, ms, 5.040, 0.05, false, true); cut.Keyframe != 5 {
		t.Errorf("tie went to %v", cut.Keyframe)
	}
	if _, problems, _ := planCut(pkts, ms, 7, DefaultTol, false, true); len(problems) != 1 || !strings.Contains(problems[0], "6.000 s before") {
		t.Errorf("past the window: %v", problems)
	}
}

func TestTimestampArg(t *testing.T) {
	cases := []struct {
		pts  int64
		tb   frames.Rational
		want string
	}{
		{1425050, ms, "1425.050s"},
		{5000, ms, "5.000s"},
		{5, ms, "0.005s"},
		{90000, frames.Rational{Num: 1, Den: 90000}, "1.000s"},
		{1, frames.Rational{Num: 1, Den: 24000}, "0.000041667s"},
	}
	for _, c := range cases {
		if got := timestampArg(c.pts, c.tb); got != c.want {
			t.Errorf("%d in %v: %q, want %q", c.pts, c.tb, got, c.want)
		}
	}
}

func TestParseCuts(t *testing.T) {
	if got, err := parseChapterCuts("2, 4,9", 10); err != nil || !slices.Equal(got, []int{2, 4, 9}) {
		t.Fatalf("%v %v", got, err)
	}
	for _, bad := range []string{"1", "11", "4,4", "5,3", "a", "2,", ""} {
		if _, err := parseChapterCuts(bad, 10); err == nil {
			t.Errorf("chapters %q accepted", bad)
		}
	}
	if got, err := parseTimeCuts("1.5,20", 30); err != nil || !slices.Equal(got, []float64{1.5, 20}) {
		t.Fatalf("%v %v", got, err)
	}
	for _, bad := range []string{"0", "30", "-1", "5,5", "9,3", "x", "NaN", "Inf", ""} {
		if _, err := parseTimeCuts(bad, 30); err == nil {
			t.Errorf("times %q accepted", bad)
		}
	}
}

// TestNoRemovalOrChildProcessInPackage is the structural half of "the source
// is only read": the package removes nothing itself (library.DiscardPartial
// does, for .partial names only) and starts children only through kit/proc.
func TestNoRemovalOrChildProcessInPackage(t *testing.T) {
	banned := map[string][]string{
		"os":      {"Remove", "RemoveAll", "Truncate", "Rename", "Link", "Symlink", "WriteFile", "Create", "OpenFile"},
		"unix":    {"Unlink", "Unlinkat", "Rmdir", "Truncate", "Ftruncate", "Rename", "Renameat", "Renameat2"},
		"syscall": {"Unlink", "Unlinkat", "Rmdir", "Truncate", "Ftruncate", "Rename", "Renameat"},
	}
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	var scanned int
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		scanned++
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, file, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, imp := range f.Imports {
			if imp.Path.Value == `"os/exec"` {
				t.Errorf("%s imports os/exec", file)
			}
		}
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
				if pkg, ok := sel.X.(*ast.Ident); ok && slices.Contains(banned[pkg.Name], sel.Sel.Name) {
					t.Errorf("%s: calls %s.%s", fset.Position(call.Pos()), pkg.Name, sel.Sel.Name)
				}
			}
			return true
		})
	}
	if scanned < 6 {
		t.Fatalf("scanned only %d files", scanned)
	}
}

func TestSplitSpec(t *testing.T) {
	var cuts []Cut
	for i, req := range []float64{9.995, 15.0005} {
		pkts := []ffprobe.Packet{pkt(0, true), pkt(10000, true), pkt(15000, true)}
		c, problems, _ := planCut(pkts, ms, req, DefaultTol, false, true)
		if len(problems) != 0 {
			t.Fatalf("cut %d: %v", i, problems)
		}
		cuts = append(cuts, c)
	}
	if got := splitSpec(cuts); got != "timestamps:10.000s,15.000s" {
		t.Fatalf("%q", got)
	}
	if got := splitSpec([]Cut{{Chapter: 6}, {Chapter: 10}}); got != "chapters:6,10" {
		t.Fatalf("%q", got)
	}
}

func TestPCMBytesPerSecond(t *testing.T) {
	cases := []struct {
		codec string
		rate  int
		ch    int
		want  int
		ok    bool
	}{
		{"pcm_s16le", 48000, 2, 192000, true},
		{"pcm_s16be", 48000, 6, 576000, true},
		{"pcm_s24le", 96000, 2, 576000, true},
		{"pcm_s32le", 44100, 2, 352800, true},
		{"pcm_f32le", 48000, 1, 192000, true},
		{"pcm_f64be", 48000, 2, 768000, true},
		{"pcm_u8", 8000, 1, 8000, true},
		{"pcm_alaw", 8000, 1, 0, false},
		{"pcm_bluray", 48000, 2, 0, false},
		{"pcm_s16le", 0, 2, 0, false},
		{"pcm_s16le", 48000, 0, 0, false},
	}
	for _, c := range cases {
		got, err := pcmBytesPerSecond(c.codec, c.rate, c.ch)
		if (err == nil) != c.ok || got != c.want {
			t.Errorf("%s %d Hz x %d: %d, %v", c.codec, c.rate, c.ch, got, err)
		}
	}
}
