// Package sheet is mkv-frames and mkv-titlescan: contact sheets of decoded
// frames, drawn and PNG-encoded in Go.
package sheet

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"image"

	"github.com/reclyptor/mediaops/internal/cli"
	"github.com/reclyptor/mediaops/kit/frames"
)

// FramesUsage is mkv-frames' usage text. The grammar it ends on is why the
// tool exists: it is the only way to confirm a cut lands in the right place.
const FramesUsage = `usage: mkv-frames FILE OUT.png T... [--width 640] [--cols N] | --version

Renders the frame at each T into one contact sheet. T is seconds (2600) or
[HH:]MM:SS (43:20). A T past the end of the file is drawn as a NO FRAME
placeholder and the exit is 1.

This is the only tool here that can confirm a cut lands in the right place.
The episode grammar to read for:
  ED credits -> (post-ED bumper) -> next-episode preview -> CUT -> cold open
  -> OP -> the episode's own title card
The next-episode preview (the 次回予告) belongs to the OUTGOING episode.
Anchoring on its title card puts every cut one chapter late`

// FramesCommand is the mkv-frames body: args are what follows the program
// name. It seeks to each T with -ss before -i, as a keyframe seek is orders of
// magnitude faster than decoding through and exactness is not needed to
// recognise a scene.
func FramesCommand(ctx context.Context, ex Exec, args []string, streams cli.IO) error {
	fs := cli.NewFlagSet("mkv-frames")
	width := fs.Int("width", 640, "")
	cols := fs.Int("cols", 0, "")
	rest, err := cli.ParseFlags(fs, args)
	if err != nil || len(rest) < 3 {
		return cli.Usage(errors.New(FramesUsage))
	}
	colsSet := false
	fs.Visit(func(f *flag.Flag) { colsSet = colsSet || f.Name == "cols" })

	spots := make([]spot, 0, len(rest)-2)
	for _, arg := range rest[2:] {
		at, err := ParseTime(arg)
		if err != nil {
			return cli.Usage(err)
		}
		spots = append(spots, spot{at: at, label: secondsLabel(at)})
	}
	if !colsSet {
		*cols = min(3, len(spots))
	}
	file, out, err := checkPaths(rest[0], rest[1])
	if err != nil {
		return err
	}
	if err := checkTileCount(*width, *cols, len(spots)); err != nil {
		return err
	}
	p := &plan{file: file, out: out, width: *width, cols: *cols, spots: spots}
	box, err := p.fit(ctx, ex)
	if err != nil {
		return err
	}

	imgs := make([]*image.RGBA, len(spots))
	for i, s := range spots {
		img, err := frames.RGBFrame(ctx, ex, file, s.at, frames.TileOptions{Box: box})
		switch {
		case errors.Is(err, frames.ErrNoFrame):
		case err != nil:
			return fmt.Errorf("decoding tile %d at %s: %w", i+1, s.label, err)
		default:
			imgs[i] = img
		}
	}
	rep, err := p.compose(box, imgs)
	if err != nil {
		return err
	}
	return report(streams, rep, rep.Errors)
}
