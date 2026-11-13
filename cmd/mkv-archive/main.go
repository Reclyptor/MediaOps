// Command mkv-archive moves superseded library files into the archive tree,
// layout preserved. It is a dry run unless given --go.
package main

import (
	"context"

	"github.com/reclyptor/mediaops/internal/cli"
	"github.com/reclyptor/mediaops/internal/library"
)

func main() {
	cli.Main("mkv-archive", func(ctx context.Context, args []string, streams cli.IO) error {
		return library.ArchiveCommand(ctx, library.DefaultDeps(), args, streams)
	})
}
