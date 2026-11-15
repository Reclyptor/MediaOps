package dhash

import (
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

type fixtureTrack struct {
	Path         string   `json:"path"`
	PreviewStart *float64 `json:"preview_start"`
	Body         []string `json:"body"`
	Preview      []string `json:"preview"`
}

type fixture struct {
	Frames            int                     `json:"frames"`
	PartialBytes      int                     `json:"partial_bytes"`
	Hashes            []string                `json:"hashes"`
	FingerprintArgv   [][]string              `json:"fingerprint_argv"`
	StaticcheckArgv   [][]string              `json:"staticcheck_argv"`
	Tracks            map[string]fixtureTrack `json:"tracks"`
	Scores            map[string]float64      `json:"scores"`
	MatchStdout       string                  `json:"match_stdout"`
	DupcheckTarget    string                  `json:"dupcheck_target"`
	DupcheckStdout    string                  `json:"dupcheck_stdout"`
	StaticCases       map[string][]string     `json:"staticcheck_cases"`
	StaticcheckStdout string                  `json:"staticcheck_stdout"`
}

func loadFixture(t *testing.T) (fixture, []byte) {
	t.Helper()
	b, err := os.ReadFile("testdata/python.json")
	if err != nil {
		t.Fatal(err)
	}
	var f fixture
	if err := json.Unmarshal(b, &f); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile("testdata/frames.gray")
	if err != nil {
		t.Fatal(err)
	}
	return f, raw
}

func parseAll(t *testing.T, hex []string) []uint64 {
	t.Helper()
	out := make([]uint64, len(hex))
	for i, s := range hex {
		h, err := Parse(s)
		if err != nil {
			t.Fatal(err)
		}
		out[i] = h
	}
	return out
}

func TestHashesMatchPython(t *testing.T) {
	f, raw := loadFixture(t)
	if want := f.Frames*FrameSize + f.PartialBytes; len(raw) != want {
		t.Fatalf("frames.gray is %d bytes, fixture says %d", len(raw), want)
	}
	got := Hashes(raw)
	if len(got) != f.Frames || len(f.Hashes) != f.Frames {
		t.Fatalf("got %d hashes, fixture has %d for %d frames", len(got), len(f.Hashes), f.Frames)
	}
	for i, h := range got {
		if Format(h) != f.Hashes[i] {
			t.Errorf("frame %d = %s, Python %s", i, Format(h), f.Hashes[i])
		}
	}
	if n := len(Hashes(raw[:len(raw)-f.PartialBytes])); n != f.Frames {
		t.Errorf("without the partial frame: %d hashes, want %d", n, f.Frames)
	}
}

func TestHashEdgeFrames(t *testing.T) {
	fill := func(f func(i int) byte) []byte {
		px := make([]byte, FrameSize)
		for i := range px {
			px[i] = f(i)
		}
		return px
	}
	tests := []struct {
		name string
		px   []byte
		want uint64
	}{
		{"all zero", fill(func(int) byte { return 0 }), 0},
		{"all 255", fill(func(int) byte { return 255 }), 0},
		{"increasing", fill(func(i int) byte { return byte(i) }), 0},
		{"decreasing", fill(func(i int) byte { return byte(255 - i) }), 0xffffffffffffffff},
		{"alternating", fill(func(i int) byte { return byte(i % 2 * 200) }), 0x55aa55aa55aa55aa},
		{"bright left column", fill(func(i int) byte {
			if i%Width == 0 {
				return 200
			}
			return 10
		}), 0x8080808080808080},
		{"dark right column", fill(func(i int) byte {
			if i%Width == Width-1 {
				return 10
			}
			return 200
		}), 0x0101010101010101},
		{"equal neighbours are not greater", fill(func(i int) byte { return 7 }), 0},
	}
	for _, tt := range tests {
		if got := Hash(tt.px); got != tt.want {
			t.Errorf("%s: %016x, want %016x", tt.name, got, tt.want)
		}
	}
}

func TestHashBitOrder(t *testing.T) {
	px := make([]byte, FrameSize)
	px[0] = 1 // row 0, comparison 0: the most significant bit
	if got := Hash(px); got != 1<<63 {
		t.Errorf("first comparison = %016x, want the top bit", got)
	}
	px = make([]byte, FrameSize)
	px[Width*Height-2] = 1 // last row, last comparison: the least significant bit
	if got := Hash(px); got != 1 {
		t.Errorf("last comparison = %016x, want the bottom bit", got)
	}
}

func TestHashPanicsOnWrongSize(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("Hash of a short frame did not panic")
		}
	}()
	Hash(make([]byte, FrameSize-1))
}

