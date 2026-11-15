package cards

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/reclyptor/mediaops/internal/cli"
	"github.com/reclyptor/mediaops/kit/proc"
)

func decodeFind(t *testing.T, r Tools, args ...string) FindReport {
	t.Helper()
	out, _ := run(t, r, args...)
	var rep FindReport
	if err := json.Unmarshal([]byte(out), &rep); err != nil {
		t.Fatalf("stdout is not a find report: %v\n%s", err, out)
	}
	return rep
}

func decodeFreeze(t *testing.T, r Tools, args ...string) FreezeReport {
	t.Helper()
	out, _ := run(t, r, args...)
	var rep FreezeReport
	if err := json.Unmarshal([]byte(out), &rep); err != nil {
		t.Fatalf("stdout is not a freeze report: %v\n%s", err, out)
	}
	return rep
}

// never fails the test if anything is decoded.
type never struct{ t *testing.T }

func (n never) Run(context.Context, ...string) (proc.Result, error) {
	n.t.Error("a child was started")
	return proc.Result{}, nil
}

func (n never) Pipe(context.Context, []string, func(io.Reader) error) ([]byte, int, error) {
	n.t.Error("a child was started")
	return nil, 0, nil
}

func (n never) Stream(context.Context, []string, func(string)) (int, error) {
	n.t.Error("a child was started")
	return 0, nil
}

func TestUsageErrorsDecodeNothing(t *testing.T) {
	file := touch(t, "e01.mkv")
	dir := t.TempDir()
	cases := map[string][]string{
		"no arguments":         {},
		"an unknown command":   {"scan", file},
		"sheet with no mode":   {"sheet", "out.png"},
		"find without a file":  {"find"},
		"freeze without file":  {"freeze", "--lo", "1"},
		"hi equal to lo":       {"find", file, "--lo", "60", "--hi", "60"},
		"hi below lo (find)":   {"find", file, "--lo", "90", "--hi", "60"},
		"hi below lo (freeze)": {"freeze", file, "--lo", "90", "--hi", "60"},
		"a directory":          {"find", dir},
		"a missing file":       {"freeze", filepath.Join(dir, "gone.mkv")},
		"one bad file of two":  {"find", file, dir},
		"an unknown flag":      {"find", file, "--nope", "1"},
		"a flag with no value": {"find", file, "--lo"},
		"a negative lo":        {"find", file, "--lo", "-1"},
		"an empty V range":     {"find", file, "--minv", "200", "--maxv", "100"},
		"an empty U range":     {"freeze", file, "--minu", "200", "--maxu", "100"},
		"a zero minrun":        {"find", file, "--minrun", "0"},
		"a negative tol":       {"find", file, "--tol", "-1"},
		"zero workers":         {"find", file, "--workers", "0"},
		"a zero mindur":        {"freeze", file, "--mindur", "0"},
		"a NaN window":         {"find", file, "--hi", "NaN"},
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			out, code := run(t, never{t}, args...)
			if code != cli.ExitUsage {
				t.Errorf("exit %d, want 2", code)
			}
			if out != "" {
				t.Errorf("stdout = %q, want nothing", out)
			}
		})
	}
}

func TestFlagsMayFollowFiles(t *testing.T) {
	a, b := touch(t, "a.mkv"), touch(t, "b.mkv")
	ff := &fakeFFmpeg{t: t, files: map[string]*fakeFile{a: {length: 10, look: solid(colour{})}, b: {length: 10, look: solid(colour{})}}}
	rep := decodeFind(t, ff, "find", a, "--lo", "1", "--hi", "5", b, "--minrun", "2")
	if rep.Params.Lo != 1 || rep.Params.MinRun != 2 || rep.Total != 2 {
		t.Fatalf("%+v", rep)
	}
}

