package frames

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"os"
	"path/filepath"
	"strings"

	"github.com/reclyptor/mediaops/kit/fsutil"
)

// A sheet holds at most MaxTiles tiles and MaxPixels pixels, so a mistake in a
// column count cannot ask for gigabytes.
const (
	MaxTiles  = 600
	MaxPixels = 100_000_000

	tileGap = 2
)

var placeholderGray = color.RGBA{R: 40, G: 40, B: 40, A: 255}

// Tile is one cell of a sheet. A nil Img is a placeholder, a dark gray box
// that still carries its label.
type Tile struct {
	Img   *image.RGBA
	Label string
}

// sheetSize is the composed size of n tiles in cols columns. It assumes the
// box was checked and n and cols are positive.
func sheetSize(box Box, cols, n int) (w, h, usedCols int) {
	usedCols = min(cols, n)
	rows := (n + usedCols - 1) / usedCols
	return usedCols*box.W + (usedCols-1)*tileGap, rows*box.H + (rows-1)*tileGap, usedCols
}

// CheckSheet refuses a sheet Sheet would not build: no tiles, more than
// MaxTiles, no columns, a bad box, or more than MaxPixels once composed. Call
// it before decoding anything.
func CheckSheet(box Box, cols, n int) error {
	if err := box.check(); err != nil {
		return err
	}
	switch {
	case n < 1:
		return fmt.Errorf("a sheet needs at least one tile, not %d", n)
	case n > MaxTiles:
		return fmt.Errorf("%d tiles is more than the %d a sheet may hold", n, MaxTiles)
	case cols < 1:
		return fmt.Errorf("a sheet needs at least one column, not %d", cols)
	case box.W > MaxPixels || box.H > MaxPixels:
		return fmt.Errorf("a %dx%d tile is more than a sheet may hold", box.W, box.H)
	}
	w, h, _ := sheetSize(box, cols, n)
	if w*h > MaxPixels {
		return fmt.Errorf("a sheet of %d tiles in %d columns would be %dx%d, more than %d pixels", n, cols, w, h, MaxPixels)
	}
	return nil
}

// Sheet lays the tiles out row-major on a black background with a 2 px gap
// and draws each label. Every non-nil image must be exactly box-sized; the
// tiles themselves are not modified.
func Sheet(box Box, cols int, tiles []Tile) (*image.RGBA, error) {
	if err := CheckSheet(box, cols, len(tiles)); err != nil {
		return nil, err
	}
	for i, t := range tiles {
		if t.Img != nil && t.Img.Bounds().Size() != image.Pt(box.W, box.H) {
			return nil, fmt.Errorf("tile %d is %v, not the %dx%d box", i+1, t.Img.Bounds().Size(), box.W, box.H)
		}
	}
	w, h, usedCols := sheetSize(box, cols, len(tiles))
	out := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.Draw(out, out.Bounds(), image.NewUniform(color.Black), image.Point{}, draw.Src)
	for i, t := range tiles {
		x, y := (i%usedCols)*(box.W+tileGap), (i/usedCols)*(box.H+tileGap)
		cell := image.Rect(x, y, x+box.W, y+box.H)
		if t.Img != nil {
			draw.Draw(out, cell, t.Img, t.Img.Bounds().Min, draw.Src)
		} else {
			draw.Draw(out, cell, image.NewUniform(placeholderGray), image.Point{}, draw.Src)
		}
		DrawLabel(out.SubImage(cell).(*image.RGBA), t.Label)
	}
	return out, nil
}

// WritePNG writes img to path, which must end in .png. A path that exists and
// is not a regular file is refused. The file is replaced atomically with the
// library mode, and the parent directory must already exist.
func WritePNG(path string, img image.Image) error {
	if !strings.EqualFold(filepath.Ext(path), ".png") {
		return fmt.Errorf("%s does not end in .png", path)
	}
	if fi, err := os.Lstat(path); err == nil {
		if !fi.Mode().IsRegular() {
			return fmt.Errorf("%s exists and is not a regular file", path)
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("checking %s: %w", path, err)
	}
	if fi, err := os.Stat(filepath.Dir(path)); err != nil || !fi.IsDir() {
		return fmt.Errorf("the directory of %s does not exist", path)
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return fmt.Errorf("encoding %s: %w", path, err)
	}
	return fsutil.WriteFile(path, buf.Bytes(), fsutil.FileMode)
}
