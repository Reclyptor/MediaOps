package cards

import (
	"reflect"
	"strings"
	"testing"
)

// vp matches only samples whose V is 100 or more.
func vp() FindParams {
	return FindParams{Lo: 0, Hi: 100, MaxY: 255, MaxU: 255, MinV: 100, MaxV: 255, MinRun: 3, Tol: 0.01}
}

// at builds samples one second apart from t=10, from "y:v" pairs.
func at(pairs ...[2]float64) []Sample {
	out := make([]Sample, len(pairs))
	for i, p := range pairs {
		out[i] = Sample{T: float64(10 + i), YAvg: p[0], UAvg: 128, VAvg: p[1], YMax: 200}
	}
	return out
}

func times(runs [][]Sample) [][]float64 {
	out := [][]float64{}
	for _, r := range runs {
		ts := []float64{}
		for _, s := range r {
			ts = append(ts, s.T)
		}
		out = append(out, ts)
	}
	return out
}

func TestRunsGrammar(t *testing.T) {
	hit := func(y float64) [2]float64 { return [2]float64{y, 150} }
	miss := [2]float64{50, 20}
	cases := []struct {
		name   string
		in     []Sample
		static bool
		want   [][]float64
	}{
		{"a run shorter than minrun is dropped", at(hit(1), hit(1), miss, hit(1), hit(1), hit(1)), false, [][]float64{{13, 14, 15}}},
		{"a run still open at the end counts", at(miss, hit(1), hit(1), hit(1)), false, [][]float64{{11, 12, 13}}},
		{"every qualifying run is kept, in time order", at(hit(1), hit(1), hit(1), miss, hit(1), hit(1), hit(1)), false, [][]float64{{10, 11, 12}, {14, 15, 16}}},
		{"without --static the tolerance is ignored", at(hit(10), hit(40), hit(90)), false, [][]float64{{10, 11, 12}}},
		{"--static ends a run where Y moves", at(hit(10), hit(40), hit(90)), true, [][]float64{}},
		{
			// The restart quirk: the sample that broke the run starts the next one.
			"--static restarts with the sample that broke the run",
			at(hit(10), hit(10), hit(10), hit(50), hit(50), hit(50)), true, [][]float64{{10, 11, 12}, {13, 14, 15}},
		},
		{
			"--static compares with the previous sample, not the first",
			at(hit(10), hit(10.005), hit(10.01), hit(10.0149)), true, [][]float64{{10, 11, 12, 13}},
		},
		{"a non-matching sample restarts empty", at(hit(1), hit(1), miss, miss, hit(1), hit(1)), false, [][]float64{}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := vp()
			p.Static = c.static
			if got := times(Runs(c.in, p)); !reflect.DeepEqual(got, c.want) {
				t.Errorf("runs = %v, want %v", got, c.want)
			}
		})
	}
}

func TestRunsStaticToleranceIsStrict(t *testing.T) {
	p := vp()
	p.Static, p.Tol = true, 0.5
	hit := func(y float64) [2]float64 { return [2]float64{y, 150} }
	// 0.5 apart is not < 0.5.
	if got := Runs(at(hit(10), hit(10.5), hit(11), hit(11.5)), p); len(got) != 0 {
		t.Errorf("a step of exactly tol extended a run: %v", times(got))
	}
	if got := Runs(at(hit(10), hit(10.4), hit(10.8), hit(11.2)), p); len(got) != 1 {
		t.Errorf("steps below tol did not make one run: %v", times(got))
	}
}

func TestSummarizePicksTheMostSaturatedRunNotTheFirst(t *testing.T) {
	mk := func(t0 float64, v ...float64) []Sample {
		out := make([]Sample, len(v))
		for i, x := range v {
			out[i] = Sample{T: t0 + float64(i), YAvg: 50, UAvg: 100, VAvg: x}
		}
		return out
	}
	cands, best := summarize([][]Sample{
		mk(100, 160, 162, 164),
		mk(200, 170, 190, 180, 176, 182, 178),
		mk(300, 170, 190, 180, 176, 182, 178),
	})
	if len(cands) != 3 {
		t.Fatalf("%d candidates", len(cands))
	}
	if best == nil || *best != 203 {
		t.Fatalf("best = %v, want 203: the run with the highest mean V, middle sample run[len/2]", best)
	}
	c := cands[1]
	if c.Start != 200 || c.End != 205 || c.At != 203 || c.Frames != 6 || c.VAvg != 179.333 || c.YAvg != 50 || c.UAvg != 100 {
		t.Errorf("candidate = %+v", c)
	}
}

func TestSummarizeTieKeepsTheFirst(t *testing.T) {
	mk := func(t0 float64) []Sample {
		return []Sample{{T: t0, VAvg: 170}, {T: t0 + 1, VAvg: 170}, {T: t0 + 2, VAvg: 170}}
	}
	_, best := summarize([][]Sample{mk(10), mk(50)})
	if best == nil || *best != 11 {
		t.Fatalf("best = %v, want the first run's 11", best)
	}
}

func TestSummarizeNoRuns(t *testing.T) {
	cands, best := summarize(nil)
	if cands == nil || len(cands) != 0 || best != nil {
		t.Fatalf("%#v, %v", cands, best)
	}
}

func TestFindArgvIsExact(t *testing.T) {
	path := touch(t, "e01.mkv")
	ff := &fakeFFmpeg{t: t, files: map[string]*fakeFile{path: {length: 300, look: func(float64) colour { return colour{} }}}}
	run(t, ff, "find", path)
	want := []string{"ffmpeg", "-nostdin", "-hide_banner", "-nostats", "-v", "info", "-ss", "60", "-t", "140", "-i", path,
		"-map", "0:v:0", "-vf", "fps=1,signalstats,metadata=print", "-f", "null", "-"}
	if len(ff.argvs) != 1 || !reflect.DeepEqual(ff.argvs[0], want) {
		t.Fatalf("argv = %q, want %q", ff.argvs, want)
	}
}

func TestFindFractionalWindowKeepsPlainNumbers(t *testing.T) {
	path := touch(t, "e01.mkv")
	ff := &fakeFFmpeg{t: t, files: map[string]*fakeFile{path: {length: 300, look: func(float64) colour { return colour{} }}}}
	run(t, ff, "find", path, "--lo", "92.5", "--hi", "92.6")
	got := strings.Join(ff.argvs[0], " ")
	if !strings.Contains(got, "-ss 92.5 -t 0.1 ") {
		t.Fatalf("argv = %s", got)
	}
}
