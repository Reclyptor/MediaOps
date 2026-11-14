// Command mkv-manifest checks and applies op manifests against the media
// library, and generates them for common jobs. exec is a dry run unless given
// --go, and runs the check first either way.
package main

import (
	"context"

	"github.com/reclyptor/mediaops/internal/cli"
	"github.com/reclyptor/mediaops/internal/manifest"
)

func main() {
	cli.Main("mkv-manifest", func(ctx context.Context, args []string, streams cli.IO) error {
		return manifest.Command(ctx, manifest.DefaultConfig(), args, streams)
	})
}
