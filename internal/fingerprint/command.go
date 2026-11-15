package fingerprint

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/reclyptor/mediaops/internal/cli"
	"github.com/reclyptor/mediaops/kit/proc"
)

const fingerprintUsage = "usage: mkv-fingerprint hash FILE PREVIEW_START|none OUT.json | batch JOBS.tsv OUTDIR [--workers N] | match OUTDIR | --version"

const dupcheckUsage = "usage: mkv-dupcheck OUTDIR TRACK | --version"

// HashSummary is what `hash` prints: the record it wrote and its frame counts.
type HashSummary struct {
	Path    string `json:"path"`
	Out     string `json:"out"`
	Body    int    `json:"body"`
	Preview int    `json:"preview"`
}

// FingerprintCommand is the mkv-fingerprint body: args are what follows the
// program name. hash and batch write records, match reads a directory of them
// and exits 1 when its findings need a look; batch exits 1 when a job failed.
func FingerprintCommand(ctx context.Context, p proc.Piper, args []string, streams cli.IO) error {
	if len(args) == 0 {
		return cli.Usage(errors.New(fingerprintUsage))
	}
	switch args[0] {
	case "hash":
		return hashCommand(ctx, p, args[1:], streams)
	case "batch":
		return batchCommand(ctx, p, args[1:], streams)
	case "match":
		return matchCommand(args[1:], streams)
	}
	return cli.Usage(errors.New(fingerprintUsage))
}

func hashCommand(ctx context.Context, p proc.Piper, args []string, streams cli.IO) error {
	rest, err := cli.ParseFlags(cli.NewFlagSet("mkv-fingerprint hash"), args)
	if err != nil || len(rest) != 3 {
		return cli.Usage(errors.New(fingerprintUsage))
	}
	file, err := filepath.Abs(rest[0])
	if err != nil {
		return cli.Usage(fmt.Errorf("bad FILE %q: %w", rest[0], err))
	}
	start, err := ParsePreviewStart(rest[1])
	if err != nil {
		return cli.Usage(err)
	}
	out, err := filepath.Abs(rest[2])
	if err != nil {
		return cli.Usage(fmt.Errorf("bad OUT %q: %w", rest[2], err))
	}
	info, err := os.Stat(file)
	switch {
	case err != nil:
		return cli.Usage(fmt.Errorf("cannot read %s: %w", file, err))
	case !info.Mode().IsRegular():
		return cli.Usage(fmt.Errorf("%s is not a regular file", file))
	}
	if err := CheckOutput(out); err != nil {
		return cli.Usage(err)
	}

	rec, err := Compute(ctx, p, file, start)
	if err != nil {
		return err
	}
	if err := Write(out, rec); err != nil {
		return err
	}
	return cli.PrintJSON(streams.Stdout, HashSummary{Path: file, Out: out, Body: len(rec.Body), Preview: len(rec.Preview)})
}

func batchCommand(ctx context.Context, p proc.Piper, args []string, streams cli.IO) error {
	workers := DefaultWorkers
	fs := cli.NewFlagSet("mkv-fingerprint batch")
	fs.IntVar(&workers, "workers", DefaultWorkers, "")
	rest, err := cli.ParseFlags(fs, args)
	if err != nil || len(rest) != 2 {
		return cli.Usage(errors.New(fingerprintUsage))
	}
	if workers < 1 {
		return cli.Usage(fmt.Errorf("--workers must be at least 1, got %d", workers))
	}
	jobs, err := ParseJobs(rest[0])
	if err != nil {
		return err
	}
	outDir, err := PrepareOutDir(rest[1])
	if err != nil {
		return err
	}

	rep, err := RunBatch(ctx, p, jobs, outDir, workers, streams.Stderr)
	if err != nil {
		return err
	}
	if err := cli.PrintJSON(streams.Stdout, rep); err != nil {
		return err
	}
	if len(rep.Failed) > 0 {
		return cli.Exit(cli.ExitIncomplete)
	}
	return nil
}

func matchCommand(args []string, streams cli.IO) error {
	rest, err := cli.ParseFlags(cli.NewFlagSet("mkv-fingerprint match"), args)
	if err != nil || len(rest) != 1 {
		return cli.Usage(errors.New(fingerprintUsage))
	}
	dir, err := filepath.Abs(rest[0])
	if err != nil {
		return cli.Usage(fmt.Errorf("bad OUTDIR %q: %w", rest[0], err))
	}
	tracks, err := LoadDir(dir)
	if err != nil {
		return err
	}
	if len(tracks) < 2 {
		return cli.Usage(fmt.Errorf("%s holds %d fingerprint record(s); match needs at least 2", dir, len(tracks)))
	}
	rep := Analyze(dir, tracks)
	if err := cli.PrintJSON(streams.Stdout, rep); err != nil {
		return err
	}
	if !rep.Clean {
		return cli.Exit(cli.ExitIncomplete)
	}
	return nil
}

// DupcheckCommand is the mkv-dupcheck body: args are what follows the program
// name. It prints how much of TRACK's body every other track shares.
func DupcheckCommand(_ context.Context, _ proc.Piper, args []string, streams cli.IO) error {
	rest, err := cli.ParseFlags(cli.NewFlagSet("mkv-dupcheck"), args)
	if err != nil || len(rest) != 2 {
		return cli.Usage(errors.New(dupcheckUsage))
	}
	dir, err := filepath.Abs(rest[0])
	if err != nil {
		return cli.Usage(fmt.Errorf("bad OUTDIR %q: %w", rest[0], err))
	}
	tracks, err := LoadDir(dir)
	if err != nil {
		return err
	}
	rep, err := Dupcheck(dir, tracks, rest[1])
	if err != nil {
		return err
	}
	return cli.PrintJSON(streams.Stdout, rep)
}