func TestExitCodes(t *testing.T) {
	card := touch(t, "card.mkv")
	bare := touch(t, "bare.mkv")
	broken := touch(t, "broken.mkv")
	ff := &fakeFFmpeg{t: t, files: map[string]*fakeFile{
		card:   {length: 200, look: solid(colour{y: 77, u: 105, v: 180, ymax: 235})},
		bare:   {length: 200, look: solid(colour{y: 77, u: 128, v: 128, ymax: 235})},
		broken: {exit: 183},
	}}
	flags := []string{"--minv", "160", "--maxu", "115"}
	cases := []struct {
		name  string
		files []string
		code  int
	}{
		{"every file has a candidate", []string{card, card}, cli.ExitOK},
		{"one file has none", []string{card, bare}, cli.ExitIncomplete},
		{"one file failed", []string{card, broken}, cli.ExitIncomplete},
		{"none is not a pass", []string{bare}, cli.ExitIncomplete},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out, code := run(t, ff, append(append([]string{"find"}, c.files...), flags...)...)
			if code != c.code {
				t.Errorf("exit %d, want %d", code, c.code)
			}
			if !json.Valid([]byte(out)) {
				t.Errorf("stdout is not one JSON value: %q", out)
			}
		})
	}
}

func TestFindReportShape(t *testing.T) {
	card := touch(t, "card.mkv")
	broken := touch(t, "broken.mkv")
	bare := touch(t, "bare.mkv")
	ff := &fakeFFmpeg{t: t, files: map[string]*fakeFile{
		card:   {length: 200, look: solid(colour{y: 77.1, u: 105.2, v: 181, ymax: 235})},
		broken: {exit: 183},
		bare:   {length: 200, look: solid(colour{y: 77, u: 128, v: 128})},
	}}
	out, code := run(t, ff, "find", card, broken, bare, "--lo", "10", "--hi", "16", "--minv", "160")
	if code != cli.ExitIncomplete {
		t.Fatalf("exit %d", code)
	}
	var raw struct {
		Mode   string
		Params map[string]any
		Files  []map[string]any
		Found  int
		Total  int
	}
	if err := json.Unmarshal([]byte(out), &raw); err != nil {
		t.Fatal(err)
	}
	if raw.Mode != "find" || raw.Found != 1 || raw.Total != 3 || len(raw.Files) != 3 {
		t.Fatalf("%s", out)
	}
	for _, k := range []string{"lo", "hi", "miny", "maxy", "minu", "maxu", "minv", "maxv", "minrun", "static", "tol"} {
		if _, ok := raw.Params[k]; !ok {
			t.Errorf("params lack %q", k)
		}
	}
	f0 := raw.Files[0]
	if f0["path"] != card || f0["samples"] != float64(6) || f0["best"] != float64(13) {
		t.Errorf("file 0 = %v", f0)
	}
	if w, _ := f0["window"].([]any); len(w) != 2 || w[0] != float64(10) || w[1] != float64(16) {
		t.Errorf("window = %v", f0["window"])
	}
	c := f0["candidates"].([]any)[0].(map[string]any)
	for _, k := range []string{"start", "end", "at", "frames", "yavg", "uavg", "vavg"} {
		if _, ok := c[k]; !ok {
			t.Errorf("candidate lacks %q: %v", k, c)
		}
	}
	if len(raw.Files[1]) != 2 || raw.Files[1]["error"] == nil || raw.Files[1]["path"] != broken {
		t.Errorf("a failed file is {path, error}, got %v", raw.Files[1])
	}
	f2 := raw.Files[2]
	if cs, ok := f2["candidates"].([]any); !ok || len(cs) != 0 {
		t.Errorf("a file without candidates has an empty list, got %v", f2["candidates"])
	}
	if _, ok := f2["best"]; ok {
		t.Errorf("best must be absent when there is no candidate: %v", f2)
	}
}

func TestZeroSamplesIsTheFilesError(t *testing.T) {
	path := touch(t, "short.mkv")
	ff := &fakeFFmpeg{t: t, files: map[string]*fakeFile{path: {length: 50, look: solid(colour{})}}}
	out, code := run(t, ff, "find", path)
	if code != cli.ExitIncomplete {
		t.Fatalf("exit %d", code)
	}
	var rep FindReport
	if err := json.Unmarshal([]byte(out), &rep); err != nil {
		t.Fatal(err)
	}
	if e := rep.Files[0].Error; !strings.Contains(e, "no frames") {
		t.Fatalf("error = %q", e)
	}
}

func TestFailedChildCarriesTheEndOfItsOutput(t *testing.T) {
	path := touch(t, "bad.mkv")
	ff := &fakeFFmpeg{t: t, files: map[string]*fakeFile{path: {exit: 183}}}
	rep := decodeFind(t, ff, "find", path)
	if e := rep.Files[0].Error; e != "ffmpeg exited 183: Invalid data found when processing input" {
		t.Fatalf("error = %q", e)
	}
}

