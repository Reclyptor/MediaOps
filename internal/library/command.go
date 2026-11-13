package library

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/reclyptor/mediaops/internal/cli"
)

// ArchiveCommand is the mkv-archive body: args are what follows the program
// name. It prints one JSON report, a dry run unless --go is given.
func ArchiveCommand(ctx context.Context, d Deps, args []string, streams cli.IO) error {
	const usage = "usage: mkv-archive MANIFEST.json [--go] | --version"
	data, run, err := readManifest(args, usage)
	if err != nil {
		return err
	}
	entries, err := ParseArchiveManifest(data)
	if err != nil {
		return cli.Usage(fmt.Errorf("bad manifest %s: %w", args[0], err))
	}
	rep, err := d.Archive(ctx, entries, run)
	return finish(streams, rep, rep.Error, err)
}

// PromoteCommand is the mkv-promote body: args are what follows the program
// name. It prints one JSON report, a dry run unless --go is given.
func PromoteCommand(ctx context.Context, d Deps, args []string, streams cli.IO) error {
	const usage = "usage: mkv-promote MANIFEST.json [--go] | --version"
	data, run, err := readManifest(args, usage)
	if err != nil {
		return err
	}
	entries, err := ParsePromoteManifest(data)
	if err != nil {
		return cli.Usage(fmt.Errorf("bad manifest %s: %w", args[0], err))
	}
	rep, err := d.Promote(ctx, entries, run)
	return finish(streams, rep, rep.Error, err)
}

// readManifest parses MANIFEST.json [--go], in either order.
func readManifest(args []string, usage string) (data []byte, run bool, err error) {
	fs := cli.NewFlagSet("manifest")
	fs.BoolVar(&run, "go", false, "")
	rest, err := cli.ParseFlags(fs, args)
	if err != nil || len(rest) != 1 {
		return nil, false, cli.Usage(errors.New(usage))
	}
	data, err = os.ReadFile(rest[0])
	if err != nil {
		return nil, false, cli.Usage(fmt.Errorf("cannot read the manifest %s: %w", rest[0], err))
	}
	return data, run, nil
}

// finish prints the report when the command either succeeded or got as far as
// changing something. A refusal before anything moved prints nothing on
// stdout, only the FATAL line.
func finish(streams cli.IO, report any, midRunError string, err error) error {
	if err != nil && midRunError == "" {
		return err
	}
	if perr := cli.PrintJSON(streams.Stdout, report); perr != nil && err == nil {
		return perr
	}
	return err
}
