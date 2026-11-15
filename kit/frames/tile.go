package frames

import (
	"context"
	"errors"
	"fmt"
	"image"
	"io"
	"math"
	"strconv"
	"strings"

	"github.com/reclyptor/mediaops/kit/ffprobe"
	"github.com/reclyptor/mediaops/kit/proc"
)

// bandSlack absorbs the rounding of decimal fractions such as 0.3 + 0.7.
const bandSlack = 1e-9

// Box is the size of one tile in pixels. Both sides are even and at least 2,
// which is what yuv scaling and the rgb24 pad need.
type Box struct{ W, H int }

func (b Box) check() error {
	if b.W < 2 || b.H < 2 || b.W%2 != 0 || b.H%2 != 0 {
		return fmt.Errorf("a %dx%d tile box is not a pair of even sizes of at least 2", b.W, b.H)
	}
	return nil
}

// Band is the horizontal slice of a frame a tile shows, as fractions of the
// frame height. The zero value is the whole frame.
type Band struct{ Height, Offset float64 }

// ParseBand reads "full" (also 1, 1.0, 1.00), "H" for the top H of the frame,
// or "H:Y" for a band H high starting Y down. 0 < H <= 1, 0 <= Y, H+Y <= 1.
func ParseBand(s string) (Band, error) {
	if s == "full" {
		return Band{}, nil
	}
	hs, ys, hasY := strings.Cut(s, ":")
	h, err := parseFraction(hs)
	if err != nil {
		return Band{}, fmt.Errorf("band %q: its height %w", s, err)
	}
	y := 0.0
	if hasY {
		if y, err = parseFraction(ys); err != nil {
			return Band{}, fmt.Errorf("band %q: its offset %w", s, err)
		}
	}
	switch {
	case h <= 0 || h > 1+bandSlack:
		return Band{}, fmt.Errorf("band %q: the height must be above 0 and at most 1", s)
	case y < 0:
		return Band{}, fmt.Errorf("band %q: the offset must not be negative", s)
	case h+y > 1+bandSlack:
		return Band{}, fmt.Errorf("band %q: the height and offset add up to more than the frame", s)
	}
	b := Band{Height: h, Offset: y}
	if b.Full() {
		return Band{}, nil
	}
	return b, nil
}

func parseFraction(s string) (float64, error) {
	v, err := strconv.ParseFloat(s, 64)
	if err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
		return 0, fmt.Errorf("%q is not a number", s)
	}
	return v, nil
}

// Full reports whether the band is the whole frame.
func (b Band) Full() bool {
	return b.Height == 0 || (b.Height >= 1-bandSlack && b.Offset <= bandSlack)
}

// String is "full" or "H:Y", the form ParseBand reads back.
func (b Band) String() string {
	if b.Full() {
		return "full"
	}
	return fmtFloat(b.Height) + ":" + fmtFloat(b.Offset)
}

func fmtFloat(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }

func evenRound(v float64) int {
	n := 2 * int(math.Round(v/2))
	if n < 2 {
		n = 2
	}
	return n
}

// Box16x9 is the box of the given width for sources of no single shape.
func Box16x9(width int) Box {
	return Box{W: width, H: evenRound(float64(width) * 9 / 16)}
}

// FitBox sizes a tile of the given width for the first video stream of path:
// its display aspect (width times sample aspect over height), with the band
// shrinking the height. The height is rounded to even.
func FitBox(ctx context.Context, r proc.Runner, path string, width int, band Band) (Box, error) {
	if err := (Box{W: width, H: 2}).check(); err != nil {
		return Box{}, err
	}
	streams, err := ffprobe.Streams(ctx, r, path)
	if err != nil {
		return Box{}, err
	}
	for _, st := range streams {
		if st.CodecType != "video" {
			continue
		}
		if st.Width <= 0 || st.Height <= 0 {
			return Box{}, fmt.Errorf("the first video stream of %s reports a size of %dx%d", path, st.Width, st.Height)
		}
		sar := st.SampleAspect
		if sar <= 0 {
			sar = 1
		}
		frac := 1.0
		if !band.Full() {
			frac = band.Height
		}
		h := float64(width) * float64(st.Height) * frac / (float64(st.Width) * sar)
		return Box{W: width, H: evenRound(h)}, nil
	}
	return Box{}, fmt.Errorf("%s has no video stream", path)
}

// TileOptions says which frames RGBFrames decodes and how each is fitted.
type TileOptions struct {
	Box  Box
	Band Band
	// SeekFrom is -ss and SeekEOF -sseof (negative seconds), both before -i.
	SeekFrom *float64
	SeekEOF  *float64
	// Duration is -t after -i.
	Duration *float64
	// FPS is a rate such as "1/3", applied first in the filter chain.
	FPS       string
	MaxFrames int
	// Subtitle burns in bitmap subtitle stream s:N. It decodes the one frame
	// at SeekFrom, so it excludes SeekEOF, Duration and FPS.
	Subtitle *int
}

// ErrNoFrame is what RGBFrame wraps when ffmpeg decodes nothing, as it does
// when the seek lands past the end.
var ErrNoFrame = errors.New("ffmpeg decoded no frame")

// subtitleLead is how far ahead of the frame a subtitle decode starts, so the
// event that is on screen has been read by the time the frame is reached.
const subtitleLead = 6.0