func TestMalformedOutputIsTheFilesError(t *testing.T) {
	path := touch(t, "odd.mkv")
	r := streamFunc(func(_ context.Context, _ []string, onLine func(string)) (int, error) {
		onLine("[Parsed_metadata_2 @ 0x1] frame:0    pts:0       pts_time:0")
		onLine("[Parsed_metadata_2 @ 0x1] lavfi.signalstats.YAVG=40")
		return 0, nil
	})
	rep := decodeFind(t, r, "find", path)
	if e := rep.Files[0].Error; !strings.Contains(e, "has no UAVG") {
		t.Fatalf("error = %q", e)
	}
}

func TestRunnerFailureEndsTheCommand(t *testing.T) {
	path := touch(t, "a.mkv")
	boom := errors.New("proc: locating ffmpeg: not found")
	r := streamFunc(func(context.Context, []string, func(string)) (int, error) { return -1, boom })
	out, code := run(t, r, "find", path)
	if code != cli.ExitInternal || out != "" {
		t.Fatalf("exit %d, stdout %q", code, out)
	}
}

type streamFunc func(ctx context.Context, argv []string, onLine func(string)) (int, error)

func (f streamFunc) Run(context.Context, ...string) (proc.Result, error) { panic("not used") }
func (f streamFunc) Pipe(context.Context, []string, func(io.Reader) error) ([]byte, int, error) {
	panic("not used")
}
func (f streamFunc) Stream(ctx context.Context, argv []string, onLine func(string)) (int, error) {
	return f(ctx, argv, onLine)
}

func TestWorkersAreBoundedAndResultsKeepArgumentOrder(t *testing.T) {
	const n, workers = 9, 3
	dir := t.TempDir()
	var files []string
	for i := range n {
		p := filepath.Join(dir, string(rune('a'+i))+".mkv")
		if err := os.WriteFile(p, nil, 0o644); err != nil {
			t.Fatal(err)
		}
		files = append(files, p)
	}
	var live, peak atomic.Int32
	var mu sync.Mutex
	r := streamFunc(func(_ context.Context, argv []string, onLine func(string)) (int, error) {
		cur := live.Add(1)
		defer live.Add(-1)
		mu.Lock()
		peak.Store(max(peak.Load(), cur))
		mu.Unlock()
		// earlier files finish last
		name := filepath.Base(argAfter(argv, "-i"))
		time.Sleep(time.Duration('k'-name[0]) * 8 * time.Millisecond)
		for _, l := range statsFrame(0, colour{y: 1, u: 2, v: 3, ymax: 4}) {
			onLine(l)
		}
		return 0, nil
	})
	rep := decodeFind(t, r, append([]string{"find"}, append(files, "--workers", "3")...)...)
	for i, f := range rep.Files {
		if f.Path != files[i] {
			t.Fatalf("result %d is %s, want %s", i, f.Path, files[i])
		}
	}
	if p := peak.Load(); p > workers || p < 2 {
		t.Errorf("peak concurrency %d, want 2..%d", p, workers)
	}
}

func TestReadLocatedRoundTrip(t *testing.T) {
	card := touch(t, "card.mkv")
	broken := touch(t, "broken.mkv")
	ff := &fakeFFmpeg{t: t, files: map[string]*fakeFile{
		card:   {length: 300, look: solid(colour{y: 77, u: 105, v: 181, ymax: 235}), freezes: []fakeFreeze{{start: 200, duration: 5, floor: -30}}},
		broken: {exit: 1},
	}}
	for _, mode := range [][]string{{"find", card, broken, "--minv", "160"}, {"freeze", card, broken}} {
		out, _ := run(t, ff, mode...)
		got, err := ReadLocated(strings.NewReader(out))
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 2 || got[0].Path != card || len(got[0].At) == 0 || got[0].Best == nil || got[1].Error == "" || got[1].Best != nil {
			t.Errorf("%s: %+v", mode[0], got)
		}
	}
	if _, err := ReadLocated(strings.NewReader(`{"mode":"points","files":[]}`)); err == nil {
		t.Error("a report of another mode was accepted")
	}
}

func jsonInto(s string, v any) error { return json.Unmarshal([]byte(s), v) }
