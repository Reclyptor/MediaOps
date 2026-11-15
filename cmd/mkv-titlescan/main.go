// Command mkv-titlescan tiles one frame every few seconds of a window of a
// video into a contact sheet, to find an episode title card.
package main

import (
	"context"

	"github.com/reclyptor/mediaops/internal/cli"
	"github.com/reclyptor/mediaops/internal/sheet"
	"github.com/reclyptor/mediaops/kit/proc"
)

func main() {
	cli.Main("mkv-titlescan", func(ctx context.Context, args []string, streams cli.IO) error {
		return sheet.TitlescanCommand(ctx, &proc.Exec{}, args, streams)
	})
}
