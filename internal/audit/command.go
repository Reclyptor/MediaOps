package audit

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/reclyptor/mediaops/internal/cli"
	"github.com/reclyptor/mediaops/internal/inventory"
	"github.com/reclyptor/mediaops/internal/manifest"
)

const (
	usageSeries     = "usage: mkv-audit series DIR... | --list FILE [--workers N]"
	usageFilms      = "usage: mkv-audit films ROOT... [--min-minutes 60]"
	usageBlindspot  = "usage: mkv-audit blindspot ROOT..."
	usageSweep      = "usage: mkv-audit sweep MANIFEST ROOT... [--min-minutes 60]"
	usagePostcheck  = "usage: mkv-audit postcheck MANIFEST --before INVENTORY.json [--workers N]"
	usageGeneral    = "usage: mkv-audit series DIR... | --list FILE | films ROOT... | blindspot ROOT... | sweep MANIFEST ROOT... | postcheck MANIFEST --before INVENTORY.json | --version"
	maxListFileSize = 8 << 20
)

// Command is the mkv-audit body: args are what follows the program name. Every
// subcommand prints one JSON report on stdout for exit 0 and 1 only, and a
// refusal prints only the FATAL line.
func Command(ctx context.Context, d Deps, args []string, streams cli.IO) error {
	if len(args) == 0 {
		return usageError(usageGeneral)
	}
	switch args[0] {
	case "series":
		return seriesCommand(ctx, d, args[1:], streams)
	case "films":
		return filmsCommand(ctx, d, args[1:], streams)
	case "blindspot":
		return blindspotCommand(args[1:], streams)
	case "sweep":
		return sweepCommand(ctx, d, args[1:], streams)
	case "postcheck":
		return postcheckCommand(ctx, d, args[1:], streams)
	}
	return usageError(usageGeneral)
}

// usageError is a usage line as an exit-2 error. The line is a parameter so
// that a usage ending in "ROOT..." is not mistaken for an error message ending
// in punctuation.
func usageError(usage string) error {
	return cli.Usage(errors.New(usage))
}

// finish prints the report and turns an unclean one into exit 1.
func finish(streams cli.IO, report any, clean bool) error {
	if err := cli.PrintJSON(streams.Stdout, report); err != nil {
		return err
	}
	if !clean {
		return cli.Exit(cli.ExitIncomplete)
	}
	return nil
}

func checkWorkers(n int) error {
	if n < 1 {
		return cli.Usage(fmt.Errorf("--workers must be at least 1, got %d", n))
	}
	return nil
}

// existingDirs resolves each argument against the current directory and
// refuses one that is not a directory: a mistyped series used to read as EMPTY,
// exactly like a series with nothing in it.
func existingDirs(args []string) ([]string, error) {
	out := make([]string, len(args))
	for i, a := range args {
		abs, err := filepath.Abs(a)
		if err != nil {
			return nil, cli.Usage(fmt.Errorf("cannot resolve %s: %w", a, err))
		}
		info, err := os.Stat(abs)
		switch {
		case err != nil:
			return nil, cli.Usage(fmt.Errorf("%s: %w", a, err))
		case !info.IsDir():
			return nil, cli.Usage(fmt.Errorf("%s is not a directory", a))
		}
		out[i] = abs
	}
	return out, nil
}

// listedDirs reads --list's file: one directory per line, blank lines ignored.
func listedDirs(path string) ([]string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, cli.Usage(fmt.Errorf("cannot read the list: %w", err))
	}
	if !info.Mode().IsRegular() {
		return nil, cli.Usage(fmt.Errorf("the list %s is not a regular file", path))
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, cli.Usage(fmt.Errorf("cannot read the list: %w", err))
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxListFileSize+1))
	if err != nil {
		return nil, cli.Usage(fmt.Errorf("cannot read the list: %w", err))
	}
	if len(data) > maxListFileSize {
		return nil, cli.Usage(fmt.Errorf("the list %s is larger than %d MiB", path, maxListFileSize>>20))
	}
	var dirs []string
	for _, line := range strings.Split(string(data), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			dirs = append(dirs, line)
		}
	}
	return dirs, nil
}

