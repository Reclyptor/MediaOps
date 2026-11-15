package fingerprint

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/reclyptor/mediaops/internal/cli"
)

func writeJobs(t *testing.T, lines ...string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "jobs.tsv")
	if err := os.WriteFile(p, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestParseJobs(t *testing.T) {
	dir := t.TempDir()
	files := media(t, dir, "a.mkv", "b.mkv")
	jobs, err := ParseJobs(writeJobs(t,
		files[0]+"\t1301.5\tt00",
		"",
		"  ",
		files[1]+"\tnone\tt01",
	))
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 2 || jobs[0].Name != "t00" || *jobs[0].PreviewStart != 1301.5 || jobs[1].PreviewStart != nil || jobs[1].Path != files[1] {
		t.Errorf("jobs = %+v", jobs)
	}
}

func TestParseJobsMakesPathsAbsolute(t *testing.T) {
	dir := t.TempDir()
	media(t, dir, "a.mkv")
	t.Chdir(dir)
	jobs, err := ParseJobs(writeJobs(t, "a.mkv\tnone\tt00"))
	if err != nil {
		t.Fatal(err)
	}
	if jobs[0].Path != filepath.Join(dir, "a.mkv") {
		t.Errorf("path = %q", jobs[0].Path)
	}
}

func TestParseJobsRejects(t *testing.T) {
	dir := t.TempDir()
	f := media(t, dir, "a.mkv")[0]
	if err := os.Mkdir(filepath.Join(dir, "d"), 0o755); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name  string
		lines []string
		want  string
	}{
		{"two fields", []string{f + "\tnone"}, "line 1"},
		{"four fields", []string{f + "\tnone\tt00\tx"}, "line 1"},
		{"space separated", []string{f + " none t00"}, "line 1"},
		{"bad start", []string{f + "\tsoon\tt00"}, "line 1"},
		{"zero start", []string{f + "\t0\tt00"}, "line 1"},
		{"negative start", []string{f + "\t-5\tt00"}, "line 1"},
		{"empty name", []string{f + "\tnone\t"}, "line 1"},
		{"slash in name", []string{f + "\tnone\ta/b"}, "line 1"},
		{"dotted name", []string{f + "\tnone\t.hidden"}, "line 1"},
		{"missing file", []string{f + "\tnone\tt00", filepath.Join(dir, "nope.mkv") + "\tnone\tt01"}, "line 2"},
		{"directory", []string{filepath.Join(dir, "d") + "\tnone\tt00"}, "line 1"},
		{"empty path", []string{"\tnone\tt00"}, "line 1"},
		{"duplicate", []string{f + "\tnone\tt00", "", f + "\tnone\tt00"}, "line 3"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := ParseJobs(writeJobs(t, c.lines...))
			if cli.Code(err) != cli.ExitUsage {
				t.Fatalf("err = %v (exit %d), want a usage error", err, cli.Code(err))
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("%q does not name %s", err, c.want)
			}
		})
	}
	if _, err := ParseJobs(filepath.Join(dir, "nojobs.tsv")); cli.Code(err) != cli.ExitUsage {
		t.Errorf("a missing jobs file is exit %d", cli.Code(err))
	}
	if _, err := ParseJobs(dir); cli.Code(err) != cli.ExitUsage {
		t.Errorf("a directory as jobs file is exit %d", cli.Code(err))
	}
}

// batchRig is a set of media files, their jobs and an output directory.
type batchRig struct {
	t    *testing.T
	jobs []Job
	out  string
}

func newRig(t *testing.T, n int) *batchRig {
	t.Helper()
	dir := t.TempDir()
	r := &batchRig{t: t, out: filepath.Join(dir, "fp")}
	if err := os.Mkdir(r.out, 0o770); err != nil {
		t.Fatal(err)
	}
	for i := range n {
		name := "t0" + string(rune('0'+i))
		path := media(t, dir, name+".mkv")[0]
		var start *float64
		if i < n-1 {
			s := 1300 + float64(i)
			start = &s
		}
		r.jobs = append(r.jobs, Job{Path: path, PreviewStart: start, Name: name})
	}
	return r
}