func TestHashesDropsPartial(t *testing.T) {
	if got := Hashes(make([]byte, FrameSize-1)); len(got) != 0 {
		t.Errorf("got %d hashes from less than a frame", len(got))
	}
	if got := Hashes(nil); len(got) != 0 {
		t.Errorf("got %d hashes from nothing", len(got))
	}
}

func TestDistance(t *testing.T) {
	if d := Distance(0, 0xffffffffffffffff); d != 64 {
		t.Errorf("distance = %d, want 64", d)
	}
	if d := Distance(0b1010, 0b0110); d != 2 {
		t.Errorf("distance = %d, want 2", d)
	}
}

func TestOverlapEdges(t *testing.T) {
	if Overlap(nil, []uint64{1}, Threshold) != 0 || Overlap([]uint64{1}, nil, Threshold) != 0 {
		t.Error("an empty side must score 0")
	}
	a := []uint64{0, 0xff, 1 << 40}
	b := []uint64{0b1111111} // 7 bits from 0, 7 from 0xff? 0xff^0x7f = 1 bit
	if got := Overlap(a, b, 6); got != 1.0/3 {
		t.Errorf("overlap = %v, want 1/3 (only 0xff is within 6 bits)", got)
	}
	if got := Overlap(a, b, 7); got != 2.0/3 {
		t.Errorf("overlap at 7 = %v, want 2/3", got)
	}
}

func TestFormatParse(t *testing.T) {
	if got := Format(0xab); got != "00000000000000ab" {
		t.Errorf("Format = %q", got)
	}
	h, err := Parse("ffffffffffffffff")
	if err != nil || h != ^uint64(0) {
		t.Errorf("Parse = %x, %v", h, err)
	}
	for _, bad := range []string{"", "abc", "00000000000000ab0", "000000000000000g", "+00000000000000a", "0x00000000000000a"} {
		if _, err := Parse(bad); err == nil {
			t.Errorf("Parse(%q) succeeded", bad)
		}
	}
}

func TestArgvMatchesPython(t *testing.T) {
	f, _ := loadFixture(t)
	from := 1234.5
	if got := Argv("/x.mkv", 2.0, nil); !reflect.DeepEqual(got, f.FingerprintArgv[0]) {
		t.Errorf("fingerprint body argv = %q, Python %q", got, f.FingerprintArgv[0])
	}
	if got := Argv("/x.mkv", 4.0, &from); !reflect.DeepEqual(got, f.FingerprintArgv[1]) {
		t.Errorf("fingerprint preview argv = %q, Python %q", got, f.FingerprintArgv[1])
	}
	if got := Argv("/x.mkv", 1.0, nil); !reflect.DeepEqual(got, f.StaticcheckArgv[0]) {
		t.Errorf("staticcheck argv = %q, Python %q", got, f.StaticcheckArgv[0])
	}
}

func TestArgvFloatsPrintLikePython(t *testing.T) {
	for _, tt := range []struct {
		from float64
		want string
	}{{1300, "1300.0"}, {0, "0.0"}, {95.25, "95.25"}, {1234.5, "1234.5"}, {0.1, "0.1"}} {
		got := Argv("/x.mkv", 1, &tt.from)
		if got[4] != "-ss" || got[5] != tt.want {
			t.Errorf("seek %v printed as %q, want %q", tt.from, got[5], tt.want)
		}
	}
}

