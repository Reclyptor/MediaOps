// Command mkv-anilist prints a series' metadata and per-episode titles from
// AniList as one JSON value.
package main

import (
	"context"

	"github.com/reclyptor/mediaops/internal/anilist"
	"github.com/reclyptor/mediaops/internal/cli"
)

func main() {
	cli.Main("mkv-anilist", func(ctx context.Context, args []string, streams cli.IO) error {
		return anilist.Command(ctx, anilist.DefaultConfig(), args, streams)
	})
}
