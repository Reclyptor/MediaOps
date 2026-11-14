package bounds

import (
	"context"
	"encoding/json"
	"math"
	"os"
	"reflect"
	"testing"

	"github.com/reclyptor/mediaops/kit/ffprobe"
	"github.com/reclyptor/mediaops/kit/proc"
)

// fixtureRunner answers every ffprobe call with one recorded file.
type fixtureRunner struct{ stdout []byte }

func (f fixtureRunner) Run(context.Context, ...string) (proc.Result, error) {
	return proc.Result{Stdout: f.stdout}, nil
}

func (f fixtureRunner) Stream(context.Context, []string, func(string)) (int, error) {
	panic("unused")
}

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// load returns a recorded Play-All's duration and chapter starts in minutes.
func load(t *testing.T, name string) (float64, []float64) {
	t.Helper()
	info, err := ffprobe.Probe(context.Background(), fixtureRunner{readFixture(t, name)}, "/x/"+name)
	if err != nil {
		t.Fatal(err)
	}
	st := make([]float64, len(info.Chapters))
	for i, c := range info.Chapters {
		st[i] = c.Start / 60
	}
	return info.Format.Duration / 60, st
}

func near(a, b, tol float64) bool { return math.Abs(a-b) <= tol }

type want struct {
	episodes       int
	ep, err, tl    float64
	grade          string
	split          []int
	boundaries     []float64
	hint           float64
	scanLo, scanHi float64 // when set, Scan is used instead of Detect
}

func TestRecordedPlayAlls(t *testing.T) {
	cases := []struct {
		name, file string
		w          want
	}{
		{"fate hint 24", "fate-apocrypha-s01d01.json", want{
			episodes: 6, ep: 23.7503, err: 0.0203, tl: 23.7495, grade: "auto", hint: 24,
			split:      []int{6, 10, 14, 18, 23},
			boundaries: []float64{0.0, 23.75, 47.5, 71.25, 95.0, 118.751},
		}},
		{"fate unhinted scan 9..36", "fate-apocrypha-s01d01.json", want{
			episodes: 6, ep: 23.7503, err: 0.0003, tl: 23.7495, split: []int{6, 10, 14, 18, 23},
			scanLo: 9, scanHi: 36,
		}},
		{"sailor moon hint 24", "sailor-moon-s01d01.json", want{
			episodes: 8, ep: 24.1241, err: 0.0059, tl: 24.1241, grade: "auto", hint: 24,
			split: []int{6, 11, 16, 21, 26, 31, 36},
		}},
		{"demon slayer hint 24", "demon-slayer-s02d02.json", want{
			episodes: 4, ep: 23.3673, err: 0.2261, tl: 25.5317, grade: "auto", hint: 24,
			split: []int{7, 14, 20},
		}},
		{"demon slayer no hint", "demon-slayer-s02d02.json", want{
			episodes: 4, ep: 22.8223, err: 0.2320, tl: 27.1667, grade: "auto",
			split: []int{6, 13, 18},
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d, st := load(t, c.file)
			var r Result
			var ok bool
			if c.w.scanHi > 0 {
				r, ok = Scan(d, st, c.w.scanLo, c.w.scanHi, 1.5)
			} else {
				r, ok = Detect(d, st, c.w.hint)
			}
			if !ok {
				t.Fatal("no pattern")
			}
			if r.Episodes() != c.w.episodes {
				t.Fatalf("%d episodes, want %d", r.Episodes(), c.w.episodes)
			}
			if !near(r.EpisodeLen, c.w.ep, 5e-5) || !near(r.FitError, c.w.err, 5e-5) || !near(r.Tail, c.w.tl, 5e-5) {
				t.Errorf("ep=%.4f err=%.4f tail=%.4f, want %.4f %.4f %.4f", r.EpisodeLen, r.FitError, r.Tail, c.w.ep, c.w.err, c.w.tl)
			}
			if got := SplitChapterIndices(st, r.Boundaries); !reflect.DeepEqual(got, c.w.split) {
				t.Errorf("split chapters = %v, want %v", got, c.w.split)
			}
			if c.w.grade != "" {
				if got := Grade(r.EpisodeLen, r.FitError, c.w.hint); got != c.w.grade {
					t.Errorf("grade = %s, want %s", got, c.w.grade)
				}
			}
			if c.w.boundaries != nil {
				if len(r.Boundaries) != len(c.w.boundaries) {
					t.Fatalf("boundaries = %v", r.Boundaries)
				}
				for i, b := range c.w.boundaries {
					if !near(r.Boundaries[i], b, 5e-4) {
						t.Errorf("boundary %d = %.4f, want %.3f", i, r.Boundaries[i], b)
					}
				}
			}
		})
	}
}

