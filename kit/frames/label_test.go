package frames

import (
	"image"
	"image/color"
	"os"
	"path/filepath"
	"testing"
)

func TestFontIsComplete(t *testing.T) {
	if len(glyphArt) != 95 {
		t.Errorf("%d glyphs, want 95", len(glyphArt))
	}
	seen := map[[glyphH]uint8]rune{}
	for r := ' '; r <= '~'; r++ {
		g := glyphFor(r)
		if r != ' ' && g == glyphFor(' ') {
			t.Errorf("%q is blank", r)
		}
		if prev, dup := seen[g]; dup {
			t.Errorf("%q and %q have the same shape", prev, r)
		}
		seen[g] = r
	}
	if glyphFor('é') != glyphFor('?') || glyphFor('\n') != glyphFor('?') || glyphFor(0x7f) != glyphFor('?') {
		t.Error("an unprintable rune is not drawn as '?'")
	}
}

func TestDrawLabelPixels(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 100, 40))
	for i := range img.Pix {
		img.Pix[i] = 128
	}
	DrawLabel(img, "I")
	yellow, black := color.RGBA{255, 255, 0, 255}, color.RGBA{0, 0, 0, 255}
	// The box is the 5x7 glyph at scale 2 plus a 2 px margin on every side.
	for _, p := range []image.Point{{0, 0}, {13, 17}, {1, 8}} {
		if got := img.RGBAAt(p.X, p.Y); got != black {
			t.Errorf("%v = %+v, want the black box", p, got)
		}
	}
	if got := img.RGBAAt(14, 0); got == black || got == yellow {
		t.Errorf("(14,0) = %+v, want it outside the box", got)
	}
	if got := img.RGBAAt(5, 20); got == black {
		t.Errorf("(5,20) = %+v, want the box to stop at 18", got)
	}
	// 'I' is ".###." on its first row: columns 1..3, at x = 2 + 2..7.
	if got := img.RGBAAt(2+2, 2); got != yellow {
		t.Errorf("(4,2) = %+v, want yellow", got)
	}
	if got := img.RGBAAt(2, 2); got != black {
		t.Errorf("(2,2) = %+v, want black", got)
	}
}

func TestDrawLabelClipsAndNeverPanics(t *testing.T) {
	for _, img := range []*image.RGBA{
		image.NewRGBA(image.Rect(0, 0, 30, 10)),
		image.NewRGBA(image.Rect(0, 0, 1, 1)),
		image.NewRGBA(image.Rect(0, 0, 0, 0)),
		image.NewRGBA(image.Rect(50, 50, 80, 70)).SubImage(image.Rect(55, 55, 70, 65)).(*image.RGBA),
	} {
		long := make([]rune, 500)
		for i := range long {
			long[i] = rune(i % 300)
		}
		DrawLabel(img, string(long))
		DrawLabel(img, "")
	}
	DrawLabel(nil, "x")

	sub := image.NewRGBA(image.Rect(50, 50, 80, 70))
	DrawLabel(sub, "Hi")
	if sub.RGBAAt(50, 50) != (color.RGBA{0, 0, 0, 255}) {
		t.Error("a label on an offset image does not start at its own corner")
	}
	if sub.RGBAAt(49, 49) != (color.RGBA{}) {
		t.Error("the label spilled outside the image")
	}
}

// TestRenderEveryGlyph writes each printable glyph as drawn by DrawLabel,
// rows of 16, to a PNG a person can look at.
func TestRenderEveryGlyph(t *testing.T) {
	dir := os.Getenv("GLYPH_PNG_DIR")
	if dir == "" {
		dir = t.TempDir()
	}
	var tiles []Tile
	for start := ' '; start <= '~'; start += 16 {
		text := ""
		for r := start; r < start+16 && r <= '~'; r++ {
			text += string(r)
		}
		tiles = append(tiles, Tile{Img: image.NewRGBA(image.Rect(0, 0, 16*12+4, 18)), Label: text})
	}
	box := Box{16*12 + 4, 18}
	out, err := Sheet(box, 1, tiles)
	if err != nil {
		t.Fatal(err)
	}
	big := image.NewRGBA(image.Rect(0, 0, out.Bounds().Dx()*4, out.Bounds().Dy()*4))
	for y := 0; y < big.Bounds().Dy(); y++ {
		for x := 0; x < big.Bounds().Dx(); x++ {
			big.SetRGBA(x, y, out.RGBAAt(x/4, y/4))
		}
	}
	if err := WritePNG(filepath.Join(dir, "glyphs.png"), big); err != nil {
		t.Fatal(err)
	}
}
