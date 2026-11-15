package fingerprint

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/reclyptor/mediaops/internal/cli"
	"github.com/reclyptor/mediaops/kit/fsutil"
	"github.com/reclyptor/mediaops/kit/proc"
)

// DefaultWorkers bounds the fingerprints computed at once.
const DefaultWorkers = 6

// maxJobsBytes bounds how much of a jobs file is read.
const maxJobsBytes = 16 << 20

// Job is one line of a jobs file: a track to fingerprint into OUTDIR/Name.json.
type Job struct {
	Path         string
	PreviewStart *float64
	Name         string
}

// Failure is a job that did not finish, with the sentence saying why.
type Failure struct {
	Name  string `json:"name"`
	Error string `json:"error"`
}

// BatchReport is what a batch printed at the end: the jobs it computed, the
// ones a valid record already covered, and the ones that failed, each in jobs
// order.
type BatchReport struct {
	Jobs     int       `json:"jobs"`
	OutDir   string    `json:"out_dir"`
	Computed []string  `json:"computed"`
	Skipped  []string  `json:"skipped"`
	Failed   []Failure `json:"failed"`
}

// ParseJobs reads a jobs file: one `PATH<TAB>PREVIEW_START|none<TAB>NAME` per
// line, blank lines skipped. Every problem is a usage error naming its line.
// A PATH is made absolute and must be a regular file now, so a typo fails
// before any decode starts.
func ParseJobs(path string) ([]Job, error) {
	data, err := readRegular(path, maxJobsBytes, false)
	if err != nil {
		return nil, cli.Usage(fmt.Errorf("cannot read the jobs file %s: %w", path, err))
	}
	var jobs []Job
	firstLine := map[string]int{}
	for i, line := range strings.Split(string(data), "\n") {
		n := i + 1
		if strings.TrimSpace(line) == "" {
			continue
		}
		fields := strings.Split(line, "\t")
		if len(fields) != 3 {
			return nil, cli.Usage(fmt.Errorf("%s line %d: want PATH, PREVIEW_START and NAME separated by tabs, got %d field(s)", path, n, len(fields)))
		}
		start, err := ParsePreviewStart(fields[1])
		if err != nil {
			return nil, cli.Usage(fmt.Errorf("%s line %d: %w", path, n, err))
		}
		name := fields[2]
		if err := checkName(name); err != nil {
			return nil, cli.Usage(fmt.Errorf("%s line %d: %w", path, n, err))
		}
		if prev, dup := firstLine[name]; dup {
			return nil, cli.Usage(fmt.Errorf("%s line %d: name %q is already used on line %d", path, n, name, prev))
		}
		firstLine[name] = n
		abs, err := filepath.Abs(fields[0])
		if err != nil || fields[0] == "" {
			return nil, cli.Usage(fmt.Errorf("%s line %d: bad PATH %q", path, n, fields[0]))
		}
		info, err := os.Stat(abs)
		switch {
		case err != nil:
			return nil, cli.Usage(fmt.Errorf("%s line %d: cannot read %s: %w", path, n, abs, err))
		case !info.Mode().IsRegular():
			return nil, cli.Usage(fmt.Errorf("%s line %d: %s is not a regular file", path, n, abs))
		}
		jobs = append(jobs, Job{Path: abs, PreviewStart: start, Name: name})
	}
	return jobs, nil
}

func checkName(name string) error {
	switch {
	case name == "":
		return errors.New("NAME is empty")
	case strings.Contains(name, "/"):
		return fmt.Errorf("NAME %q contains a /", name)
	case strings.HasPrefix(name, "."):
		return fmt.Errorf("NAME %q starts with a dot", name)
	}
	return nil
}

// PrepareOutDir makes dir if it is missing (its parent must exist) and
// returns its absolute path.
func PrepareOutDir(dir string) (string, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", cli.Usage(fmt.Errorf("bad OUTDIR %q: %w", dir, err))
	}
	info, err := os.Stat(abs)
	switch {
	case err == nil && !info.IsDir():
		return "", cli.Usage(fmt.Errorf("%s is not a directory", abs))
	case err == nil:
		return abs, nil
	case !errors.Is(err, os.ErrNotExist):
		return "", cli.Usage(fmt.Errorf("cannot read %s: %w", abs, err))
	}
	if err := os.Mkdir(abs, fsutil.DirMode); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", cli.Usage(fmt.Errorf("the parent of OUTDIR %s does not exist", abs))
		}
		return "", fmt.Errorf("creating %s: %w", abs, err)
	}
	if err := os.Chmod(abs, fsutil.DirMode); err != nil {
		return "", fmt.Errorf("setting the mode of %s: %w", abs, err)
	}
	return abs, nil
}

