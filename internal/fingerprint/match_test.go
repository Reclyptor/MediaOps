package fingerprint

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reclyptor/mediaops/internal/cli"
	"github.com/reclyptor/mediaops/internal/dhash"
)

type pyTrack struct {
	Path         string   `json:"path"`
	PreviewStart *float64 `json:"preview_start"`
	Body         []string `json:"body"`
	Preview      []string `json:"preview"`
}

type pyFixture struct {
	Tracks         map[string]pyTrack `json:"tracks"`
	Scores         map[string]float64 `json:"scores"`
	MatchStdout    string             `json:"match_stdout"`
	DupcheckTarget string             `json:"dupcheck_target"`
	DupcheckStdout string             `json:"dupcheck_stdout"`
}

func loadPython(t testing.TB) pyFixture {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "dhash", "testdata", "python.json"))
	if err != nil {
		t.Fatal(err)
	}
	var f pyFixture
	if err := json.Unmarshal(b, &f); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f pyFixture) records() map[string]Record {
	out := map[string]Record{}
	for name, tr := range f.Tracks {
		out[name] = Record{
			Tool: ToolName, Schema: SchemaVersion, Path: tr.Path, PreviewStart: tr.PreviewStart,
			BodyFPS: BodyFPS, PreviewFPS: PreviewFPS, Body: tr.Body, Preview: tr.Preview,
		}
	}
	return out
}