// vfChain is the filter chain: restore the display aspect, crop the band,
// fit into the box and pad black.
func vfChain(o TileOptions) string {
	var f []string
	if o.FPS != "" {
		f = append(f, "fps="+o.FPS)
	}
	f = append(f, "scale=trunc(iw*sar/2)*2:ih", "setsar=1")
	if !o.Band.Full() {
		f = append(f, "crop=iw:trunc(ih*"+fmtFloat(o.Band.Height)+"/2)*2:0:trunc(ih*"+fmtFloat(o.Band.Offset)+"/2)*2")
	}
	w, h := strconv.Itoa(o.Box.W), strconv.Itoa(o.Box.H)
	f = append(f, "scale="+w+":"+h+":force_original_aspect_ratio=decrease", "pad="+w+":"+h+":(ow-iw)/2:(oh-ih)/2", "format=rgb24")
	return strings.Join(f, ",")
}

func tileArgv(path string, o TileOptions) ([]string, error) {
	if err := o.Box.check(); err != nil {
		return nil, err
	}
	if !o.Band.Full() && (o.Band.Height <= 0 || o.Band.Offset < 0 || o.Band.Height+o.Band.Offset > 1+bandSlack) {
		return nil, fmt.Errorf("band %s is not inside the frame", o.Band)
	}
	if o.SeekFrom != nil && o.SeekEOF != nil {
		return nil, errors.New("a tile cannot seek from the start and from the end at once")
	}
	if o.MaxFrames < 0 {
		return nil, fmt.Errorf("a limit of %d frames is negative", o.MaxFrames)
	}
	argv := []string{"ffmpeg", "-nostdin", "-v", "error"}

	if o.Subtitle != nil {
		switch {
		case *o.Subtitle < 0:
			return nil, fmt.Errorf("subtitle stream s:%d is negative", *o.Subtitle)
		case o.SeekFrom == nil:
			return nil, errors.New("burning in a subtitle needs a time to seek to")
		case o.SeekEOF != nil || o.FPS != "" || o.Duration != nil || o.MaxFrames > 1:
			return nil, errors.New("burning in a subtitle decodes the one frame at its time, so it excludes an end seek, fps, a duration and a frame limit above 1")
		case *o.SeekFrom < 0:
			return nil, fmt.Errorf("a seek to %vs is before the start", *o.SeekFrom)
		}
		pre := math.Max(0, *o.SeekFrom-subtitleLead)
		post := *o.SeekFrom - pre
		argv = append(argv, "-ss", seconds(pre), "-i", path,
			"-filter_complex", "[0:v:0][0:s:"+strconv.Itoa(*o.Subtitle)+"]overlay,"+vfChain(o)+"[v]", "-map", "[v]")
		if post > 0 {
			argv = append(argv, "-ss", seconds(post))
		}
		return append(argv, "-frames:v", "1", "-f", "rawvideo", "-"), nil
	}

	if o.SeekFrom != nil {
		argv = append(argv, "-ss", seconds(*o.SeekFrom))
	}
	if o.SeekEOF != nil {
		argv = append(argv, "-sseof", seconds(*o.SeekEOF))
	}
	argv = append(argv, "-i", path)
	if o.Duration != nil {
		argv = append(argv, "-t", seconds(*o.Duration))
	}
	argv = append(argv, "-map", "0:v:0")
	if o.MaxFrames > 0 {
		argv = append(argv, "-frames:v", strconv.Itoa(o.MaxFrames))
	}
	return append(argv, "-vf", vfChain(o), "-f", "rawvideo", "-"), nil
}

// RGBFrames streams the frames of path as rgb24 and calls each with every one
// in order, returning how many it saw. A frame that stops short, a non-zero
// exit, or an error from each (which also kills the child) is an error.
func RGBFrames(ctx context.Context, p proc.Piper, path string, o TileOptions, each func(i int, img *image.RGBA) error) (int, error) {
	argv, err := tileArgv(path, o)
	if err != nil {
		return 0, fmt.Errorf("planning frames of %s: %w", path, err)
	}
	size := o.Box.W * o.Box.H * 3
	buf := make([]byte, size)
	n := 0
	stderr, exit, err := p.Pipe(ctx, argv, func(stdout io.Reader) error {
		for {
			if _, err := io.ReadFull(stdout, buf); err != nil {
				if err == io.EOF {
					return nil
				}
				if err == io.ErrUnexpectedEOF {
					return fmt.Errorf("ffmpeg stopped part way through frame %d of %d bytes", n+1, size)
				}
				return err
			}
			if err := each(n, rgbToRGBA(buf, o.Box)); err != nil {
				return err
			}
			n++
		}
	})
	if err != nil {
		if msg := tail(stderr); msg != "" {
			return n, fmt.Errorf("reading frames of %s: %w (ffmpeg said: %s)", path, err, msg)
		}
		return n, fmt.Errorf("reading frames of %s: %w", path, err)
	}
	if exit != 0 {
		return n, fmt.Errorf("ffmpeg exited %d reading frames of %s: %s", exit, path, tail(stderr))
	}
	return n, nil
}

func rgbToRGBA(rgb []byte, b Box) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, b.W, b.H))
	for src, dst := 0, 0; src < len(rgb); src, dst = src+3, dst+4 {
		img.Pix[dst], img.Pix[dst+1], img.Pix[dst+2], img.Pix[dst+3] = rgb[src], rgb[src+1], rgb[src+2], 255
	}
	return img
}

// RGBFrame decodes the first frame at or after at seconds. ffmpeg exits 0
// with nothing when the seek is past the end, which comes back as an error
// wrapping ErrNoFrame.
func RGBFrame(ctx context.Context, p proc.Piper, path string, at float64, o TileOptions) (*image.RGBA, error) {
	o.SeekFrom, o.MaxFrames = &at, 1
	var got *image.RGBA
	if _, err := RGBFrames(ctx, p, path, o, func(_ int, img *image.RGBA) error {
		got = img
		return nil
	}); err != nil {
		return nil, err
	}
	if got == nil {
		return nil, fmt.Errorf("no frame of %s at %vs: %w", path, at, ErrNoFrame)
	}
	return got, nil
}