type seriesReport struct {
	Series []SeriesResult `json:"series"`
	Clean  int            `json:"clean"`
	Total  int            `json:"total"`
}

func seriesCommand(ctx context.Context, d Deps, args []string, streams cli.IO) error {
	var (
		list    string
		workers = DefaultWorkers
	)
	fs := cli.NewFlagSet("mkv-audit")
	fs.StringVar(&list, "list", "", "")
	fs.IntVar(&workers, "workers", DefaultWorkers, "")
	rest, err := cli.ParseFlags(fs, args)
	if err != nil || (len(rest) == 0 && list == "") {
		return usageError(usageSeries)
	}
	if err := checkWorkers(workers); err != nil {
		return err
	}
	if list != "" {
		listed, err := listedDirs(list)
		if err != nil {
			return err
		}
		rest = append(rest, listed...)
	}
	dirs, err := existingDirs(rest)
	if err != nil {
		return err
	}

	results, err := AuditSeriesDirs(ctx, d.Runner, dirs, workers)
	if err != nil {
		return err
	}
	rep := seriesReport{Series: results, Total: len(results)}
	for _, r := range results {
		if r.Clean {
			rep.Clean++
		}
	}
	return finish(streams, rep, rep.Clean == rep.Total)
}

func filmsCommand(ctx context.Context, d Deps, args []string, streams cli.IO) error {
	minutes := DefaultMinMinutes
	fs := cli.NewFlagSet("mkv-audit")
	fs.Float64Var(&minutes, "min-minutes", DefaultMinMinutes, "")
	rest, err := cli.ParseFlags(fs, args)
	if err != nil || len(rest) == 0 {
		return usageError(usageFilms)
	}
	roots, err := existingDirs(rest)
	if err != nil {
		return err
	}
	rep, err := AuditFilms(ctx, d.Runner, roots, minutes)
	if err != nil {
		return err
	}
	return finish(streams, rep, rep.Clean)
}

func blindspotCommand(args []string, streams cli.IO) error {
	fs := cli.NewFlagSet("mkv-audit")
	rest, err := cli.ParseFlags(fs, args)
	if err != nil || len(rest) == 0 {
		return usageError(usageBlindspot)
	}
	roots, err := existingDirs(rest)
	if err != nil {
		return err
	}
	rep, err := AuditBlindspot(roots)
	if err != nil {
		return err
	}
	return finish(streams, rep, rep.Clean)
}

func sweepCommand(ctx context.Context, d Deps, args []string, streams cli.IO) error {
	minutes := DefaultMinMinutes
	fs := cli.NewFlagSet("mkv-audit")
	fs.Float64Var(&minutes, "min-minutes", DefaultMinMinutes, "")
	rest, err := cli.ParseFlags(fs, args)
	if err != nil || len(rest) < 2 {
		return usageError(usageSweep)
	}
	m, schema, err := manifest.Load(rest[0])
	if err != nil {
		return err
	}
	roots, err := existingDirs(rest[1:])
	if err != nil {
		return err
	}
	rep, err := SweepManifest(ctx, d.Runner, m, schema, roots, minutes)
	if err != nil {
		return err
	}
	return finish(streams, rep, rep.Clean)
}

func postcheckCommand(ctx context.Context, d Deps, args []string, streams cli.IO) error {
	var (
		beforePath string
		workers    = DefaultWorkers
	)
	fs := cli.NewFlagSet("mkv-audit")
	fs.StringVar(&beforePath, "before", "", "")
	fs.IntVar(&workers, "workers", DefaultWorkers, "")
	rest, err := cli.ParseFlags(fs, args)
	if err != nil || len(rest) != 1 || beforePath == "" {
		return usageError(usagePostcheck)
	}
	if err := checkWorkers(workers); err != nil {
		return err
	}
	m, schema, err := manifest.Load(rest[0])
	if err != nil {
		return err
	}
	journal, err := manifest.ReadJournal(m)
	if err != nil {
		return err
	}
	before, err := inventory.Read(beforePath)
	if err != nil {
		return err
	}
	rep, err := d.Postcheck(ctx, m, schema, journal, before, workers)
	if err != nil {
		return err
	}
	return finish(streams, rep, rep.Clean)
}
