// Command mkv-drives prints every optical drive as one JSON array.
package main

import (
	"context"

	"github.com/reclyptor/mediaops/internal/cli"
	"github.com/reclyptor/mediaops/internal/drives"
	"github.com/reclyptor/mediaops/kit/proc"
)

func main() {
	cli.Main("mkv-drives", func(ctx context.Context, args []string, streams cli.IO) error {
		return drives.Command(ctx, drives.NewHost(&proc.Exec{}), args, streams)
	})
}