func (r *batchRig) run(ctx context.Context, p *fakePiper, workers int) (BatchReport, string, error) {
	r.t.Helper()
	var progress bytes.Buffer
	rep, err := RunBatch(ctx, p, r.jobs, r.out, workers, &progress)
	return rep, progress.String(), err
}

func (r *batchRig) record(name string) []byte {
	r.t.Helper()
	b, err := os.ReadFile(filepath.Join(r.out, name+".json"))
	if err != nil {
		r.t.Fatal(err)
	}
	return b
}

func names(jobs []Job) []string {
	var out []string
	for _, j := range jobs {
		out = append(out, j.Name)
	}
	return out
}

func TestBatchComputesThenSkips(t *testing.T) {
	r := newRig(t, 4)
	fake := newFake(t)
	rep, progress, err := r.run(context.Background(), fake, 3)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Jobs != 4 || rep.OutDir != r.out || !reflect.DeepEqual(rep.Computed, names(r.jobs)) || len(rep.Skipped) != 0 || len(rep.Failed) != 0 {
		t.Fatalf("report = %+v", rep)
	}
	for _, j := range r.jobs {
		if !strings.Contains(progress, "ok "+j.Name+"\n") {
			t.Errorf("no `ok %s` in %q", j.Name, progress)
		}
		rec, err := Read(filepath.Join(r.out, j.Name+".json"))
		if err != nil {
			t.Fatal(err)
		}
		if rec.Path != j.Path || (rec.PreviewStart == nil) != (j.PreviewStart == nil) {
			t.Errorf("%s: record %+v for job %+v", j.Name, rec, j)
		}
	}

	before := len(fake.decodes())
	rep, progress, err = r.run(context.Background(), fake, 3)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(rep.Skipped, names(r.jobs)) || len(rep.Computed) != 0 {
		t.Errorf("rerun report = %+v", rep)
	}
	if got := len(fake.decodes()); got != before {
		t.Errorf("a rerun decoded %d more times", got-before)
	}
	if !strings.Contains(progress, "skip t00\n") || strings.Contains(progress, "ok ") {
		t.Errorf("progress = %q", progress)
	}
}

func TestBatchEmptyReportListsAreArrays(t *testing.T) {
	r := newRig(t, 0)
	rep, _, err := r.run(context.Background(), newFake(t), 6)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Computed == nil || rep.Skipped == nil || rep.Failed == nil {
		t.Errorf("report = %+v, want empty lists, not null", rep)
	}
}

func TestBatchFailureIsolatedToItsJob(t *testing.T) {
	r := newRig(t, 5)
	fake := newFake(t)
	fake.failing = map[string]string{r.jobs[1].Path: "Invalid data found when processing input"}
	fake.silent = map[string]bool{r.jobs[2].Path: true}
	fake.silentPreview = map[string]bool{r.jobs[3].Path: true}

	rep, progress, err := r.run(context.Background(), fake, 2)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(rep.Computed, []string{"t00", "t04"}) {
		t.Errorf("computed = %v", rep.Computed)
	}
	if len(rep.Failed) != 3 || rep.Failed[0].Name != "t01" || rep.Failed[1].Name != "t02" || rep.Failed[2].Name != "t03" {
		t.Fatalf("failed = %+v", rep.Failed)
	}
	for i, want := range []string{"Invalid data found", "no frames decoded", "no frames after preview start"} {
		if !strings.Contains(rep.Failed[i].Error, want) {
			t.Errorf("%s: error %q lacks %q", rep.Failed[i].Name, rep.Failed[i].Error, want)
		}
		if !strings.Contains(progress, "FAIL "+rep.Failed[i].Name+": ") {
			t.Errorf("no FAIL line for %s in %q", rep.Failed[i].Name, progress)
		}
		if _, err := os.Lstat(filepath.Join(r.out, rep.Failed[i].Name+".json")); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s left a record behind", rep.Failed[i].Name)
		}
	}
	entries, _ := os.ReadDir(r.out)
	if len(entries) != 2 {
		t.Errorf("directory holds %d entries, want the 2 finished records", len(entries))
	}
}

