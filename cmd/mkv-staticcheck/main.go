// Command mkv-staticcheck separates real extras from disc furniture: a menu
// card is a still, so every frame sampled from it hashes the same.
package main

import (
	"context"

	"github.com/reclyptor/mediaops/internal/cli"
	"github.com/reclyptor/mediaops/internal/staticcheck"
	"github.com/reclyptor/mediaops/kit/proc"
)

func main() {
	cli.Main("mkv-staticcheck", func(ctx context.Context, args []string, streams cli.IO) error {
		return staticcheck.Command(ctx, &proc.Exec{}, args, streams)
	})
}
