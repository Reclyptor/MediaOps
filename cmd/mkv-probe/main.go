// Command mkv-probe inventories a tree of video files as one JSON document:
// duration, chapters and stream layout, the raw material every later decision
// in the library pipeline is made from.
package main

import (
	"context"

	"github.com/reclyptor/mediaops/internal/cli"
	"github.com/reclyptor/mediaops/internal/inventory"
)

func main() {
	cli.Main("mkv-probe", func(ctx context.Context, args []string, streams cli.IO) error {
		return inventory.ProbeCommand(ctx, inventory.DefaultDeps(), args, streams)
	})
}
