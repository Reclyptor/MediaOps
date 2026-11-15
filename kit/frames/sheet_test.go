package frames

import (
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"golang.org/x/sys/unix"
)

func solid(box Box, c color.RGBA) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, box.W, box.H))
	draw.Draw(img, img.Bounds(), image.NewUniform(c), image.Point{}, draw.Src)
	return img
}

func TestCheckSheet(t *testing.T) {
	box := Box{320, 180}
	tests := []struct {
		name    string
		box     Box
		cols, n int
		wantErr bool
	}{
		{"one tile", box, 1, 1, false},
		{"max tiles", Box{64, 36}, 25, MaxTiles, false},
		{"too many tiles", Box{64, 36}, 25, MaxTiles + 1, true},
		{"no tiles", box, 3, 0, true},
		{"no columns", box, 0, 3, true},
		{"negative columns", box, -2, 3, true},
		{"odd box", Box{321, 180}, 3, 3, true},
		{"zero box", Box{}, 3, 3, true},
		{"too many pixels", Box{1920, 1080}, 20, 600, true},
		{"just inside the pixel limit", Box{4998, 2}, 1, 1, false},
		{"huge box", Box{1 << 40, 1 << 40}, 1, 1, true},
		{"more columns than tiles", box, 1000, 2, false},
	}
	for _, tc := range tests {
		err := CheckSheet(tc.box, tc.cols, tc.n)
		if (err != nil) != tc.wantErr {
			t.Errorf("%s: err = %v, wantErr %v", tc.name, err, tc.wantErr)
		}
	}
	// 100 Mpx exactly is allowed and one more row is not.
	if err := CheckSheet(Box{10000, 10000}, 1, 1); err != nil {
		t.Errorf("a sheet of exactly %d pixels: %v", MaxPixels, err)
	}
	if err := CheckSheet(Box{10000, 10000}, 1, 2); err == nil {
		t.Error("two 10000x10000 tiles passed")
	}
}

func TestSheetGeometry(t *testing.T) {
	box := Box{20, 10}
	red, green, blue := color.RGBA{255, 0, 0, 255}, color.RGBA{0, 255, 0, 255}, color.RGBA{0, 0, 255, 255}
	tiles := []Tile{{Img: solid(box, red)}, {Img: solid(box, green)}, {Img: solid(box, blue)}, {}}
	out, err := Sheet(box, 3, tiles)
	if err != nil {
		t.Fatal(err)
	}
	// 3 columns, 2 rows, a 2 px gap between them.
	if want := image.Rect(0, 0, 3*20+2*2, 2*10+2); out.Bounds() != want {
		t.Fatalf("bounds = %v, want %v", out.Bounds(), want)
	}
	at := func(x, y int) color.RGBA { return out.RGBAAt(x, y) }
	black := color.RGBA{0, 0, 0, 255}
	for _, tc := range []struct {
		x, y int
		want color.RGBA
	}{
		{0, 0, red}, {19, 9, red}, {22, 9, green}, {41, 0, green}, {44, 9, blue}, {63, 9, blue},
		{43, 5, black},
		{20, 5, black}, {21, 5, black}, {42, 5, black}, {5, 10, black}, {5, 11, black},
		{63, 21, black}, {50, 15, black},
	} {
		if got := at(tc.x, tc.y); got != tc.want {
			t.Errorf("pixel (%d,%d) = %+v, want %+v", tc.x, tc.y, got, tc.want)
		}
	}
	if got := at(0, 12); got != placeholderGray {
		t.Errorf("placeholder corner = %+v, want %+v", got, placeholderGray)
	}
	if got := at(19, 21); got != placeholderGray {
		t.Errorf("placeholder far corner = %+v, want %+v", got, placeholderGray)
	}
}

func TestSheetLabelsAndPlaceholder(t *testing.T) {
	box := Box{64, 36}
	src := solid(box, color.RGBA{10, 20, 30, 255})
	out, err := Sheet(box, 2, []Tile{{Img: src, Label: "12s"}, {Label: "no frame"}})
	if err != nil {
		t.Fatal(err)
	}
	yellow := color.RGBA{255, 255, 0, 255}
	has := func(r image.Rectangle) bool {
		for y := r.Min.Y; y < r.Max.Y; y++ {
			for x := r.Min.X; x < r.Max.X; x++ {
				if out.RGBAAt(x, y) == yellow {
					return true
				}
			}
		}
		return false
	}
	if !has(image.Rect(0, 0, 64, 18)) || !has(image.Rect(66, 0, 130, 18)) {
		t.Error("a label is missing")
	}
	if has(image.Rect(0, 18, 64, 36)) {
		t.Error("a label spilled below its box")
	}
	if src.RGBAAt(2, 2) != (color.RGBA{10, 20, 30, 255}) {
		t.Error("Sheet drew its label onto the caller's tile")
	}
	if got := out.RGBAAt(100, 30); got != placeholderGray {
		t.Errorf("placeholder = %+v", got)
	}
}