// writeDir stores records as OUTDIR/NAME.json and returns the directory.
func writeDir(t testing.TB, recs map[string]Record) string {
	t.Helper()
	dir := t.TempDir()
	for name, r := range recs {
		if err := Write(filepath.Join(dir, name+".json"), r); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func mustLoad(t testing.TB, dir string) []Track {
	t.Helper()
	tracks, err := LoadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	return tracks
}

// pythonLine prints a decision exactly as the Python's cmd_match did.
func pythonLine(d decision) string {
	verdict := "[weak]"
	if d.verdict == Confident {
		verdict = "[CONFIDENT]"
	}
	return fmt.Sprintf("%s -> %s  (match %.2f, runner-up %s %.2f, gap %.2f)  %s",
		d.track, d.best.name, d.best.score, d.second.name, d.second.score, d.gap, verdict)
}

func TestMatchReproducesPython(t *testing.T) {
	f := loadPython(t)
	tracks := mustLoad(t, writeDir(t, f.records()))
	decisions, terminal := decide(tracks)

	got := map[string]string{}
	for _, d := range decisions {
		got[d.track] = pythonLine(d)
	}
	for _, name := range terminal {
		got[name] = name + ": no preview segment -- terminal episode or not part of a run"
	}
	var want []string
	for _, line := range strings.Split(f.MatchStdout, "\n") {
		if strings.Contains(line, "->") || strings.Contains(line, "no preview segment") {
			want = append(want, line)
		}
	}
	if len(want) != len(f.Tracks) {
		t.Fatalf("fixture has %d track lines for %d tracks", len(want), len(f.Tracks))
	}
	for _, line := range want {
		name := strings.FieldsFunc(line, func(r rune) bool { return r == ' ' || r == ':' })[0]
		if got[name] != line {
			t.Errorf("%s\n got  %s\n want %s", name, got[name], line)
		}
	}
}

func TestMatchReportShape(t *testing.T) {
	f := loadPython(t)
	rep := Analyze("/fp", mustLoad(t, writeDir(t, f.records())))
	if rep.Tracks != 7 || strings.Join(rep.Terminal, ",") != "t05,t05b" || len(rep.Matches) != 5 || rep.Clean {
		t.Fatalf("report = %+v", rep)
	}
	byTrack := map[string]Match{}
	for _, m := range rep.Matches {
		byTrack[m.Track] = m
	}
	verdicts := map[string]string{"t00": Confident, "t01": Confident, "t02": Weak, "t03": Confident, "t04": Confident}
	successor := map[string]string{"t00": "t01", "t01": "t02", "t02": "t03", "t03": "t04", "t04": "t05"}
	for name, v := range verdicts {
		m := byTrack[name]
		if m.Verdict != v || m.Successor != successor[name] || m.RunnerUp == nil || m.RunnerUpScore == nil || m.Gap == nil {
			t.Errorf("%s = %+v", name, m)
		}
	}
	if m := byTrack["t02"]; *m.RunnerUp != "t04" || m.Score != 0.4 || *m.RunnerUpScore != 0.375 || *m.Gap != 0.025 {
		t.Errorf("t02 = %+v, %v %v %v", m, *m.RunnerUp, *m.RunnerUpScore, *m.Gap)
	}
	if len(rep.Findings) != 1 || rep.Findings[0].Track != "t02" || !strings.HasPrefix(rep.Findings[0].Problem, "weak: t02 -> t03 (match 0.40, runner-up t04 0.38, gap 0.03)") {
		t.Errorf("findings = %+v", rep.Findings)
	}
}

func TestPreviewScoresReproducePython(t *testing.T) {
	f := loadPython(t)
	tracks := mustLoad(t, writeDir(t, f.records()))
	byName := map[string]Track{}
	for _, tr := range tracks {
		byName[tr.Name] = tr
	}
	for key, want := range f.Scores {
		a, b, _ := strings.Cut(key, ">")
		if got := dhash.Overlap(byName[a].Preview, byName[b].Body, dhash.Threshold); got != want {
			t.Errorf("%s = %v, Python %v", key, got, want)
		}
	}
}

// track builds a track whose preview and body are the given hashes.
func track(name string, preview, body []uint64) Track {
	return Track{Name: name, Preview: preview, Body: body}
}

// farHash returns hashes at least 7 bits from each other and from the
// patterns below: a distinct bit pattern per index.
func farHash(i int) uint64 {
	return 0x0123456789abcdef*uint64(2*i+1) ^ uint64(i)<<56
}

func TestFarHashesAreFar(t *testing.T) {
	for i := range 100 {
		for j := range i {
			if d := dhash.Distance(farHash(i), farHash(j)); d <= dhash.Threshold {
				t.Fatalf("farHash(%d) and farHash(%d) are %d bits apart", i, j, d)
			}
		}
	}
}

func TestTiedScoresRankByNameDescending(t *testing.T) {
	p := farHash(1)
	tracks := []Track{
		track("a", []uint64{p}, []uint64{farHash(9)}),
		track("b", nil, []uint64{p}),
		track("c", nil, []uint64{p}),
		track("d", nil, []uint64{farHash(8)}),
	}
	decisions, _ := decide(tracks)
	d := decisions[0]
	if d.best.name != "c" || d.second.name != "b" || d.best.score != 1 || d.second.score != 1 {
		t.Errorf("best %+v, runner-up %+v: a tie goes to the greater name", d.best, d.second)
	}
	if d.verdict != Weak {
		t.Errorf("a tie has gap 0, verdict = %s", d.verdict)
	}
}

func TestVerdictThresholds(t *testing.T) {
	// A 20 frame preview: 6 frames in the best body is 0.30, 3 in the runner-up
	// 0.15, so the gap is exactly 0.15 and the verdict confident.
	var preview []uint64
	for i := range 20 {
		preview = append(preview, farHash(i))
	}
	cases := []struct {
		name         string
		best, second int
		want         string
	}{
		{"exactly at both thresholds", 6, 3, Confident},
		{"gap just under", 6, 4, Weak},
		{"score just under", 5, 0, Weak},
		{"clear", 20, 0, Confident},
		{"runner-up matches nothing", 6, 0, Confident},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tracks := []Track{
				track("a", preview, []uint64{farHash(99)}),
				track("b", nil, preview[:c.best]),
				track("c", nil, append(append([]uint64{}, preview[c.best:][:c.second]...), farHash(98))),
			}
			decisions, _ := decide(tracks)
			if got := decisions[0]; got.verdict != c.want || got.best.name != "b" {
				t.Errorf("best %+v runner-up %+v gap %v: verdict %s, want %s", got.best, got.second, got.gap, got.verdict, c.want)
			}
		})
	}
}

