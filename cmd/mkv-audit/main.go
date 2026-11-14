// Command mkv-audit checks the library against the anime-normalize rules and a
// finished manifest run against its manifest. It only reads.
package main

import (
	"context"

	"github.com/reclyptor/mediaops/internal/audit"
	"github.com/reclyptor/mediaops/internal/cli"
)

func main() {
	cli.Main("mkv-audit", func(ctx context.Context, args []string, streams cli.IO) error {
		return audit.Command(ctx, audit.DefaultDeps(), args, streams)
	})
}
