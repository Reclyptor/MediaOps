// Command mkv-cards locates held title cards in MakeMKV rips by colour
// signature or by freeze detection.
package main

import (
	"context"

	"github.com/reclyptor/mediaops/internal/cards"
	"github.com/reclyptor/mediaops/internal/cli"
	"github.com/reclyptor/mediaops/kit/proc"
)

func main() {
	cli.Main("mkv-cards", func(ctx context.Context, args []string, streams cli.IO) error {
		return cards.Command(ctx, &proc.Exec{}, args, streams)
	})
}
