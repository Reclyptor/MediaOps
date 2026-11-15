// Command mkv-fingerprint proves which episode follows which: each episode's
// closing preview is cut from the next one, so the track whose frames hold
// the preview is its successor.
package main

import (
	"context"

	"github.com/reclyptor/mediaops/internal/cli"
	"github.com/reclyptor/mediaops/internal/fingerprint"
	"github.com/reclyptor/mediaops/kit/proc"
)

func main() {
	cli.Main("mkv-fingerprint", func(ctx context.Context, args []string, streams cli.IO) error {
		return fingerprint.FingerprintCommand(ctx, &proc.Exec{}, args, streams)
	})
}
