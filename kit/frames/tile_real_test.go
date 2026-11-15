package frames

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"image/color"
	"os"
	"path/filepath"
	"testing"

	"github.com/reclyptor/mediaops/kit/ffprobe"
)

// These tests run the real ffmpeg, like real_test.go, and fail rather than
// skip when it is missing.

func lavfiMKV(t *testing.T, name, source string, extra ...string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	args := []string{"-nostdin", "-v", "error", "-f", "lavfi", "-i", source}
	args = append(args, extra...)
	args = append(args, "-c:v", "libx264", "-preset", "ultrafast", "-pix_fmt", "yuv420p", path)
	synth(t, "ffmpeg", args...)
	return path
}

func TestRGBFramesOfARealFile(t *testing.T) {
	src := lavfiMKV(t, "a.mkv", "testsrc2=duration=3:size=320x180:rate=24")
	box := Box{160, 90}
	var frames []*image.RGBA
	fps := "1/1"
	n, err := RGBFrames(context.Background(), realExec, src, TileOptions{Box: box, FPS: fps}, func(_ int, img *image.RGBA) error {
		frames = append(frames, img)
		return nil
	})
	if err != nil || n != 3 || len(frames) != 3 {
		t.Fatalf("got %d frames, %v; want 3", n, err)
	}
	for i, img := range frames {
		if img.Bounds() != image.Rect(0, 0, 160, 90) {
			t.Errorf("frame %d is %v", i, img.Bounds())
		}
		if img.RGBAAt(80, 45).A != 255 {
			t.Errorf("frame %d is not opaque", i)
		}
	}
	if bytes.Equal(frames[0].Pix, frames[2].Pix) {
		t.Error("the first and last frame are identical")
	}

	// A limit and a window.
	from, dur := 1.0, 1.0
	n, err = RGBFrames(context.Background(), realExec, src, TileOptions{Box: box, SeekFrom: &from, Duration: &dur, MaxFrames: 5},
		func(int, *image.RGBA) error { return nil })
	if err != nil || n != 5 {
		t.Fatalf("limited decode: %d, %v; want 5", n, err)
	}
}

func TestRGBFramesStopsWhenEachFails(t *testing.T) {
	src := lavfiMKV(t, "a.mkv", "testsrc2=duration=3:size=320x180:rate=24")
	boom := errors.New("enough")
	calls := 0
	_, err := RGBFrames(context.Background(), realExec, src, TileOptions{Box: Box{160, 90}}, func(int, *image.RGBA) error {
		calls++
		return boom
	})
	if !errors.Is(err, boom) || calls != 1 {
		t.Fatalf("err = %v after %d calls", err, calls)
	}
}

func TestRGBFramePastTheEnd(t *testing.T) {
	src := lavfiMKV(t, "a.mkv", "testsrc2=duration=3:size=320x180:rate=24")
	if _, err := RGBFrame(context.Background(), realExec, src, 999, TileOptions{Box: Box{160, 90}}); !errors.Is(err, ErrNoFrame) {
		t.Fatalf("err = %v, want ErrNoFrame", err)
	}
	if img, err := RGBFrame(context.Background(), realExec, src, 1.5, TileOptions{Box: Box{160, 90}}); err != nil || img == nil {
		t.Fatalf("a frame inside the file: %v, %v", img, err)
	}
	if _, err := RGBFrame(context.Background(), realExec, filepath.Join(t.TempDir(), "missing.mkv"), 1, TileOptions{Box: Box{160, 90}}); err == nil || errors.Is(err, ErrNoFrame) {
		t.Fatalf("a missing file: err = %v", err)
	}
}

