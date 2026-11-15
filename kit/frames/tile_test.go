package frames

import (
	"bytes"
	"context"
	"errors"
	"image"
	"reflect"
	"strings"
	"testing"

	"github.com/reclyptor/mediaops/kit/proc"
)

func fp(v float64) *float64 { return &v }
func ip(v int) *int         { return &v }

func TestParseBand(t *testing.T) {
	ok := []struct {
		in   string
		want Band
		str  string
	}{
		{"full", Band{}, "full"},
		{"1", Band{}, "full"},
		{"1.0", Band{}, "full"},
		{"1.00", Band{}, "full"},
		{"1:0", Band{}, "full"},
		{"0.3", Band{Height: 0.3}, "0.3:0"},
		{"0.3:0.7", Band{Height: 0.3, Offset: 0.7}, "0.3:0.7"},
		{"0.1:0.9", Band{Height: 0.1, Offset: 0.9}, "0.1:0.9"},
		{".5:.25", Band{Height: 0.5, Offset: 0.25}, "0.5:0.25"},
		{"1:0.0000000001", Band{}, "full"},
	}
	for _, tc := range ok {
		got, err := ParseBand(tc.in)
		if err != nil || got != tc.want || got.String() != tc.str {
			t.Errorf("ParseBand(%q) = %+v %q, %v; want %+v %q", tc.in, got, got.String(), err, tc.want, tc.str)
		}
		if got.Full() != (tc.str == "full") {
			t.Errorf("ParseBand(%q).Full() = %v", tc.in, got.Full())
		}
		if again, err := ParseBand(got.String()); err != nil || again != got {
			t.Errorf("%q does not round-trip: %+v, %v", got.String(), again, err)
		}
	}
	for _, in := range []string{"", "0", "0:0.5", "-0.1", "1.1", "0.5:0.6", "0.3:-0.1", "abc", "0.3:abc", "0.3:", ":0.5", "NaN", "Inf", "0.3:0.2:0.1", "1:0.5", "full:0.5"} {
		if got, err := ParseBand(in); err == nil {
			t.Errorf("ParseBand(%q) = %+v, want an error", in, got)
		}
	}
}

func TestBoxes(t *testing.T) {
	for _, tc := range []struct {
		w    int
		want Box
	}{{640, Box{640, 360}}, {320, Box{320, 180}}, {256, Box{256, 144}}, {340, Box{340, 192}}, {2, Box{2, 2}}} {
		if got := Box16x9(tc.w); got != tc.want {
			t.Errorf("Box16x9(%d) = %+v, want %+v", tc.w, got, tc.want)
		}
	}
}

const probeVideo = `{"streams": [{"index": 0, "codec_type": "audio", "channels": 2},
 {"index": 1, "codec_type": "video", "width": %W%, "height": %H%, "sample_aspect_ratio": "%SAR%"}]}`

func probeJSON(w, h, sar string) proc.Result {
	s := strings.NewReplacer("%W%", w, "%H%", h, "%SAR%", sar).Replace(probeVideo)
	return proc.Result{Stdout: []byte(s)}
}

func TestFitBox(t *testing.T) {
	tests := []struct {
		name string
		res  proc.Result
		w    int
		band Band
		want Box
	}{
		{"square pixels", probeJSON("1920", "1080", "1:1"), 640, Band{}, Box{640, 360}},
		{"no sar", probeJSON("1920", "1080", "N/A"), 640, Band{}, Box{640, 360}},
		{"unset sar", probeJSON("720", "480", "0:1"), 480, Band{}, Box{480, 320}},
		{"anamorphic ntsc", probeJSON("720", "480", "32:27"), 640, Band{}, Box{640, 360}},
		{"anamorphic pal", probeJSON("720", "576", "64:45"), 640, Band{}, Box{640, 360}},
		{"band top third", probeJSON("1920", "1080", "1:1"), 640, Band{Height: 0.3, Offset: 0.7}, Box{640, 108}},
		{"tiny band keeps two pixels", probeJSON("1920", "1080", "1:1"), 64, Band{Height: 0.001}, Box{64, 2}},
		{"full band value", probeJSON("1920", "1080", "1:1"), 640, Band{Height: 1}, Box{640, 360}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := &fakeRunner{t: t, res: tc.res}
			got, err := FitBox(context.Background(), r, "/x/a.mkv", tc.w, tc.band)
			if err != nil || got != tc.want {
				t.Fatalf("got %+v, %v; want %+v", got, err, tc.want)
			}
		})
	}

	fails := []struct {
		name string
		res  proc.Result
		w    int
	}{
		{"no video", proc.Result{Stdout: []byte(`{"streams": [{"index": 0, "codec_type": "audio"}]}`)}, 640},
		{"zero size", probeJSON("0", "0", "1:1"), 640},
		{"bad sar", probeJSON("1920", "1080", "x:y"), 640},
		{"odd width", probeJSON("1920", "1080", "1:1"), 641},
		{"ffprobe fails", proc.Result{ExitCode: 1, Stderr: []byte("nope")}, 640},
	}
	for _, tc := range fails {
		t.Run(tc.name, func(t *testing.T) {
			r := &fakeRunner{t: t, res: tc.res}
			if got, err := FitBox(context.Background(), r, "/x/a.mkv", tc.w, Band{}); err == nil {
				t.Fatalf("got %+v, want an error", got)
			}
		})
	}
}

