package cards

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sync"

	"github.com/reclyptor/mediaops/internal/cli"
	"github.com/reclyptor/mediaops/kit/proc"
)

const usage = "usage: mkv-cards find FILE... [--lo S] [--hi S] [--miny N] [--maxy N] [--minu N] [--maxu N] [--minv N] [--maxv N] [--minrun N] [--static] [--tol N] [--workers N]\n" +
	"       mkv-cards freeze FILE... [--lo S] [--hi S] [--db N] [--mindur S] [--minv N] [--maxv N] [--minu N] [--maxu N] [--minymax N] [--workers N]\n" +
	"       mkv-cards sheet OUT.png --candidates REPORT.json|- [--width 320] [--cols N] [--subs N]\n" +
	"       mkv-cards sheet OUT.png --points POINTS.tsv|- [--width 320] [--cols N] [--subs N]\n" +
	"       mkv-cards sheet OUT.png --chapters FILE [--offset 3] [--width 320] [--cols 7] [--subs N]\n" +
	"       mkv-cards sheet OUT.png --seams PART PART... [--width 256]\n" +
	"       mkv-cards --version"

// Command is the mkv-cards body: args are what follows the program name. It
// prints one JSON report. find and freeze exit 1 when any file has no
// candidate or failed, and sheet when a tile could not be drawn.
func Command(ctx context.Context, t Tools, args []string, streams cli.IO) error {
	if len(args) == 0 {
		return cli.Usage(errors.New(usage))
	}
	switch args[0] {
	case "find":
		return runFind(ctx, t, args[1:], streams)
	case "freeze":
		return runFreeze(ctx, t, args[1:], streams)
	case "sheet":
		return runSheet(ctx, t, args[1:], streams)
	}
	return cli.Usage(fmt.Errorf("unknown subcommand %q\n%s", args[0], usage))
}

func runFind(ctx context.Context, r proc.Runner, args []string, streams cli.IO) error {
	fs := cli.NewFlagSet("mkv-cards find")
	p := FindParams{}
	var workers int
	fs.Float64Var(&p.Lo, "lo", 60, "")
	fs.Float64Var(&p.Hi, "hi", 200, "")
	fs.Float64Var(&p.MinY, "miny", 0, "")
	fs.Float64Var(&p.MaxY, "maxy", 255, "")
	fs.Float64Var(&p.MinU, "minu", 0, "")
	fs.Float64Var(&p.MaxU, "maxu", 255, "")
	fs.Float64Var(&p.MinV, "minv", 0, "")
	fs.Float64Var(&p.MaxV, "maxv", 255, "")
	fs.IntVar(&p.MinRun, "minrun", 3, "")
	fs.BoolVar(&p.Static, "static", false, "")
	fs.Float64Var(&p.Tol, "tol", 0.01, "")
	fs.IntVar(&workers, "workers", 4, "")
	files, err := parseFiles(fs, args)
	if err != nil {
		return err
	}
	if err := checkWindow(p.Lo, p.Hi); err != nil {
		return err
	}
	if err := checkOrdered(map[string][2]float64{"y": {p.MinY, p.MaxY}, "u": {p.MinU, p.MaxU}, "v": {p.MinV, p.MaxV}}); err != nil {
		return err
	}
	switch {
	case p.MinRun < 1:
		return cli.Usage(fmt.Errorf("--minrun must be at least 1, got %d", p.MinRun))
	case !finite(p.Tol) || p.Tol < 0:
		return cli.Usage(fmt.Errorf("--tol must be a number of at least 0, got %v", p.Tol))
	case workers < 1:
		return cli.Usage(fmt.Errorf("--workers must be at least 1, got %d", workers))
	}

	results, err := forEach(ctx, len(files), workers, func(ctx context.Context, i int) (FindFile, error) {
		return findFile(ctx, r, files[i], p)
	})
	if err != nil {
		return err
	}
	rep := FindReport{Mode: "find", Params: p, Files: results, Total: len(results)}
	complete := true
	for _, f := range results {
		if f.Error == "" && len(f.Candidates) > 0 {
			rep.Found++
		} else {
			complete = false
		}
	}
	return finish(streams, rep, complete)
}