func TestBatchLeavesNonMatchingRecordsAlone(t *testing.T) {
	r := newRig(t, 4)
	other := filepath.Join(filepath.Dir(r.out), "other.mkv")
	elsewhere := NewRecord(other, nil, []uint64{1}, nil)
	start := 99.0
	retimed := NewRecord(r.jobs[1].Path, &start, []uint64{1}, []uint64{2})
	stale := map[string][]byte{
		"t00": []byte("garbage"),
		"t01": nil,
		"t02": nil,
	}
	if err := os.WriteFile(filepath.Join(r.out, "t00.json"), stale["t00"], 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Write(filepath.Join(r.out, "t01.json"), retimed); err != nil {
		t.Fatal(err)
	}
	if err := Write(filepath.Join(r.out, "t02.json"), elsewhere); err != nil {
		t.Fatal(err)
	}
	keep := map[string][]byte{"t00": r.record("t00"), "t01": r.record("t01"), "t02": r.record("t02")}

	fake := newFake(t)
	rep, _, err := r.run(context.Background(), fake, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Failed) != 3 || !reflect.DeepEqual(rep.Computed, []string{"t03"}) {
		t.Fatalf("report = %+v", rep)
	}
	for name, want := range keep {
		if got := r.record(name); !bytes.Equal(got, want) {
			t.Errorf("%s.json was rewritten", name)
		}
	}
	for _, c := range fake.decodes() {
		if c != r.jobs[3].Path {
			t.Errorf("decoded %s though its job failed before computing", c)
		}
	}
	if !strings.Contains(rep.Failed[1].Error, "preview_start") || !strings.Contains(rep.Failed[2].Error, other) {
		t.Errorf("mismatch errors do not say what differs: %+v", rep.Failed)
	}
}

func TestBatchRefusesNonRegularExistingRecord(t *testing.T) {
	r := newRig(t, 2)
	if err := os.Mkdir(filepath.Join(r.out, "t00.json"), 0o755); err != nil {
		t.Fatal(err)
	}
	rep, _, err := r.run(context.Background(), newFake(t), 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Failed) != 1 || rep.Failed[0].Name != "t00" || !reflect.DeepEqual(rep.Computed, []string{"t01"}) {
		t.Errorf("report = %+v", rep)
	}
}

func TestBatchRemovesOnlyItsOwnStaleTempFiles(t *testing.T) {
	r := newRig(t, 3)
	ours := []string{".t01.json.tmp-123456", ".t01.json.tmp-9"}
	theirs := []string{".t0.json.tmp-1", ".t01.json.json.tmp-5", ".t01.json.tmp-", ".t01.json.tmp-12x", "t01.json.tmp-5"}
	for _, n := range append(append([]string{}, ours...), theirs...) {
		if err := os.WriteFile(filepath.Join(r.out, n), []byte("partial"), 0o660); err != nil {
			t.Fatal(err)
		}
	}
	// t02 has a finished record, so its job is skipped and its temp file stays.
	if _, _, err := (&batchRig{t: t, jobs: r.jobs[2:], out: r.out}).run(context.Background(), newFake(t), 1); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(r.out, ".t02.json.tmp-77"), []byte("partial"), 0o660); err != nil {
		t.Fatal(err)
	}
	theirs = append(theirs, ".t02.json.tmp-77")
	rep, _, err := r.run(context.Background(), newFake(t), 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Failed) != 0 {
		t.Fatalf("report = %+v", rep)
	}
	for _, n := range ours {
		if _, err := os.Lstat(filepath.Join(r.out, n)); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s survived its job", n)
		}
	}
	for _, n := range theirs {
		if _, err := os.Lstat(filepath.Join(r.out, n)); err != nil {
			t.Errorf("%s was removed though it is not a temp file of an affected job: %v", n, err)
		}
	}
}