// Without the hint Sailor Moon's mid-episode eyecatch chapters fit E=12 as well
// as E=24, and more episodes win the ordering: 16 half-episodes. The error
// stays under the auto threshold, which is why the hint is required.
func TestHalfEpisodeTrap(t *testing.T) {
	d, st := load(t, "sailor-moon-s01d01.json")
	r, ok := Detect(d, st, 0)
	if !ok {
		t.Fatal("no pattern")
	}
	if r.Episodes() != 16 || !near(r.EpisodeLen, 12.0626, 5e-5) || !near(r.FitError, 0.3223, 5e-5) || !near(r.Tail, 12.0537, 5e-5) {
		t.Fatalf("%d episodes ep=%.4f err=%.4f tail=%.4f", r.Episodes(), r.EpisodeLen, r.FitError, r.Tail)
	}
	wantSplit := []int{3, 5, 8, 11, 13, 16, 18, 20, 23, 26, 28, 30, 33, 36, 38}
	if got := SplitChapterIndices(st, r.Boundaries); !reflect.DeepEqual(got, wantSplit) {
		t.Errorf("split chapters = %v", got)
	}
	if g := Grade(r.EpisodeLen, r.FitError, 0); g != "auto" {
		t.Errorf("unhinted grade = %s: the trap is that it looks fine", g)
	}
	if g := Grade(r.EpisodeLen, r.FitError, 24); g != "review" {
		t.Errorf("grade against the real 24 minute hint = %s, want review", g)
	}
}

// No statistic catches a constant offset. The library's own S02E04-E07, laid
// end to end, put the true cuts at chapters 7, 13 and 19, yet the detector
// grades its split 7,14,20 "auto". The second cut is 1.57 minutes past the end
// of E05.
func TestConstantOffsetEscapesEveryStatistic(t *testing.T) {
	d, st := load(t, "demon-slayer-s02d02.json")
	r, ok := Detect(d, st, 24)
	if !ok {
		t.Fatal("no pattern")
	}
	if g := Grade(r.EpisodeLen, r.FitError, 24); g != "auto" {
		t.Fatalf("grade = %s", g)
	}
	got := SplitChapterIndices(st, r.Boundaries)

	var doc struct {
		Episodes []struct{ Duration float64 } `json:"episodes"`
	}
	if err := json.Unmarshal(readFixture(t, "episodes-demon-slayer-s02.json"), &doc); err != nil {
		t.Fatal(err)
	}
	var truth []int
	var truthSec []float64
	cum := 0.0
	for _, e := range doc.Episodes[:len(doc.Episodes)-1] {
		cum += e.Duration
		truthSec = append(truthSec, cum)
		for i, s := range st {
			if near(s*60, cum, 0.1) {
				truth = append(truth, i+1)
			}
		}
	}
	if want := []int{7, 13, 19}; !reflect.DeepEqual(truth, want) {
		t.Fatalf("true cuts = %v (seconds %v), want chapters %v", truth, truthSec, want)
	}
	if reflect.DeepEqual(got, truth) || !reflect.DeepEqual(got, []int{7, 14, 20}) {
		t.Fatalf("detected cuts = %v, truth %v: the limit is that they differ", got, truth)
	}
	if off := st[got[1]-1] - truthSec[1]/60; !near(off, 1.57, 0.01) {
		t.Errorf("second cut is %.3f minutes past E05's true end, want 1.57", off)
	}
}

func TestWalkRejections(t *testing.T) {
	cases := []struct {
		name   string
		d      float64
		st     []float64
		e      float64
		reason string
	}{
		{"no chapters", 30, nil, 10, "no chapters"},
		{"lead-in over 3 minutes", 60, []float64{3.01, 13, 23, 33, 43}, 10, "lead-in"},
		{"no chapter within tol of a target", 40, []float64{0, 20}, 10, "no chapter near target"},
		{"snap lands within a minute of the last boundary", 10, []float64{0, 0.5, 30}, 1, "boundary too close to the last"},
		{"a single episode", 12, []float64{0, 5}, 10, "fewer than two episodes"},
		{"tail under 0.55 E", 16, []float64{0, 11.5}, 10, "tail out of bounds"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, reason := walk(c.d, c.st, c.e, 1.5); reason != c.reason {
				t.Fatalf("reason = %q, want %q", reason, c.reason)
			}
		})
	}
}

func TestWalkAcceptsTheEdges(t *testing.T) {
	// a lead-in of exactly 3.0 and a snap exactly tol from its target both pass
	r, reason := walk(25.6, []float64{3.0, 14.5, 30}, 10, 1.5)
	if reason != "" {
		t.Fatalf("rejected: %s", reason)
	}
	if !reflect.DeepEqual(r.Boundaries, []float64{3.0, 14.5}) || r.FitError != 1.5 || !near(r.Tail, 11.1, 1e-9) {
		t.Fatalf("result = %+v", r)
	}
}