func TestMatchWithoutRunnerUpIsWeak(t *testing.T) {
	p := farHash(1)
	rep := Analyze("/fp", []Track{track("a", []uint64{p}, []uint64{farHash(2)}), track("b", nil, []uint64{p})})
	m := rep.Matches[0]
	if m.Verdict != Weak || m.Successor != "b" || m.Score != 1 || m.RunnerUp != nil || m.RunnerUpScore != nil || m.Gap != nil {
		t.Errorf("match = %+v", m)
	}
	if rep.Clean || len(rep.Findings) != 1 || !strings.Contains(rep.Findings[0].Problem, "no runner-up") {
		t.Errorf("findings = %+v", rep.Findings)
	}
}

func TestSharedSuccessorIsAFinding(t *testing.T) {
	p, q := farHash(1), farHash(2)
	tracks := []Track{
		track("a", []uint64{p}, []uint64{farHash(5)}),
		track("b", []uint64{p, q}, []uint64{farHash(6)}),
		track("c", nil, []uint64{p, q}),
		track("d", nil, []uint64{farHash(7)}),
	}
	rep := Analyze("/fp", tracks)
	for _, m := range rep.Matches {
		if m.Verdict != Confident || m.Successor != "c" {
			t.Fatalf("match = %+v", m)
		}
	}
	if rep.Clean || len(rep.Findings) != 2 || rep.Findings[0].Track != "a" || rep.Findings[1].Track != "b" {
		t.Fatalf("findings = %+v", rep.Findings)
	}
	if !strings.Contains(rep.Findings[0].Problem, "a, b all name c") {
		t.Errorf("problem = %q", rep.Findings[0].Problem)
	}
}

func TestCleanChain(t *testing.T) {
	p, q := farHash(1), farHash(2)
	rep := Analyze("/fp", []Track{
		track("a", []uint64{p}, []uint64{farHash(5)}),
		track("b", []uint64{q}, []uint64{p}),
		track("c", nil, []uint64{q}),
	})
	if !rep.Clean || len(rep.Findings) != 0 || rep.Findings == nil {
		t.Errorf("report = %+v", rep)
	}
}

func TestLoadDirReadsOnlyRegularVisibleJSON(t *testing.T) {
	dir := t.TempDir()
	rec := NewRecord("/m/a.mkv", nil, []uint64{1}, nil)
	for _, n := range []string{"a.json", "b.json"} {
		if err := Write(filepath.Join(dir, n), rec); err != nil {
			t.Fatal(err)
		}
	}
	// None of these are read, so none of them may be able to fail the load.
	if err := os.WriteFile(filepath.Join(dir, ".hidden.json"), []byte("junk"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".a.json.tmp-12"), []byte("junk"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("junk"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "sub.json"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(dir, "a.json"), filepath.Join(dir, "link.json")); err != nil {
		t.Fatal(err)
	}
	tracks := mustLoad(t, dir)
	if len(tracks) != 2 || tracks[0].Name != "a" || tracks[1].Name != "b" {
		t.Errorf("tracks = %+v", tracks)
	}
}

func TestLoadDirRejectsWhatIsNotSchemaOne(t *testing.T) {
	cases := map[string]string{
		"python.json": `{"path": "/m/a.mkv", "preview_start": null, "body": [1, 2, 3], "preview": []}`,
		"schema2.json": `{"tool":"mkv-fingerprint","schema":2,"path":"/m/a.mkv","preview_start":null,` +
			`"body_fps":2,"preview_fps":4,"body":["0000000000000001"],"preview":[]}`,
		"other.json": `{"tool":"mkv-probe","schema":1}`,
	}
	for name, content := range cases {
		dir := t.TempDir()
		if err := Write(filepath.Join(dir, "good.json"), NewRecord("/m/g.mkv", nil, []uint64{1}, nil)); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		_, err := LoadDir(dir)
		if cli.Code(err) != cli.ExitUsage || !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), name) {
			t.Errorf("%s: err = %v (exit %d)", name, err, cli.Code(err))
		}
	}
	if _, err := LoadDir(filepath.Join(t.TempDir(), "absent")); cli.Code(err) != cli.ExitUsage {
		t.Errorf("a missing directory is exit %d", cli.Code(err))
	}
	file := filepath.Join(t.TempDir(), "f")
	if err := os.WriteFile(file, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadDir(file); cli.Code(err) != cli.ExitUsage {
		t.Errorf("a file as directory is exit %d", cli.Code(err))
	}
}