func TestTileArgv(t *testing.T) {
	box := Box{320, 180}
	const tail = "scale=320:180:force_original_aspect_ratio=decrease,pad=320:180:(ow-iw)/2:(oh-ih)/2,format=rgb24"
	const head = "scale=trunc(iw*sar/2)*2:ih,setsar=1,"
	pre := []string{"ffmpeg", "-nostdin", "-v", "error"}
	cat := func(parts ...[]string) []string {
		var out []string
		for _, p := range parts {
			out = append(out, p...)
		}
		return out
	}
	end := func(vf string) []string { return []string{"-vf", vf, "-f", "rawvideo", "-"} }

	tests := []struct {
		name string
		o    TileOptions
		want []string
	}{
		{"plain", TileOptions{Box: box},
			cat(pre, []string{"-i", "/x/a.mkv", "-map", "0:v:0"}, end(head+tail))},
		{"seek from", TileOptions{Box: box, SeekFrom: fp(12.5)},
			cat(pre, []string{"-ss", "12.5", "-i", "/x/a.mkv", "-map", "0:v:0"}, end(head+tail))},
		{"seek eof", TileOptions{Box: box, SeekEOF: fp(-30)},
			cat(pre, []string{"-sseof", "-30", "-i", "/x/a.mkv", "-map", "0:v:0"}, end(head+tail))},
		{"duration and frames", TileOptions{Box: box, Duration: fp(6), MaxFrames: 4},
			cat(pre, []string{"-i", "/x/a.mkv", "-t", "6", "-map", "0:v:0", "-frames:v", "4"}, end(head+tail))},
		{"fps", TileOptions{Box: box, FPS: "1/3", SeekFrom: fp(100), Duration: fp(30)},
			cat(pre, []string{"-ss", "100", "-i", "/x/a.mkv", "-t", "30", "-map", "0:v:0"}, end("fps=1/3,"+head+tail))},
		{"band", TileOptions{Box: box, Band: Band{Height: 0.3, Offset: 0.7}, MaxFrames: 1, SeekFrom: fp(0)},
			cat(pre, []string{"-ss", "0", "-i", "/x/a.mkv", "-map", "0:v:0", "-frames:v", "1"},
				end(head+"crop=iw:trunc(ih*0.3/2)*2:0:trunc(ih*0.7/2)*2,"+tail))},
		{"band and fps", TileOptions{Box: box, Band: Band{Height: 0.5}, FPS: "1/3"},
			cat(pre, []string{"-i", "/x/a.mkv", "-map", "0:v:0"},
				end("fps=1/3,"+head+"crop=iw:trunc(ih*0.5/2)*2:0:trunc(ih*0/2)*2,"+tail))},
		{"everything", TileOptions{Box: box, Band: Band{Height: 0.25, Offset: 0.5}, SeekEOF: fp(-12.25), Duration: fp(3), FPS: "2", MaxFrames: 9},
			cat(pre, []string{"-sseof", "-12.25", "-i", "/x/a.mkv", "-t", "3", "-map", "0:v:0", "-frames:v", "9"},
				end("fps=2,"+head+"crop=iw:trunc(ih*0.25/2)*2:0:trunc(ih*0.5/2)*2,"+tail))},
		{"subtitle late", TileOptions{Box: box, SeekFrom: fp(100), Subtitle: ip(2)},
			cat(pre, []string{"-ss", "94", "-i", "/x/a.mkv", "-filter_complex", "[0:v:0][0:s:2]overlay," + head + tail + "[v]",
				"-map", "[v]", "-ss", "6", "-frames:v", "1", "-f", "rawvideo", "-"})},
		{"subtitle early", TileOptions{Box: box, SeekFrom: fp(2.5), Subtitle: ip(0), Band: Band{Height: 0.5}},
			cat(pre, []string{"-ss", "0", "-i", "/x/a.mkv", "-filter_complex",
				"[0:v:0][0:s:0]overlay," + head + "crop=iw:trunc(ih*0.5/2)*2:0:trunc(ih*0/2)*2," + tail + "[v]",
				"-map", "[v]", "-ss", "2.5", "-frames:v", "1", "-f", "rawvideo", "-"})},
		{"subtitle at six", TileOptions{Box: box, SeekFrom: fp(6), Subtitle: ip(1), MaxFrames: 1},
			cat(pre, []string{"-ss", "0", "-i", "/x/a.mkv", "-filter_complex", "[0:v:0][0:s:1]overlay," + head + tail + "[v]",
				"-map", "[v]", "-ss", "6", "-frames:v", "1", "-f", "rawvideo", "-"})},
		{"subtitle at zero", TileOptions{Box: box, SeekFrom: fp(0), Subtitle: ip(1)},
			cat(pre, []string{"-ss", "0", "-i", "/x/a.mkv", "-filter_complex", "[0:v:0][0:s:1]overlay," + head + tail + "[v]",
				"-map", "[v]", "-frames:v", "1", "-f", "rawvideo", "-"})},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := &fakePiper{t: t}
			n, err := RGBFrames(context.Background(), p, "/x/a.mkv", tc.o, func(int, *image.RGBA) error { return nil })
			if err != nil || n != 0 {
				t.Fatalf("got %d, %v", n, err)
			}
			if !reflect.DeepEqual(p.argv, tc.want) {
				t.Fatalf("argv =\n %q\nwant\n %q", p.argv, tc.want)
			}
		})
	}
}