// The walk gives up on the first chapter that is not close enough, and the gap
// check can never fire after it: a snapped gap is E plus the snap distance, at
// most E+tol. It stays for fidelity with the Python.
func TestWalkGapsStayWithinTolerance(t *testing.T) {
	r, reason := walk(100, []float64{0, 11.4, 21.3, 32.6, 41.9, 53, 63, 74, 83.5, 93}, 10, 1.5)
	if reason != "" {
		t.Fatalf("rejected: %s", reason)
	}
	for i := 1; i < len(r.Boundaries); i++ {
		if g := r.Boundaries[i] - r.Boundaries[i-1]; g < 8.5 || g > 11.5 {
			t.Errorf("gap %d = %.2f", i, g)
		}
	}
}

func TestScanPrefersMoreEpisodesThenLowerError(t *testing.T) {
	// A 100 minute track with chapters every 10 minutes fits E=10 (10
	// episodes) and E=20, 25, 50, 100/3 ...; the most episodes win.
	st := []float64{0, 10, 20, 30, 40, 50, 60, 70, 80, 90}
	r, ok := Scan(100, st, 9, 36, 1.5)
	if !ok || r.Episodes() != 10 {
		t.Fatalf("got %+v ok=%v", r, ok)
	}
	if r.FitError > 0.5 {
		t.Errorf("fit error %.3f", r.FitError)
	}
}

func TestBetterOrdering(t *testing.T) {
	mk := func(err float64, b ...float64) Result {
		return Result{Boundaries: b, FitError: err, EpisodeLen: 10, Tail: 10}
	}
	cases := []struct {
		name string
		a, b Result
		want bool
	}{
		{"more episodes first", mk(0.9, 0, 1, 2), mk(0.1, 0, 1), true},
		{"fewer episodes lose", mk(0.1, 0, 1), mk(0.9, 0, 1, 2), false},
		{"then lower error", mk(0.1, 0, 5), mk(0.2, 0, 4), true},
		{"then boundaries compared element by element", mk(0.1, 0, 4, 9), mk(0.1, 0, 5, 8), true},
		{"a later first difference does not win", mk(0.1, 0, 5, 8), mk(0.1, 0, 4, 9), false},
		{"equal is not better", mk(0.1, 0, 4), mk(0.1, 0, 4), false},
	}
	for _, c := range cases {
		if got := better(c.a, c.b); got != c.want {
			t.Errorf("%s: better = %v, want %v", c.name, got, c.want)
		}
	}
	a, b := mk(0.1, 0, 4), mk(0.1, 0, 4)
	a.EpisodeLen = 9
	if !better(a, b) || better(b, a) {
		t.Error("the mean gap breaks a tie on boundaries")
	}
	a, b = mk(0.1, 0, 4), mk(0.1, 0, 4)
	a.Tail = 9
	if !better(a, b) || better(b, a) {
		t.Error("the tail breaks the last tie")
	}
}

func TestNearestKeepsTheFirstOnATie(t *testing.T) {
	if got := nearest([]float64{0, 20}, 10); got != 0 {
		t.Fatalf("nearest = %v, want the first of two equidistant chapters", got)
	}
}

func TestGradeThresholds(t *testing.T) {
	cases := []struct {
		name          string
		ep, err, hint float64
		want          string
	}{
		{"error at the limit", 24, 0.35, 24, "auto"},
		{"error over the limit", 24, 0.3500001, 24, "review"},
		{"length just inside the drift", 26.87, 0.1, 24, "auto"},
		{"length just outside the drift", 26.89, 0.1, 24, "review"},
		{"short length just inside", 21.13, 0.1, 24, "auto"},
		{"short length just outside", 21.11, 0.1, 24, "review"},
		{"no hint ignores the length", 12, 0.1, 0, "auto"},
		{"no hint still checks the error", 12, 0.5, 0, "review"},
	}
	for _, c := range cases {
		if got := Grade(c.ep, c.err, c.hint); got != c.want {
			t.Errorf("%s: Grade = %s, want %s", c.name, got, c.want)
		}
	}
}

func TestSplitChapterIndices(t *testing.T) {
	st := []float64{0, 5, 10, 15, 20}
	if got := SplitChapterIndices(st, []float64{0, 10, 20}); !reflect.DeepEqual(got, []int{3, 5}) {
		t.Fatalf("got %v", got)
	}
	if got := SplitChapterIndices(st, []float64{0}); len(got) != 0 {
		t.Fatalf("got %v", got)
	}
}
