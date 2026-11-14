package inventory

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/reclyptor/mediaops/internal/cli"
	"github.com/reclyptor/mediaops/kit/paths"
	"github.com/reclyptor/mediaops/kit/proc"
)

// Deps is what mkv-probe touches outside itself.
type Deps struct {
	Paths  paths.Config
	Runner proc.Runner
}

// DefaultDeps is the real machine: MEDIA_ROOT from the environment and the real
// ffprobe.
func DefaultDeps() Deps {
	return Deps{Paths: paths.FromEnv(), Runner: &proc.Exec{}}
}

// ProbeCommand is the mkv-probe body: args are what follows the program name.
// It prints the inventory and exits 1 when any file failed to probe.
func ProbeCommand(ctx context.Context, d Deps, args []string, streams cli.IO) error {
	const usage = "usage: mkv-probe PATH... [--workers N] | --version"
	workers := DefaultWorkers
	fs := cli.NewFlagSet("mkv-probe")
	fs.IntVar(&workers, "workers", DefaultWorkers, "")
	rest, err := cli.ParseFlags(fs, args)
	if err != nil || len(rest) == 0 {
		return cli.Usage(errors.New(usage))
	}
	if workers < 1 {
		return cli.Usage(fmt.Errorf("--workers must be at least 1, got %d", workers))
	}

	roots := make([]string, len(rest))
	for i, p := range rest {
		abs, err := filepath.Abs(p)
		if err != nil {
			return cli.Usage(fmt.Errorf("cannot resolve %s: %w", p, err))
		}
		if _, err := os.Stat(abs); err != nil {
			return cli.Usage(fmt.Errorf("cannot read %s: %w", p, err))
		}
		roots[i] = abs
	}

	inv, err := Probe(ctx, d.Runner, d.Paths, roots, workers)
	if err != nil {
		return err
	}
	if err := cli.PrintJSON(streams.Stdout, inv); err != nil {
		return err
	}
	if inv.Errors > 0 {
		return cli.Exit(cli.ExitIncomplete)
	}
	return nil
}
