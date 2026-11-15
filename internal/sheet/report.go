package sheet

import (
	"context"
	"errors"
	"fmt"
	"image"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/reclyptor/mediaops/internal/cli"
	"github.com/reclyptor/mediaops/kit/frames"
	"github.com/reclyptor/mediaops/kit/proc"
)

// Exec is what the sheet commands run children with: ffprobe through Runner
// and ffmpeg's frame stream through Piper. *proc.Exec is both.
type Exec interface {
	proc.Runner
	proc.Piper
}

// Report is the JSON a sheet command prints. Width and Height are the size of
// one tile, not of the sheet.
type Report struct {
	File   string       `json:"file"`
	Out    string       `json:"out"`
	Width  int          `json:"width"`
	Height int          `json:"height"`
	Cols   int          `json:"cols"`
	Rows   int          `json:"rows"`
	Tiles  []TileReport `json:"tiles"`
	Errors int          `json:"errors"`
}

// TileReport is one tile. Error is set when it is a placeholder.
type TileReport struct {
	N     int     `json:"n"`
	At    float64 `json:"at"`
	Label string  `json:"label"`
	Error string  `json:"error,omitempty"`
}

const noFrame = "no frame"

// spot is one tile to draw: the second it shows and its label.
type spot struct {
	at    float64
	label string
}

func secondsLabel(at float64) string { return fmtFloat(at) + "s" }

func fmtFloat(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }

// checkPaths is the usage-level validation shared by both commands: FILE is a
// regular file and OUT is a .png that is new or a regular file, in a
// directory that exists. It returns both as absolute paths.
func checkPaths(file, out string) (string, string, error) {
	info, err := os.Stat(file)
	switch {
	case err != nil:
		return "", "", cli.Usage(fmt.Errorf("cannot read %s: %w", file, err))
	case !info.Mode().IsRegular():
		return "", "", cli.Usage(fmt.Errorf("%s is not a regular file", file))
	}
	if !strings.EqualFold(filepath.Ext(out), ".png") {
		return "", "", cli.Usage(fmt.Errorf("%s does not end in .png", out))
	}
	if fi, err := os.Lstat(out); err == nil {
		if !fi.Mode().IsRegular() {
			return "", "", cli.Usage(fmt.Errorf("%s exists and is not a regular file", out))
		}
	} else if !os.IsNotExist(err) {
		return "", "", cli.Usage(fmt.Errorf("checking %s: %w", out, err))
	}
	if fi, err := os.Stat(filepath.Dir(out)); err != nil || !fi.IsDir() {
		return "", "", cli.Usage(fmt.Errorf("the directory of %s does not exist", out))
	}
	absFile, err := filepath.Abs(file)
	if err != nil {
		return "", "", cli.Usage(err)
	}
	absOut, err := filepath.Abs(out)
	if err != nil {
		return "", "", cli.Usage(err)
	}
	return absFile, absOut, nil
}

// checkTileCount refuses a sheet of n tiles in cols columns at the tile count
// and column limits before any child runs. Only the real box, known after
// ffprobe, can judge the pixel count.
func checkTileCount(width, cols, n int) error {
	if err := frames.CheckSheet(frames.Box{W: width, H: 2}, cols, n); err != nil {
		return cli.Usage(err)
	}
	return nil
}

// plan is a validated sheet: the files, the tiles and how they are fitted.
type plan struct {
	file, out string
	width     int
	cols      int
	band      frames.Band
	spots     []spot
}

// fit sizes the tile box from the file's video stream and checks the whole
// sheet against it. Nothing has been decoded when it fails.
func (p *plan) fit(ctx context.Context, ex Exec) (frames.Box, error) {
	box, err := frames.FitBox(ctx, ex, p.file, p.width, p.band)
	if err != nil {
		return frames.Box{}, err
	}
	if err := frames.CheckSheet(box, p.cols, len(p.spots)); err != nil {
		return frames.Box{}, cli.Usage(err)
	}
	return box, nil
}

// compose lays the decoded frames (nil for a tile with none, drawn as a
// placeholder) out as the sheet and writes it to OUT.
func (p *plan) compose(box frames.Box, imgs []*image.RGBA) (Report, error) {
	cols := min(p.cols, len(p.spots))
	rep := Report{
		File: p.file, Out: p.out, Width: box.W, Height: box.H,
		Cols: cols, Rows: (len(p.spots) + cols - 1) / cols,
		Tiles: make([]TileReport, len(p.spots)),
	}
	tiles := make([]frames.Tile, len(p.spots))
	for i, s := range p.spots {
		rep.Tiles[i] = TileReport{N: i + 1, At: s.at, Label: s.label}
		tiles[i] = frames.Tile{Img: imgs[i], Label: s.label}
		if imgs[i] == nil {
			rep.Tiles[i].Error = noFrame
			tiles[i].Label = s.label + " NO FRAME"
			rep.Errors++
		}
	}
	sheet, err := frames.Sheet(box, p.cols, tiles)
	if err != nil {
		return Report{}, err
	}
	if err := frames.WritePNG(p.out, sheet); err != nil {
		return Report{}, err
	}
	return rep, nil
}

// report prints v and ends 1 when errors is above zero.
func report(streams cli.IO, v any, errors int) error {
	if err := cli.PrintJSON(streams.Stdout, v); err != nil {
		return err
	}
	if errors > 0 {
		return cli.Exit(cli.ExitIncomplete)
	}
	return nil
}

var errEnough = errors.New("enough frames")

// tileCount is max(1, floor((end-start)/step)). The quotient gets a hair of
// slack so 0.3/0.1, which floats put just under 3, still counts three.
func tileCount(start, end, step float64) (int, error) {
	switch {
	case math.IsNaN(start) || math.IsNaN(end) || math.IsNaN(step) || math.IsInf(step, 0):
		return 0, errors.New("start, end and step must be finite numbers")
	case end <= start:
		return 0, fmt.Errorf("END (%s) must be after START (%s)", fmtFloat(end), fmtFloat(start))
	case step <= 0:
		return 0, fmt.Errorf("STEP must be above 0, got %s", fmtFloat(step))
	}
	q := (end - start) / step
	if q > frames.MaxTiles+1 {
		return 0, fmt.Errorf("%s to %s every %ss is more than the %d tiles a sheet may hold", fmtFloat(start), fmtFloat(end), fmtFloat(step), frames.MaxTiles)
	}
	return max(1, int(math.Floor(q+1e-9))), nil
}