func TestOverlapMatchesPythonScores(t *testing.T) {
	f, _ := loadFixture(t)
	if len(f.Scores) == 0 {
		t.Fatal("fixture has no scores")
	}
	nonzero := 0
	for key, want := range f.Scores {
		a, b, ok := strings.Cut(key, ">")
		if !ok {
			t.Fatalf("bad score key %q", key)
		}
		preview, body := parseAll(t, f.Tracks[a].Preview), parseAll(t, f.Tracks[b].Body)
		if got := Overlap(preview, body, Threshold); got != want {
			t.Errorf("%s = %v, Python %v", key, got, want)
		}
		if want > 0 {
			nonzero++
		}
	}
	if nonzero < 5 {
		t.Errorf("only %d non-zero scores: the planted series is not exercising Overlap", nonzero)
	}
}

var matchLine = regexp.MustCompile(`^(\S+) -> (\S+)  \(match (\d\.\d\d), runner-up (\S+) (\d\.\d\d), gap (\d\.\d\d)\)`)

func TestMatchOutputFiguresMatchOverlap(t *testing.T) {
	f, _ := loadFixture(t)
	lines := 0
	for _, line := range strings.Split(f.MatchStdout, "\n") {
		m := matchLine.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		lines++
		a, best, second := m[1], m[2], m[4]
		pv := parseAll(t, f.Tracks[a].Preview)
		bs := Overlap(pv, parseAll(t, f.Tracks[best].Body), Threshold)
		ss := Overlap(pv, parseAll(t, f.Tracks[second].Body), Threshold)
		figures := []struct {
			name         string
			got, printed string
		}{
			{"match", fmt.Sprintf("%.2f", bs), m[3]},
			{"runner-up", fmt.Sprintf("%.2f", ss), m[5]},
			{"gap", fmt.Sprintf("%.2f", bs-ss), m[6]},
		}
		for _, fig := range figures {
			if fig.got != fig.printed {
				t.Errorf("%s: %s = %s, Python printed %s", a, fig.name, fig.got, fig.printed)
			}
		}
		if pyBest := f.Scores[a+">"+best]; pyBest != bs {
			t.Errorf("%s>%s: %v vs fixture %v", a, best, bs, pyBest)
		}
	}
	if lines != 5 {
		t.Errorf("parsed %d match lines, want 5", lines)
	}
}

var dupLine = regexp.MustCompile(`^   (\S+)  (\d\.\d{3})$`)
var controlLine = regexp.MustCompile(`\((\S+) vs (\S+)\): (\d\.\d{3})$`)

func TestDupcheckOutputFiguresMatchOverlap(t *testing.T) {
	f, _ := loadFixture(t)
	target := parseAll(t, f.Tracks[f.DupcheckTarget].Body)
	rows, controls := 0, 0
	for _, line := range strings.Split(f.DupcheckStdout, "\n") {
		if m := dupLine.FindStringSubmatch(line); m != nil {
			rows++
			got := fmt.Sprintf("%.3f", Overlap(target, parseAll(t, f.Tracks[m[1]].Body), Threshold))
			if got != m[2] {
				t.Errorf("%s: %s, Python printed %s", m[1], got, m[2])
			}
		}
		if m := controlLine.FindStringSubmatch(line); m != nil {
			controls++
			got := fmt.Sprintf("%.3f", Overlap(parseAll(t, f.Tracks[m[1]].Body), parseAll(t, f.Tracks[m[2]].Body), Threshold))
			if got != m[3] {
				t.Errorf("control %s vs %s: %s, Python printed %s", m[1], m[2], got, m[3])
			}
		}
	}
	if rows != 6 || controls != 1 {
		t.Errorf("parsed %d rows and %d controls, want 6 and 1", rows, controls)
	}
	if v, _ := strconv.ParseFloat("0.750", 64); Overlap(target, parseAll(t, f.Tracks["t05b"].Body), Threshold) != v {
		t.Error("t05 against its alternate cut is not 0.750")
	}
}