func runFreeze(ctx context.Context, r proc.Runner, args []string, streams cli.IO) error {
	fs := cli.NewFlagSet("mkv-cards freeze")
	p := FreezeParams{}
	var workers int
	fs.Float64Var(&p.Lo, "lo", 92, "")
	fs.Float64Var(&p.Hi, "hi", 520, "")
	fs.Float64Var(&p.DB, "db", -30, "")
	fs.Float64Var(&p.MinDur, "mindur", 3.5, "")
	fs.Float64Var(&p.MinV, "minv", 150, "")
	fs.Float64Var(&p.MaxV, "maxv", 255, "")
	fs.Float64Var(&p.MinU, "minu", 0, "")
	fs.Float64Var(&p.MaxU, "maxu", 122, "")
	fs.Float64Var(&p.MinYMax, "minymax", 0, "")
	fs.IntVar(&workers, "workers", 4, "")
	files, err := parseFiles(fs, args)
	if err != nil {
		return err
	}
	if err := checkWindow(p.Lo, p.Hi); err != nil {
		return err
	}
	if err := checkOrdered(map[string][2]float64{"u": {p.MinU, p.MaxU}, "v": {p.MinV, p.MaxV}}); err != nil {
		return err
	}
	switch {
	case !finite(p.DB):
		return cli.Usage(fmt.Errorf("--db must be a number, got %v", p.DB))
	case !finite(p.MinDur) || p.MinDur <= 0:
		return cli.Usage(fmt.Errorf("--mindur must be a number above 0, got %v", p.MinDur))
	case !finite(p.MinYMax):
		return cli.Usage(fmt.Errorf("--minymax must be a number, got %v", p.MinYMax))
	case workers < 1:
		return cli.Usage(fmt.Errorf("--workers must be at least 1, got %d", workers))
	}

	results, err := forEach(ctx, len(files), workers, func(ctx context.Context, i int) (FreezeFile, error) {
		return freezeFile(ctx, r, files[i], p)
	})
	if err != nil {
		return err
	}
	rep := FreezeReport{Mode: "freeze", Params: p, Files: results, Total: len(results)}
	complete := true
	for _, f := range results {
		if f.Error == "" && len(f.Candidates) > 0 {
			rep.Found++
		} else {
			complete = false
		}
	}
	return finish(streams, rep, complete)
}

func finish(streams cli.IO, rep any, complete bool) error {
	if err := cli.PrintJSON(streams.Stdout, rep); err != nil {
		return err
	}
	if !complete {
		return cli.Exit(cli.ExitIncomplete)
	}
	return nil
}

// parseFiles parses fs and returns the FILE arguments as absolute paths, each
// checked to be a regular file before anything is decoded.
func parseFiles(fs *flag.FlagSet, args []string) ([]string, error) {
	rest, err := cli.ParseFlags(fs, args)
	if err != nil || len(rest) == 0 {
		if err == nil {
			err = errors.New("no FILE given")
		}
		return nil, cli.Usage(fmt.Errorf("%w\n%s", err, usage))
	}
	files := make([]string, len(rest))
	for i, f := range rest {
		info, err := os.Stat(f)
		switch {
		case err != nil:
			return nil, cli.Usage(fmt.Errorf("cannot read %s: %w", f, err))
		case !info.Mode().IsRegular():
			return nil, cli.Usage(fmt.Errorf("%s is not a regular file", f))
		}
		if files[i], err = filepath.Abs(f); err != nil {
			return nil, cli.Usage(fmt.Errorf("cannot resolve %s: %w", f, err))
		}
	}
	return files, nil
}

func finite(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }

func checkWindow(lo, hi float64) error {
	switch {
	case !finite(lo) || !finite(hi) || lo < 0:
		return cli.Usage(fmt.Errorf("--lo and --hi must be seconds, with --lo at least 0, got %v and %v", lo, hi))
	case hi <= lo:
		return cli.Usage(fmt.Errorf("--hi (%s) must be above --lo (%s)", fmtNum(hi), fmtNum(lo)))
	}
	return nil
}

// checkOrdered refuses a channel whose minimum is above its maximum, which
// would match nothing and read as "no card".
func checkOrdered(bounds map[string][2]float64) error {
	for ch, b := range bounds {
		switch {
		case !finite(b[0]) || !finite(b[1]):
			return cli.Usage(fmt.Errorf("--min%s and --max%s must be numbers", ch, ch))
		case b[0] > b[1]:
			return cli.Usage(fmt.Errorf("--min%s (%s) is above --max%s (%s), so nothing could match", ch, fmtNum(b[0]), ch, fmtNum(b[1])))
		}
	}
	return nil
}

// forEach runs fn for 0..n-1 on at most workers goroutines and returns the
// results in index order. The first error cancels the rest and is returned.
func forEach[T any](ctx context.Context, n, workers int, fn func(ctx context.Context, i int) (T, error)) ([]T, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	out := make([]T, n)
	var (
		wg    sync.WaitGroup
		once  sync.Once
		first error
		sem   = make(chan struct{}, workers)
	)
	for i := range n {
		sem <- struct{}{}
		if ctx.Err() != nil {
			<-sem
			break
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			v, err := fn(ctx, i)
			if err != nil {
				once.Do(func() { first = err; cancel() })
				return
			}
			out[i] = v
		}()
	}
	wg.Wait()
	if first != nil {
		return nil, first
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return out, nil
}
