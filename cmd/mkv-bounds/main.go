// Command mkv-bounds finds the episode boundaries in a MakeMKV Play-All track
// from its chapter marks and the series' published episode runtime.
package main

import (
	"context"

	"github.com/reclyptor/mediaops/internal/bounds"
	"github.com/reclyptor/mediaops/internal/cli"
	"github.com/reclyptor/mediaops/kit/proc"
)

func main() {
	cli.Main("mkv-bounds", func(ctx context.Context, args []string, streams cli.IO) error {
		return bounds.Command(ctx, &proc.Exec{}, args, streams)
	})
}
