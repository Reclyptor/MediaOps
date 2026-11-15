package sheet

import (
	"context"
	"errors"
	"fmt"
	"image"
	"math"

	"github.com/reclyptor/mediaops/internal/cli"
	"github.com/reclyptor/mediaops/kit/frames"
)

// TitlescanUsage is mkv-titlescan's usage text. Why the band defaults to the
// whole frame is the reason this tool is trustworthy, so it stays in it.
const TitlescanUsage = `usage: mkv-titlescan FILE OUT.png START END [--step 3] [--band full|H|H:Y] [--width 340] | --version

Hunts for an episode title card: one frame every STEP seconds from START to
END, tiled into one sheet. START and END are seconds or [HH:]MM:SS.

  --band  full   the whole frame (DEFAULT)
          H      the top H of the frame, e.g. 0.55
          H:Y    a band H high starting Y down, e.g. 0.30:0.70 is the bottom
                 30% and 0.40:0.30 a middle band; 0 < H <= 1, H+Y <= 1

A title card is often a text overlay on an ordinary shot rather than a
full-screen card, it is on screen for only ~4-5 seconds, and it does NOT sit
at a fixed offset -- the OP moves with the length of the cold open. So sample
densely and, until you know where a series puts its card, look at the WHOLE
frame.

The default was once a hardcoded bottom third. That produced a false negative
on a series whose card renders upper-right, and nearly did on another whose
card sat just above the crop line -- both were then reported as "this show
has no title card", which is the worst possible failure for an identity
check. A narrow band is an optimisation to apply AFTER you have seen where the
card is, never a default.

ALWAYS run this against an episode whose card you have already seen before
trusting a negative result. An instrument that silently finds nothing is
indistinguishable from one pointed the wrong way.

A window running past the end of the file leaves NO FRAME placeholders and
the exit is 1`

// TitlescanReport is Report plus the scan's own parameters.
type TitlescanReport struct {
	Report
	Start float64 `json:"start"`
	End   float64 `json:"end"`
	Step  float64 `json:"step"`
	Band  string  `json:"band"`
}

// TitlescanCommand is the mkv-titlescan body: args are what follows the
// program name. It reads every frame in one decode, fps=1/STEP over the
// window, and keeps the first N.
func TitlescanCommand(ctx context.Context, ex Exec, args []string, streams cli.IO) error {
	fs := cli.NewFlagSet("mkv-titlescan")
	step := fs.Float64("step", 3, "")
	bandText := fs.String("band", "full", "")
	width := fs.Int("width", 340, "")
	rest, err := cli.ParseFlags(fs, args)
	if err != nil || len(rest) != 4 {
		return cli.Usage(errors.New(TitlescanUsage))
	}
	start, err := ParseTime(rest[2])
	if err != nil {
		return cli.Usage(fmt.Errorf("START: %w", err))
	}
	end, err := ParseTime(rest[3])
	if err != nil {
		return cli.Usage(fmt.Errorf("END: %w", err))
	}
	band, err := frames.ParseBand(*bandText)
	if err != nil {
		return cli.Usage(err)
	}
	n, err := tileCount(start, end, *step)
	if err != nil {
		return cli.Usage(err)
	}
	cols := 1
	for cols*cols < n {
		cols++
	}
	file, out, err := checkPaths(rest[0], rest[1])
	if err != nil {
		return err
	}
	if err := checkTileCount(*width, cols, n); err != nil {
		return err
	}

	spots := make([]spot, n)
	for i := range spots {
		at := roundMicro(start + float64(i)*(*step))
		spots[i] = spot{at: at, label: secondsLabel(at)}
	}
	p := &plan{file: file, out: out, width: *width, cols: cols, band: band, spots: spots}
	box, err := p.fit(ctx, ex)
	if err != nil {
		return err
	}

	window := end - start
	opts := frames.TileOptions{
		Box: box, Band: band,
		SeekFrom: &start, Duration: &window,
		FPS: "1/" + fmtFloat(*step),
	}
	imgs := make([]*image.RGBA, n)
	_, err = frames.RGBFrames(ctx, ex, file, opts, func(i int, img *image.RGBA) error {
		imgs[i] = img
		if i+1 >= n {
			return errEnough
		}
		return nil
	})
	if err != nil && !errors.Is(err, errEnough) {
		return fmt.Errorf("scanning %s from %ss to %ss: %w", file, fmtFloat(start), fmtFloat(end), err)
	}

	rep, err := p.compose(box, imgs)
	if err != nil {
		return err
	}
	return report(streams, TitlescanReport{Report: rep, Start: start, End: end, Step: *step, Band: band.String()}, rep.Errors)
}

// roundMicro drops the float noise of start + i*step, so 1.1 + 2*0.1 is
// labelled 1.3s and not 1.3000000000000003s.
func roundMicro(v float64) float64 { return math.Round(v*1e6) / 1e6 }