func TestBatchBoundsWorkers(t *testing.T) {
	r := newRig(t, 6)
	for i := range r.jobs {
		r.jobs[i].PreviewStart = nil
	}
	fake := newFake(t)
	var mu sync.Mutex
	inFlight, peak := 0, 0
	bothRunning := make(chan struct{})
	var once sync.Once
	fake.onCall = func(string, bool) error {
		mu.Lock()
		inFlight++
		peak = max(peak, inFlight)
		// In-flight count returns to 2 for every later pair of jobs, so the
		// channel is closed only the first time.
		if inFlight == 2 {
			once.Do(func() { close(bothRunning) })
		}
		mu.Unlock()
		select {
		case <-bothRunning:
		case <-time.After(10 * time.Second):
			t.Error("two decodes never ran at once under --workers 2")
		}
		mu.Lock()
		inFlight--
		mu.Unlock()
		return nil
	}
	if _, _, err := r.run(context.Background(), fake, 2); err != nil {
		t.Fatal(err)
	}
	if peak != 2 {
		t.Errorf("peak concurrency = %d, want 2", peak)
	}
}

func TestBatchCancelThenRerunMatchesUninterrupted(t *testing.T) {
	whole := newRig(t, 5)
	if _, _, err := whole.run(context.Background(), newFake(t), 1); err != nil {
		t.Fatal(err)
	}

	r := &batchRig{t: t, jobs: whole.jobs, out: filepath.Join(t.TempDir(), "fp")}
	if err := os.Mkdir(r.out, 0o770); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	fake := newFake(t)
	fake.onCall = func(path string, _ bool) error {
		if path == r.jobs[2].Path {
			cancel() // the third job is killed mid-decode
		}
		return nil
	}
	rep, progress, err := r.run(ctx, fake, 1)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want the context's", err)
	}
	if !reflect.DeepEqual(rep.Computed, []string{"t00", "t01"}) || len(rep.Failed) != 0 {
		t.Errorf("interrupted report = %+v", rep)
	}
	if strings.Contains(progress, "FAIL") || strings.Contains(progress, "t03") || strings.Contains(progress, "t02") {
		t.Errorf("progress = %q: an interrupted job is not a failure and later jobs never start", progress)
	}
	entries, _ := os.ReadDir(r.out)
	if len(entries) != 2 {
		t.Errorf("%d files after the interruption, want the 2 finished records", len(entries))
	}

	rep, _, err = r.run(context.Background(), newFake(t), 1)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(rep.Skipped, []string{"t00", "t01"}) || !reflect.DeepEqual(rep.Computed, []string{"t02", "t03", "t04"}) {
		t.Errorf("rerun report = %+v", rep)
	}
	for _, j := range r.jobs {
		if !bytes.Equal(r.record(j.Name), whole.record(j.Name)) {
			t.Errorf("%s differs from the uninterrupted run", j.Name)
		}
	}
}

func TestPrepareOutDir(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "fp")
	got, err := PrepareOutDir(dir)
	if err != nil || got != dir {
		t.Fatalf("PrepareOutDir = %q, %v", got, err)
	}
	if info, _ := os.Stat(dir); info.Mode().Perm() != 0o770 {
		t.Errorf("mode = %v, want 0770", info.Mode().Perm())
	}
	if _, err := PrepareOutDir(dir); err != nil {
		t.Errorf("an existing directory: %v", err)
	}
	file := media(t, base, "f")[0]
	if _, err := PrepareOutDir(file); cli.Code(err) != cli.ExitUsage {
		t.Errorf("a file as OUTDIR: exit %d", cli.Code(err))
	}
	if _, err := PrepareOutDir(filepath.Join(base, "no", "fp")); cli.Code(err) != cli.ExitUsage {
		t.Errorf("a missing parent: exit %d", cli.Code(err))
	}
}