func TestBandCropChangesTheTile(t *testing.T) {
	src := lavfiMKV(t, "a.mkv", "testsrc2=duration=2:size=320x180:rate=24")
	ctx := context.Background()
	full, err := FitBox(ctx, realExec, src, 160, Band{})
	if err != nil || full != (Box{160, 90}) {
		t.Fatalf("full box = %+v, %v", full, err)
	}
	topBand, bottomBand := Band{Height: 0.3}, Band{Height: 0.3, Offset: 0.7}
	box, err := FitBox(ctx, realExec, src, 160, topBand)
	if err != nil || box != (Box{160, 28}) {
		t.Fatalf("band box = %+v, %v; want 160x28", box, err)
	}
	grab := func(b Box, band Band) *image.RGBA {
		img, err := RGBFrame(ctx, realExec, src, 1, TileOptions{Box: b, Band: band})
		if err != nil {
			t.Fatal(err)
		}
		return img
	}
	top, bottom := grab(box, topBand), grab(box, bottomBand)
	if bytes.Equal(top.Pix, bottom.Pix) {
		t.Error("the top and bottom bands are the same picture")
	}
	whole := grab(Box{160, 28}, Band{})
	if bytes.Equal(top.Pix, whole.Pix) {
		t.Error("a band tile equals the squeezed whole frame")
	}
	// The top band of the full-size tile is the top rows of the whole frame.
	fullTile := grab(full, Band{})
	var diff int
	for y := 0; y < 24; y++ {
		for x := 0; x < 160; x++ {
			a, b := fullTile.RGBAAt(x, y), top.RGBAAt(x, y*28/27)
			if abs(int(a.R)-int(b.R))+abs(int(a.G)-int(b.G))+abs(int(a.B)-int(b.B)) > 180 {
				diff++
			}
		}
	}
	if diff > 160*24/4 {
		t.Errorf("the top band shares too little with the top of the frame: %d of %d pixels differ", diff, 160*24)
	}
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

func TestSampleAspectIsRestored(t *testing.T) {
	ctx := context.Background()
	plain := lavfiMKV(t, "plain.mkv", "testsrc2=duration=1:size=720x480:rate=5")
	wide := lavfiMKV(t, "wide.mkv", "testsrc2=duration=1:size=720x480:rate=5,setsar=32/27")

	st, err := ffprobe.Streams(ctx, realExec, plain)
	if err != nil || st[0].SampleAspect != 1 {
		t.Fatalf("a stream without a sample aspect: %+v, %v", st, err)
	}
	st, err = ffprobe.Streams(ctx, realExec, wide)
	if err != nil || st[0].SampleAspect < 1.18 || st[0].SampleAspect > 1.19 {
		t.Fatalf("an anamorphic stream: %+v, %v", st, err)
	}

	pbox, err := FitBox(ctx, realExec, plain, 480, Band{})
	if err != nil || pbox != (Box{480, 320}) {
		t.Fatalf("plain box = %+v, %v", pbox, err)
	}
	wbox, err := FitBox(ctx, realExec, wide, 480, Band{})
	if err != nil || wbox != (Box{480, 270}) {
		t.Fatalf("anamorphic box = %+v, %v", wbox, err)
	}

	// Into a 16:9 box the plain 3:2 frame is pillarboxed, so its left column
	// is padding, and the anamorphic one fills the box.
	box := Box{320, 180}
	column := func(img *image.RGBA) (nonBlack int) {
		for y := 0; y < box.H; y++ {
			if c := img.RGBAAt(0, y); c != (color.RGBA{0, 0, 0, 255}) {
				nonBlack++
			}
		}
		return nonBlack
	}
	pimg, err := RGBFrame(ctx, realExec, plain, 0, TileOptions{Box: box})
	if err != nil {
		t.Fatal(err)
	}
	wimg, err := RGBFrame(ctx, realExec, wide, 0, TileOptions{Box: box})
	if err != nil {
		t.Fatal(err)
	}
	if got := column(pimg); got != 0 {
		t.Errorf("the 3:2 frame has %d picture pixels in its left column, want it padded", got)
	}
	if got := column(wimg); got < box.H/2 {
		t.Errorf("the anamorphic frame has %d picture pixels in its left column, want it to fill the box", got)
	}
}

// vobsub writes a VobSub index and its sub file with one subtitle: a white
// 160x40 block at (20,100) from start for about two seconds. The RLE is one
// run-to-end-of-line code per row, so the picture is a solid block.
func vobsub(t *testing.T, dir string, start float64) string {
	t.Helper()
	const w, h, x, y = 160, 40, 20, 100
	rows := bytes.Repeat([]byte{0x00, 0x01}, h/2)
	rle := append(append([]byte{}, rows...), rows...)

	ctrlOff := 4 + len(rle)
	var ctrl bytes.Buffer
	seq2 := ctrlOff + 2 + 2 + 1 + 3 + 3 + 7 + 5 + 1
	be := func(v ...uint16) {
		for _, n := range v {
			binary.Write(&ctrl, binary.BigEndian, n)
		}
	}
	be(0, uint16(seq2))
	ctrl.Write([]byte{0x01, 0x03, 0x01, 0x10, 0x04, 0x00, 0xF0})
	x2, y2 := x+w-1, y+h-1
	ctrl.Write([]byte{0x05, x >> 4, byte(x&15)<<4 | byte(x2>>8), byte(x2), y >> 4, byte(y&15)<<4 | byte(y2>>8), byte(y2)})
	ctrl.WriteByte(0x06)
	be(4, uint16(4+len(rows)))
	ctrl.WriteByte(0xFF)
	be(175, uint16(seq2))
	ctrl.Write([]byte{0x02, 0xFF})

	var spu bytes.Buffer
	binary.Write(&spu, binary.BigEndian, uint16(4+len(rle)+ctrl.Len()))
	binary.Write(&spu, binary.BigEndian, uint16(ctrlOff))
	spu.Write(rle)
	spu.Write(ctrl.Bytes())

	pts := uint64(start * 90000)
	var sub bytes.Buffer
	sub.Write([]byte{0, 0, 1, 0xBA, 0x44, 0, 4, 0, 4, 1, 1, 0x89, 0xC3, 0xF8})
	sub.Write([]byte{0, 0, 1, 0xBD})
	binary.Write(&sub, binary.BigEndian, uint16(9+spu.Len()))
	sub.Write([]byte{0x81, 0x80, 0x05,
		0x21 | byte(pts>>30&7)<<1, byte(pts >> 22), byte(pts>>15&0x7F)<<1 | 1, byte(pts >> 7), byte(pts&0x7F)<<1 | 1, 0x20})
	sub.Write(spu.Bytes())
	if err := os.WriteFile(filepath.Join(dir, "v.sub"), sub.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}

	idx := fmt.Sprintf(`# VobSub index file, v7 (do not modify this line!)
size: 320x180
org: 0, 0
scale: 100%%, 100%%
alpha: 100%%
smooth: OFF
fadein/out: 0, 0
align: OFF at LEFT TOP
time offset: 0
forced subs: OFF
palette: 000000, ffffff, 000000, 000000, 000000, 000000, 000000, 000000, 000000, 000000, 000000, 000000, 000000, 000000, 000000, 000000
custom colors: OFF, tridx: 0000, colors: 000000, 000000, 000000, 000000
langidx: 0
id: en, index: 0
timestamp: 00:00:%02d:000, filepos: 000000000
`, int(start))
	path := filepath.Join(dir, "v.idx")
	if err := os.WriteFile(path, []byte(idx), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestSubtitleIsBurnedIn builds an MKV with a DVD subtitle on screen from
// 7 s to about 9 s and checks the burn-in only touches frames inside that
// window. 8 s exercises the seek ahead of the frame, 3 s the plain case.
func TestSubtitleIsBurnedIn(t *testing.T) {
	dir := t.TempDir()
	idx := vobsub(t, dir, 7)
	src := filepath.Join(dir, "subs.mkv")
	synth(t, "ffmpeg", "-nostdin", "-v", "error", "-f", "lavfi", "-i", "testsrc2=duration=10:size=320x180:rate=10",
		"-i", idx, "-map", "0:v", "-map", "1:s", "-c:v", "libx264", "-preset", "ultrafast", "-pix_fmt", "yuv420p", "-c:s", "copy", src)

	ctx := context.Background()
	box := Box{320, 180}
	sub0 := 0
	frame := func(at float64, burn bool) *image.RGBA {
		o := TileOptions{Box: box}
		if burn {
			o.Subtitle = &sub0
		}
		img, err := RGBFrame(ctx, realExec, src, at, o)
		if err != nil {
			t.Fatalf("frame at %vs (burn %v): %v", at, burn, err)
		}
		return img
	}
	white := func(img *image.RGBA) bool {
		c := img.RGBAAt(100, 120)
		return c.R > 240 && c.G > 240 && c.B > 240
	}

	burned, plain := frame(8, true), frame(8, false)
	if bytes.Equal(burned.Pix, plain.Pix) {
		t.Fatal("the frame at 8s is the same with and without the subtitle")
	}
	if !white(burned) || white(plain) {
		t.Errorf("block pixel: burned %+v, plain %+v", burned.RGBAAt(100, 120), plain.RGBAAt(100, 120))
	}
	for _, at := range []float64{3, 5.5, 9.6} {
		if !bytes.Equal(frame(at, true).Pix, frame(at, false).Pix) {
			t.Errorf("the frame at %vs changed with no subtitle on screen", at)
		}
	}
}
