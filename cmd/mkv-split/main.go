// Command mkv-split cuts a Play-All track into episodes at chapter numbers or
// timestamps, refusing any cut that is not on a keyframe, and proves the
// result. mkv-split verify proves a split made by anything else.
package main

import (
	"context"

	"github.com/reclyptor/mediaops/internal/cli"
	"github.com/reclyptor/mediaops/internal/split"
	"github.com/reclyptor/mediaops/kit/proc"
)

func main() {
	cli.Main("mkv-split", func(ctx context.Context, args []string, streams cli.IO) error {
		return split.Command(ctx, &proc.Exec{}, args, streams)
	})
}
