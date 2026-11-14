package split

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"math"
	"path/filepath"
	"strings"

	"github.com/reclyptor/mediaops/internal/cli"
)

const (
	usageSplit = "usage: mkv-split SRC OUTDIR/PREFIX CH,CH,... [--go] [--tol S] [--allow-open-gop] | " +
		"mkv-split SRC OUTDIR/PREFIX --at T,T,... [--go] [--tol S] [--allow-open-gop]"
	usageVerify  = "usage: mkv-split verify SRC PART PART... (the parts in order)"
	usageGeneral = usageSplit + " | mkv-split verify SRC PART PART... | --version"
)

// Command is the mkv-split body: args are what follows the program name. It
// prints one JSON report on stdout for exit 0 and 1 only.
func Command(ctx context.Context, ex Tools, args []string, streams cli.IO) error {
	if len(args) == 0 {
		return cli.Usage(errors.New(usageGeneral))
	}
	if args[0] == "verify" {
		return verifyCommand(ctx, ex, args[1:], streams)
	}
	return splitCommand(ctx, ex, args, streams)
}

func splitCommand(ctx context.Context, ex Tools, args []string, streams cli.IO) error {
	var (
		at       string
		tol      = DefaultTol
		allowGOP bool
		doGo     bool
	)
	fs := cli.NewFlagSet("mkv-split")
	fs.StringVar(&at, "at", "", "")
	fs.Float64Var(&tol, "tol", DefaultTol, "")
	fs.BoolVar(&allowGOP, "allow-open-gop", false, "")
	fs.BoolVar(&doGo, "go", false, "")
	rest, err := cli.ParseFlags(fs, args)
	if err != nil {
		return cli.Usage(errors.New(usageSplit))
	}
	atMode := false
	fs.Visit(func(f *flag.Flag) { atMode = atMode || f.Name == "at" })
	want := 3
	if atMode {
		want = 2
	}
	if len(rest) != want {
		return cli.Usage(errors.New(usageSplit))
	}
	if math.IsNaN(tol) || math.IsInf(tol, 0) || tol <= 0 {
		return cli.Usage(fmt.Errorf("--tol must be a positive number of seconds, got %v", tol))
	}

	src, err := filepath.Abs(rest[0])
	if err != nil {
		return cli.Usage(err)
	}
	target := rest[1]
	if target == "" || target[len(target)-1] == filepath.Separator {
		return cli.Usage(fmt.Errorf("%q does not name a file prefix; give OUTDIR/PREFIX", target))
	}
	target, err = filepath.Abs(target)
	if err != nil {
		return cli.Usage(err)
	}
	req := Request{
		Src: src, OutDir: filepath.Dir(target), Prefix: filepath.Base(target),
		Tol: tol, AllowOpenGOP: allowGOP, Go: doGo,
	}

	// The lists are checked against the source's chapters and duration, so
	// Run parses them once it has read the source.
	if !atMode && strings.TrimSpace(rest[2]) == "" {
		return cli.Usage(errors.New(usageSplit))
	}
	if atMode {
		req.TimeList = at
	} else {
		req.ChapterList = rest[2]
	}

	rep, err := Run(ctx, ex, req)
	if err != nil {
		return err
	}
	if err := cli.PrintJSON(streams.Stdout, rep); err != nil {
		return err
	}
	if !rep.Clean {
		return cli.Exit(cli.ExitIncomplete)
	}
	return nil
}

func verifyCommand(ctx context.Context, ex Tools, args []string, streams cli.IO) error {
	rest, err := cli.ParseFlags(cli.NewFlagSet("mkv-split"), args)
	if err != nil || len(rest) < 2 {
		return cli.Usage(errors.New(usageVerify))
	}
	paths := make([]string, len(rest))
	for i, p := range rest {
		if paths[i], err = filepath.Abs(p); err != nil {
			return cli.Usage(err)
		}
	}
	if _, err := statRegular(paths[0]); err != nil {
		return cli.Usage(fmt.Errorf("source: %w", err))
	}
	rep, err := Verify(ctx, ex, paths[0], paths[1:], nil)
	if err != nil {
		return err
	}
	if err := cli.PrintJSON(streams.Stdout, rep); err != nil {
		return err
	}
	if !rep.Clean {
		return cli.Exit(cli.ExitIncomplete)
	}
	return nil
}