func TestTileArgvRefusals(t *testing.T) {
	box := Box{320, 180}
	for name, o := range map[string]TileOptions{
		"no box":                {},
		"odd box":               {Box: Box{321, 180}},
		"tiny box":              {Box: Box{0, 2}},
		"both seeks":            {Box: box, SeekFrom: fp(1), SeekEOF: fp(-1)},
		"negative frames":       {Box: box, MaxFrames: -1},
		"band outside":          {Box: box, Band: Band{Height: 0.6, Offset: 0.6}},
		"subtitle no seek":      {Box: box, Subtitle: ip(0)},
		"subtitle with eof":     {Box: box, Subtitle: ip(0), SeekEOF: fp(-5)},
		"subtitle with fps":     {Box: box, Subtitle: ip(0), SeekFrom: fp(5), FPS: "1/3"},
		"subtitle with t":       {Box: box, Subtitle: ip(0), SeekFrom: fp(5), Duration: fp(2)},
		"subtitle many frames":  {Box: box, Subtitle: ip(0), SeekFrom: fp(5), MaxFrames: 2},
		"subtitle negative s:N": {Box: box, Subtitle: ip(-1), SeekFrom: fp(5)},
		"subtitle before zero":  {Box: box, Subtitle: ip(0), SeekFrom: fp(-5)},
	} {
		p := &fakePiper{t: t}
		if _, err := RGBFrames(context.Background(), p, "/x/a.mkv", o, func(int, *image.RGBA) error { return nil }); err == nil {
			t.Errorf("%s: no error", name)
		}
		if p.argv != nil {
			t.Errorf("%s: ffmpeg ran with %q", name, p.argv)
		}
	}
}

// rgbFrames builds n frames of box whose every byte is its index.
func rgbFrames(box Box, n int) []byte {
	var b bytes.Buffer
	for i := 0; i < n; i++ {
		b.Write(bytes.Repeat([]byte{byte(i + 1)}, box.W*box.H*3))
	}
	return b.Bytes()
}

