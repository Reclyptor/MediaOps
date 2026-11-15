// Command mkv-dupcheck tells an alternate cut of an episode from a different
// episode, by how much of its body the other tracks share.
package main

import (
	"context"

	"github.com/reclyptor/mediaops/internal/cli"
	"github.com/reclyptor/mediaops/internal/fingerprint"
	"github.com/reclyptor/mediaops/kit/proc"
)

func main() {
	cli.Main("mkv-dupcheck", func(ctx context.Context, args []string, streams cli.IO) error {
		return fingerprint.DupcheckCommand(ctx, &proc.Exec{}, args, streams)
	})
}
