// Command mkv-rip runs staggered rips, identify scans and finalizes of the
// optical drives, each detached from the command that started it.
package main

import (
	"context"

	"github.com/reclyptor/mediaops/internal/cli"
	"github.com/reclyptor/mediaops/internal/rip"
)

func main() {
	cli.Main("mkv-rip", func(ctx context.Context, args []string, streams cli.IO) error {
		return rip.Command(ctx, rip.DefaultDeps(), args, streams)
	})
}