func TestRGBFramesDecodesInOrder(t *testing.T) {
	box := Box{4, 2}
	data := rgbFrames(box, 3)
	data[0], data[1], data[2] = 10, 20, 30 // first pixel of frame 0
	p := &fakePiper{t: t, stdout: data}
	var got []*image.RGBA
	n, err := RGBFrames(context.Background(), p, "/x/a.mkv", TileOptions{Box: box}, func(i int, img *image.RGBA) error {
		if i != len(got) {
			t.Errorf("index %d, want %d", i, len(got))
		}
		got = append(got, img)
		return nil
	})
	if err != nil || n != 3 || len(got) != 3 {
		t.Fatalf("got %d frames, %v", n, err)
	}
	if got[0].Bounds() != image.Rect(0, 0, 4, 2) {
		t.Fatalf("bounds %v", got[0].Bounds())
	}
	if c := got[0].RGBAAt(0, 0); c.R != 10 || c.G != 20 || c.B != 30 || c.A != 255 {
		t.Errorf("first pixel = %+v", c)
	}
	if c := got[0].RGBAAt(3, 1); c.R != 1 || c.G != 1 || c.B != 1 || c.A != 255 {
		t.Errorf("last pixel of frame 0 = %+v", c)
	}
	if c := got[2].RGBAAt(1, 1); c.R != 3 || c.A != 255 {
		t.Errorf("frame 2 pixel = %+v", c)
	}
	if got[0] == got[1] || &got[0].Pix[0] == &got[1].Pix[0] {
		t.Error("frames share a buffer")
	}
}

func TestRGBFramesFailures(t *testing.T) {
	box := Box{4, 2}
	t.Run("partial trailing frame", func(t *testing.T) {
		p := &fakePiper{t: t, stdout: rgbFrames(box, 2)[:2*24-5], stderr: []byte("boom")}
		n, err := RGBFrames(context.Background(), p, "/x/a.mkv", TileOptions{Box: box}, func(int, *image.RGBA) error { return nil })
		if err == nil || !strings.Contains(err.Error(), "part way") || n != 1 {
			t.Fatalf("got %d, %v", n, err)
		}
	})
	t.Run("non-zero exit", func(t *testing.T) {
		p := &fakePiper{t: t, stdout: rgbFrames(box, 1), stderr: []byte("Invalid data found\n"), exit: 1}
		_, err := RGBFrames(context.Background(), p, "/x/a.mkv", TileOptions{Box: box}, func(int, *image.RGBA) error { return nil })
		if err == nil || !strings.Contains(err.Error(), "exited 1") || !strings.Contains(err.Error(), "Invalid data found") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("each fails", func(t *testing.T) {
		boom := errors.New("boom")
		calls := 0
		p := &fakePiper{t: t, stdout: rgbFrames(box, 3)}
		n, err := RGBFrames(context.Background(), p, "/x/a.mkv", TileOptions{Box: box}, func(int, *image.RGBA) error {
			calls++
			return boom
		})
		if !errors.Is(err, boom) || calls != 1 || n != 0 {
			t.Fatalf("got %d, %v after %d calls", n, err, calls)
		}
	})
}

func TestRGBFrame(t *testing.T) {
	box := Box{4, 2}
	p := &fakePiper{t: t, stdout: rgbFrames(box, 1)}
	img, err := RGBFrame(context.Background(), p, "/x/a.mkv", 7.5, TileOptions{Box: box})
	if err != nil || img == nil || img.RGBAAt(0, 0).R != 1 {
		t.Fatalf("got %v, %v", img, err)
	}
	want := []string{"-ss", "7.5", "-i", "/x/a.mkv", "-map", "0:v:0", "-frames:v", "1"}
	if !strings.Contains(strings.Join(p.argv, " "), strings.Join(want, " ")) {
		t.Errorf("argv = %q, want it to hold %q", p.argv, want)
	}

	p = &fakePiper{t: t}
	if _, err := RGBFrame(context.Background(), p, "/x/a.mkv", 99999, TileOptions{Box: box}); !errors.Is(err, ErrNoFrame) {
		t.Fatalf("err = %v, want ErrNoFrame", err)
	}
	p = &fakePiper{t: t, exit: 1, stderr: []byte("bad")}
	if _, err := RGBFrame(context.Background(), p, "/x/a.mkv", 1, TileOptions{Box: box}); err == nil || errors.Is(err, ErrNoFrame) {
		t.Fatalf("err = %v, want a failure that is not ErrNoFrame", err)
	}
}
