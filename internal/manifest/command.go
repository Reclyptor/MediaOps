package manifest

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/reclyptor/mediaops/internal/cli"
)

const (
	usageCheck   = "usage: mkv-manifest check MANIFEST [--films]"
	usageExec    = "usage: mkv-manifest exec MANIFEST [--go] [--films]"
	usageSeries  = "usage: mkv-manifest gen rename-series DIR NAME [--only N,N...] [--strip-date]"
	usageTree    = "usage: mkv-manifest gen archive-tree SRC DST"
	usageGen     = "usage: mkv-manifest gen rename-series DIR NAME [--only N,N...] [--strip-date] | gen archive-tree SRC DST"
	usageGeneral = "usage: mkv-manifest check MANIFEST [--films] | exec MANIFEST [--go] [--films] | gen rename-series|archive-tree ... | --version"
)

// Command is the mkv-manifest body: args are what follows the program name.
// check and exec print one JSON report on stdout for exit 0 and 1 only, and a
// refusal prints only the FATAL line. gen prints the manifest it generated.
func Command(ctx context.Context, c Config, args []string, streams cli.IO) error {
	if len(args) == 0 {
		return cli.Usage(errors.New(usageGeneral))
	}
	switch args[0] {
	case "check":
		return checkCommand(c, args[1:], streams)
	case "exec":
		return execCommand(ctx, c, args[1:], streams)
	case "gen":
		return genCommand(c, args[1:], streams)
	}
	return cli.Usage(errors.New(usageGeneral))
}

// loadManifest parses MANIFEST and the flags around it.
func loadManifest(args []string, usage string, bind func(fs *flag.FlagSet)) (*Manifest, []Finding, error) {
	fs := cli.NewFlagSet("mkv-manifest")
	bind(fs)
	rest, err := cli.ParseFlags(fs, args)
	if err != nil || len(rest) != 1 {
		return nil, nil, cli.Usage(errors.New(usage))
	}
	return Load(rest[0])
}

func checkCommand(c Config, args []string, streams cli.IO) error {
	var films bool
	m, schema, err := loadManifest(args, usageCheck, func(fs *flag.FlagSet) { fs.BoolVar(&films, "films", false, "") })
	if err != nil {
		return err
	}
	j, err := ReadJournal(m)
	if err != nil {
		return err
	}
	rep, err := c.Check(m, schema, j, CheckOptions{Films: films})
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

func execCommand(ctx context.Context, c Config, args []string, streams cli.IO) error {
	var opts ExecOptions
	m, schema, err := loadManifest(args, usageExec, func(fs *flag.FlagSet) {
		fs.BoolVar(&opts.Go, "go", false, "")
		fs.BoolVar(&opts.Films, "films", false, "")
	})
	if err != nil {
		return err
	}
	rep, err := c.Exec(ctx, m, schema, opts)
	if code := cli.Code(err); code != cli.ExitOK && code != cli.ExitIncomplete {
		return err
	}
	if perr := cli.PrintJSON(streams.Stdout, rep); perr != nil && err == nil {
		return perr
	}
	return err
}

func genCommand(c Config, args []string, streams cli.IO) error {
	if len(args) == 0 {
		return cli.Usage(errors.New(usageGen))
	}
	var (
		gen Generated
		err error
	)
	switch args[0] {
	case "rename-series":
		gen, err = genRenameSeries(c, args[1:])
	case "archive-tree":
		gen, err = genArchiveTree(c, args[1:])
	default:
		return cli.Usage(errors.New(usageGen))
	}
	if err != nil {
		return err
	}
	return cli.PrintJSON(streams.Stdout, gen)
}

func genRenameSeries(c Config, args []string) (Generated, error) {
	var (
		only      string
		stripDate bool
	)
	fs := cli.NewFlagSet("mkv-manifest")
	fs.StringVar(&only, "only", "", "")
	fs.BoolVar(&stripDate, "strip-date", false, "")
	rest, err := cli.ParseFlags(fs, args)
	if err != nil || len(rest) != 2 {
		return Generated{}, cli.Usage(errors.New(usageSeries))
	}
	seasons, err := parseSeasons(only)
	if err != nil {
		return Generated{}, err
	}
	dir, err := filepath.Abs(rest[0])
	if err != nil {
		return Generated{}, cli.Usage(fmt.Errorf("cannot resolve %s: %w", rest[0], err))
	}
	return GenRenameSeries(dir, rest[1], seasons, stripDate, c.now())
}

func genArchiveTree(c Config, args []string) (Generated, error) {
	fs := cli.NewFlagSet("mkv-manifest")
	rest, err := cli.ParseFlags(fs, args)
	if err != nil || len(rest) != 2 {
		return Generated{}, cli.Usage(errors.New(usageTree))
	}
	src, err := filepath.Abs(rest[0])
	if err != nil {
		return Generated{}, cli.Usage(fmt.Errorf("cannot resolve %s: %w", rest[0], err))
	}
	dst, err := filepath.Abs(rest[1])
	if err != nil {
		return Generated{}, cli.Usage(fmt.Errorf("cannot resolve %s: %w", rest[1], err))
	}
	return GenArchiveTree(src, dst, c.now())
}

// parseSeasons reads --only's comma-separated season numbers.
func parseSeasons(only string) ([]int, error) {
	if only == "" {
		return nil, nil
	}
	var out []int
	for _, field := range strings.Split(only, ",") {
		n, err := strconv.Atoi(strings.TrimSpace(field))
		if err != nil || n < 0 {
			return nil, cli.Usage(fmt.Errorf("--only wants season numbers like 1,2, not %q", only))
		}
		out = append(out, n)
	}
	return out, nil
}
