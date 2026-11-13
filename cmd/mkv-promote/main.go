// Command mkv-promote moves verified files into the library with full
// accounting. It is a dry run unless given --go.
package main

import (
	"context"

	"github.com/reclyptor/mediaops/internal/cli"
	"github.com/reclyptor/mediaops/internal/library"
)

func main() {
	cli.Main("mkv-promote", func(ctx context.Context, args []string, streams cli.IO) error {
		return library.PromoteCommand(ctx, library.DefaultDeps(), args, streams)
	})
}