type outcome int

const (
	pending outcome = iota
	computed
	skipped
	failed
	interrupted
)

type jobResult struct {
	outcome outcome
	err     error
}

// RunBatch fingerprints every job into outDir with at most workers decodes at
// once, one progress line per job on progress. A job whose record already
// holds the same path and preview start is skipped; one whose file holds
// anything else fails and the file is left alone. A finished record is
// written whole, so a killed batch leaves each job either as it was or done,
// and rerunning it finishes the rest. When ctx ends no new job starts and the
// context's error is returned with the report so far.
func RunBatch(ctx context.Context, p proc.Piper, jobs []Job, outDir string, workers int, progress io.Writer) (BatchReport, error) {
	results := make([]jobResult, len(jobs))
	var mu sync.Mutex
	say := func(format string, a ...any) {
		mu.Lock()
		defer mu.Unlock()
		fmt.Fprintf(progress, format+"\n", a...)
	}

	queue := make(chan int)
	var wg sync.WaitGroup
	for range max(1, min(workers, len(jobs))) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range queue {
				if ctx.Err() != nil {
					continue
				}
				job := jobs[i]
				out, err := runJob(ctx, p, job, outDir)
				if err != nil && ctx.Err() != nil {
					out, err = interrupted, nil
				}
				results[i] = jobResult{out, err}
				switch out {
				case computed:
					say("ok %s", job.Name)
				case skipped:
					say("skip %s", job.Name)
				case failed:
					say("FAIL %s: %v", job.Name, err)
				}
			}
		}()
	}
feed:
	for i := range jobs {
		select {
		case queue <- i:
		case <-ctx.Done():
			break feed
		}
	}
	close(queue)
	wg.Wait()

	rep := BatchReport{Jobs: len(jobs), OutDir: outDir, Computed: []string{}, Skipped: []string{}, Failed: []Failure{}}
	for i, r := range results {
		switch r.outcome {
		case computed:
			rep.Computed = append(rep.Computed, jobs[i].Name)
		case skipped:
			rep.Skipped = append(rep.Skipped, jobs[i].Name)
		case failed:
			rep.Failed = append(rep.Failed, Failure{Name: jobs[i].Name, Error: r.err.Error()})
		}
	}
	if err := ctx.Err(); err != nil {
		return rep, fmt.Errorf("interrupted: %w", err)
	}
	return rep, nil
}

func runJob(ctx context.Context, p proc.Piper, job Job, outDir string) (outcome, error) {
	out := filepath.Join(outDir, job.Name+".json")
	done, err := alreadyDone(out, job)
	if err != nil {
		return failed, err
	}
	if done {
		return skipped, nil
	}
	if err := removeStale(outDir, job.Name); err != nil {
		return failed, err
	}
	rec, err := Compute(ctx, p, job.Path, job.PreviewStart)
	if err != nil {
		return failed, err
	}
	if err := Write(out, rec); err != nil {
		return failed, err
	}
	return computed, nil
}

// alreadyDone reports whether out holds the finished record of job. Anything
// else at out is an error, and is never overwritten.
func alreadyDone(out string, job Job) (bool, error) {
	if _, err := os.Lstat(out); errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	rec, err := Read(out)
	if err != nil {
		return false, fmt.Errorf("%s exists and was left alone: %w", out, err)
	}
	if rec.Path != job.Path || !sameStart(rec.PreviewStart, job.PreviewStart) {
		return false, fmt.Errorf("%s exists for %s (preview_start %s), not %s (preview_start %s); it was left alone",
			out, rec.Path, showStart(rec.PreviewStart), job.Path, showStart(job.PreviewStart))
	}
	return true, nil
}

func sameStart(a, b *float64) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

func showStart(s *float64) string {
	if s == nil {
		return "none"
	}
	return fmt.Sprint(*s)
}

// removeStale deletes the temp files a killed write of name left in dir
// (.NAME.json.tmp-DIGITS) and no other job's.
func removeStale(dir, name string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("listing %s: %w", dir, err)
	}
	prefix := "." + name + ".json.tmp-"
	for _, e := range entries {
		suffix, ok := strings.CutPrefix(e.Name(), prefix)
		if !ok || !e.Type().IsRegular() || !allDigits(suffix) {
			continue
		}
		if err := os.Remove(filepath.Join(dir, e.Name())); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("removing the stale %s: %w", e.Name(), err)
		}
	}
	return nil
}

func allDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