func TestSheetRefusals(t *testing.T) {
	box := Box{20, 10}
	if _, err := Sheet(box, 2, []Tile{{Img: solid(Box{20, 12}, color.RGBA{})}}); err == nil || !strings.Contains(err.Error(), "tile 1") {
		t.Errorf("mismatched tile: err = %v", err)
	}
	if _, err := Sheet(box, 2, []Tile{{Label: "a"}, {Img: solid(Box{18, 10}, color.RGBA{})}}); err == nil || !strings.Contains(err.Error(), "tile 2") {
		t.Errorf("mismatched second tile: err = %v", err)
	}
	if _, err := Sheet(box, 2, nil); err == nil {
		t.Error("an empty sheet was built")
	}
	if _, err := Sheet(box, 0, []Tile{{}}); err == nil {
		t.Error("a sheet without columns was built")
	}
}

func TestSheetOfOneRowWithFewerTilesThanColumns(t *testing.T) {
	out, err := Sheet(Box{20, 10}, 5, []Tile{{}, {}})
	if err != nil || out.Bounds().Dx() != 42 || out.Bounds().Dy() != 10 {
		t.Fatalf("got %v, %v", out, err)
	}
}

func TestWritePNG(t *testing.T) {
	old := syscall.Umask(0o007)
	defer syscall.Umask(old)
	img := solid(Box{8, 4}, color.RGBA{1, 2, 3, 255})

	t.Run("writes a png with the library mode", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "out.PNG")
		if err := WritePNG(path, img); err != nil {
			t.Fatal(err)
		}
		fi, err := os.Stat(path)
		if err != nil || fi.Mode().Perm() != 0o660 {
			t.Fatalf("stat = %v, %v; want mode 0660", fi, err)
		}
		f, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		back, err := png.Decode(f)
		if err != nil || back.Bounds().Dx() != 8 {
			t.Fatalf("decoded %v, %v", back, err)
		}
		assertOnlyFile(t, dir, "out.PNG")
	})

	t.Run("replaces a regular file", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "out.png")
		os.WriteFile(path, []byte("old"), 0o600)
		if err := WritePNG(path, img); err != nil {
			t.Fatal(err)
		}
		data, _ := os.ReadFile(path)
		if !strings.HasPrefix(string(data), "\x89PNG") {
			t.Fatalf("file holds %q", data[:min(len(data), 8)])
		}
		if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o660 {
			t.Errorf("mode = %v", fi.Mode())
		}
		assertOnlyFile(t, dir, "out.png")
	})

	t.Run("wrong extension", func(t *testing.T) {
		dir := t.TempDir()
		for _, name := range []string{"out.jpg", "out", "out.mkv", "out.png.mkv"} {
			if err := WritePNG(filepath.Join(dir, name), img); err == nil {
				t.Errorf("%s was written", name)
			}
		}
		assertOnlyFile(t, dir)
	})

	t.Run("a directory", func(t *testing.T) {
		dir := t.TempDir()
		target := filepath.Join(dir, "x.png")
		os.Mkdir(target, 0o770)
		if err := WritePNG(target, img); err == nil {
			t.Error("a directory was replaced")
		}
		if fi, err := os.Lstat(target); err != nil || !fi.IsDir() {
			t.Errorf("the directory is gone: %v", err)
		}
	})

	t.Run("a fifo", func(t *testing.T) {
		dir := t.TempDir()
		target := filepath.Join(dir, "x.png")
		if err := unix.Mkfifo(target, 0o660); err != nil {
			t.Fatal(err)
		}
		if err := WritePNG(target, img); err == nil {
			t.Error("a fifo was replaced")
		}
		if fi, err := os.Lstat(target); err != nil || fi.Mode()&os.ModeNamedPipe == 0 {
			t.Errorf("the fifo is gone: %v", err)
		}
	})

	t.Run("a symlink", func(t *testing.T) {
		dir := t.TempDir()
		real := filepath.Join(dir, "real.png")
		os.WriteFile(real, []byte("keep"), 0o660)
		link := filepath.Join(dir, "link.png")
		os.Symlink(real, link)
		if err := WritePNG(link, img); err == nil {
			t.Error("a symlink was replaced")
		}
		if data, _ := os.ReadFile(real); string(data) != "keep" {
			t.Error("the symlink target was written")
		}
	})

	t.Run("missing parent directory", func(t *testing.T) {
		dir := t.TempDir()
		if err := WritePNG(filepath.Join(dir, "nope", "x.png"), img); err == nil {
			t.Error("a missing directory was created")
		}
		assertOnlyFile(t, dir)
	})
}

func assertOnlyFile(t *testing.T, dir string, names ...string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, e := range entries {
		got = append(got, e.Name())
	}
	if strings.Join(got, ",") != strings.Join(names, ",") {
		t.Errorf("%s holds %v, want %v", dir, got, names)
	}
}
