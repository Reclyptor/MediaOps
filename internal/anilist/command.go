package anilist

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"strconv"
	"strings"

	"github.com/reclyptor/mediaops/internal/cli"
)

const usage = "usage: mkv-anilist SEARCH | mkv-anilist --id N | --version"

// Command is the mkv-anilist body: args are what follows the program name.
// It prints the entry as one JSON value; a usage error, an unknown series
// (exit 2) and any failure to get an answer (exit 4) print only the FATAL line.
func Command(ctx context.Context, c Config, args []string, streams cli.IO) error {
	var idArg string
	fs := cli.NewFlagSet("mkv-anilist")
	fs.StringVar(&idArg, "id", "", "")
	rest, err := cli.ParseFlags(fs, args)
	if err != nil {
		return cli.Usage(errors.New(usage))
	}
	idGiven := false
	fs.Visit(func(f *flag.Flag) { idGiven = idGiven || f.Name == "id" })

	var entry *Entry
	var what string
	switch {
	case idGiven && len(rest) == 0:
		id, perr := strconv.Atoi(idArg)
		if perr != nil || id <= 0 {
			return cli.Usage(fmt.Errorf("--id must be a positive integer, got %q", idArg))
		}
		what = fmt.Sprintf("id %d", id)
		entry, err = c.ByID(ctx, id)
	case !idGiven && len(rest) == 1 && strings.TrimSpace(rest[0]) != "":
		what = fmt.Sprintf("search %q", rest[0])
		entry, err = c.Search(ctx, rest[0])
	default:
		return cli.Usage(errors.New(usage))
	}
	if errors.Is(err, ErrNotFound) {
		return cli.Usage(fmt.Errorf("AniList has no series for %s", what))
	}
	if err != nil {
		return err
	}
	return cli.PrintJSON(streams.Stdout, entry)
}
