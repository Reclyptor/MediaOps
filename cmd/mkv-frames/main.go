// Command mkv-frames renders the frame at each given time of a video into one
// contact sheet, to confirm where a cut lands.
package main

import (
	"context"

	"github.com/reclyptor/mediaops/internal/cli"
	"github.com/reclyptor/mediaops/internal/sheet"
	"github.com/reclyptor/mediaops/kit/proc"
)

func main() {
	cli.Main("mkv-frames", func(ctx context.Context, args []string, streams cli.IO) error {
		return sheet.FramesCommand(ctx, &proc.Exec{}, args, streams)
	})
}
