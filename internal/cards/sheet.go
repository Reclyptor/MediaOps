package cards

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"image"
	"io"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/reclyptor/mediaops/internal/cli"
	"github.com/reclyptor/mediaops/kit/ffprobe"
	"github.com/reclyptor/mediaops/kit/frames"
	"github.com/reclyptor/mediaops/kit/proc"
)

const (
	// maxInput bounds a report or points file read from a path or from stdin.
	maxInput = 64 << 20
	// maxStem is how many runes of a file's name a label carries.
	maxStem = 24

	defaultSheetWidth = 320
	defaultSeamWidth  = 256
	defaultChapCols   = 7
	maxAutoCols       = 8
	seamCols          = 8

	// A seam row: part k at last-24, last-12 and its last frame, then part
	// k+1 at frames 0, 12, 24, 36 and 48.
	tailFrames = 25
	tailEOF    = -6.0
	headFrames = 49
	headStep   = 12
)

// bitmapSubtitles are the subtitle codecs ffmpeg's overlay filter can burn in.
var bitmapSubtitles = map[string]bool{"hdmv_pgs_subtitle": true, "dvd_subtitle": true, "dvb_subtitle": true}

// Tools is what the sheet needs of the outside world: ffprobe and one-shot
// children through the Runner, and decoded frames through the Piper.
type Tools interface {
	proc.Runner
	proc.Piper
}

// SheetTile is one cell of a written sheet. At is the second a frame was taken
// at and Frame names the frame of a seam tile. A placeholder is a dark tile
// that stands for a frame that was not drawn: Error says why it failed, Note
// why a file the detector could not judge has no tile of its own.
type SheetTile struct {
	N           int      `json:"n"`
	File        string   `json:"file"`
	At          *float64 `json:"at,omitempty"`
	Frame       string   `json:"frame,omitempty"`
	Label       string   `json:"label"`
	Placeholder bool     `json:"placeholder,omitempty"`
	Error       string   `json:"error,omitempty"`
	Note        string   `json:"note,omitempty"`
}

// SheetReport is mkv-cards sheet's one JSON value. Width and Height are one
// tile's size. Errors counts tiles that failed to decode: a placeholder for a
// file the detector found nothing in is no error of the sheet.
type SheetReport struct {
	Out    string      `json:"out"`
	Mode   string      `json:"mode"`
	Width  int         `json:"width"`
	Height int         `json:"height"`
	Cols   int         `json:"cols"`
	Rows   int         `json:"rows"`
	Tiles  []SheetTile `json:"tiles"`
	Errors int         `json:"errors"`
}

// request is a tile to draw: the frame of file at a second, or a placeholder.
type request struct {
	tile SheetTile
	at   float64
}

type sheetArgs struct {
	out   string
	mode  string
	width int
	cols  int
	subs  *int
}

func runSheet(ctx context.Context, t Tools, args []string, streams cli.IO) error {
	fs := cli.NewFlagSet("mkv-cards sheet")
	var (
		candidates, points, chapters string
		seams                        bool
		a                            sheetArgs
		offset                       float64
		subs                         int
	)
	fs.StringVar(&candidates, "candidates", "", "")
	fs.StringVar(&points, "points", "", "")
	fs.StringVar(&chapters, "chapters", "", "")
	fs.BoolVar(&seams, "seams", false, "")
	fs.IntVar(&a.width, "width", 0, "")
	fs.IntVar(&a.cols, "cols", 0, "")
	fs.Float64Var(&offset, "offset", 3, "")
	fs.IntVar(&subs, "subs", 0, "")
	rest, err := cli.ParseFlags(fs, args)
	if err != nil {
		return cli.Usage(fmt.Errorf("%w\n%s", err, usage))
	}
	set := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { set[f.Name] = true })

	var modes []string
	for _, m := range []struct {
		name string
		on   bool
	}{{"candidates", set["candidates"]}, {"points", set["points"]}, {"chapters", set["chapters"]}, {"seams", seams}} {
		if m.on {
			modes = append(modes, m.name)
		}
	}
	if len(modes) != 1 {
		return cli.Usage(fmt.Errorf("give exactly one of --candidates, --points, --chapters and --seams\n%s", usage))
	}
	a.mode = modes[0]
	if len(rest) == 0 {
		return cli.Usage(fmt.Errorf("no OUT.png given\n%s", usage))
	}
	if set["subs"] {
		if subs < 0 {
			return cli.Usage(fmt.Errorf("--subs must be 0 or more, got %d", subs))
		}
		a.subs = &subs
	}
	switch {
	case set["offset"] && a.mode != "chapters":
		return cli.Usage(errors.New("--offset belongs to --chapters"))
	case a.mode == "seams" && (set["subs"] || set["cols"]):
		return cli.Usage(errors.New("--seams has a fixed layout and burns in no subtitles, so --subs and --cols do not apply"))
	case a.cols < 0:
		return cli.Usage(fmt.Errorf("--cols must be 0 or more, got %d", a.cols))
	case set["width"] && (a.width < 2 || a.width%2 != 0):
		return cli.Usage(fmt.Errorf("--width must be an even number of at least 2, got %d", a.width))
	}
	if a.out, err = checkOut(rest[0]); err != nil {
		return err
	}
	rest = rest[1:]
	if a.mode != "seams" && len(rest) > 0 {
		return cli.Usage(fmt.Errorf("unexpected argument %q after OUT.png", rest[0]))
	}

	if a.mode == "seams" {
		if a.width == 0 {
			a.width = defaultSeamWidth
		}
		return sheetSeams(ctx, t, a, rest, streams)
	}
	if a.width == 0 {
		a.width = defaultSheetWidth
	}

	var reqs []request
	box := frames.Box16x9(a.width)
	switch a.mode {
	case "candidates":
		reqs, err = candidateRequests(candidates, streams)
	case "points":
		reqs, err = pointRequests(points, streams)
	case "chapters":
		if !finite(offset) || offset < 0 {
			return cli.Usage(fmt.Errorf("--offset must be a number of at least 0, got %v", offset))
		}
		reqs, box, err = chapterRequests(ctx, t, chapters, offset, a.width)
	}
	if err != nil {
		return err
	}
	if a.subs != nil {
		if err := checkSubtitles(ctx, t, reqs, *a.subs); err != nil {
			return err
		}
	}
	if a.cols == 0 {
		a.cols = defaultCols(a.mode, len(reqs))
	}
	if err := frames.CheckSheet(box, a.cols, len(reqs)); err != nil {
		return cli.Usage(err)
	}

	tiles := make([]frames.Tile, len(reqs))
	rep := SheetReport{Out: a.out, Mode: a.mode}
	for i := range reqs {
		r := &reqs[i]
		r.tile.N = i + 1
		if !r.tile.Placeholder {
			img, err := frames.RGBFrame(ctx, t, r.tile.File, r.at, frames.TileOptions{Box: box, Subtitle: a.subs})
			if err != nil {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				r.tile.Placeholder, r.tile.Error = true, tileError(err, r.at)
			} else {
				tiles[i].Img = img
			}
		}
		tiles[i].Label = r.tile.Label
		rep.Tiles = append(rep.Tiles, r.tile)
	}
	return finishSheet(streams, a, box, tiles, rep)
}

// defaultCols is ceil(sqrt(n)) up to 8 columns, and 7 for chapters, as the
// per-job scripts drew them.
func defaultCols(mode string, n int) int {
	if mode == "chapters" {
		return defaultChapCols
	}
	return min(maxAutoCols, int(math.Ceil(math.Sqrt(float64(n)))))
}

func tileError(err error, at float64) string {
	if errors.Is(err, frames.ErrNoFrame) {
		return "no frame at " + fmtNum(at) + "s"
	}
	return err.Error()
}

// finishSheet composes and writes the sheet and prints its report. It exits 1
// when a tile failed to decode: the sheet is still written, and still useful.
func finishSheet(streams cli.IO, a sheetArgs, box frames.Box, tiles []frames.Tile, rep SheetReport) error {
	img, err := frames.Sheet(box, a.cols, tiles)
	if err != nil {
		return fmt.Errorf("composing %s: %w", a.out, err)
	}
	if err := frames.WritePNG(a.out, img); err != nil {
		return fmt.Errorf("writing the sheet: %w", err)
	}
	rep.Width, rep.Height = box.W, box.H
	rep.Cols = min(a.cols, len(tiles))
	rep.Rows = (len(tiles) + rep.Cols - 1) / rep.Cols
	for _, t := range rep.Tiles {
		if t.Error != "" {
			rep.Errors++
		}
	}
	if err := cli.PrintJSON(streams.Stdout, rep); err != nil {
		return err
	}
	if rep.Errors > 0 {
		return cli.Exit(cli.ExitIncomplete)
	}
	return nil
}

// checkOut refuses, before anything is decoded, an OUT that WritePNG would.
func checkOut(path string) (string, error) {
	if !strings.EqualFold(filepath.Ext(path), ".png") {
		return "", cli.Usage(fmt.Errorf("%s does not end in .png", path))
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", cli.Usage(fmt.Errorf("cannot resolve %s: %w", path, err))
	}
	if fi, err := os.Lstat(abs); err == nil && !fi.Mode().IsRegular() {
		return "", cli.Usage(fmt.Errorf("%s exists and is not a regular file", path))
	}
	if fi, err := os.Stat(filepath.Dir(abs)); err != nil || !fi.IsDir() {
		return "", cli.Usage(fmt.Errorf("the directory of %s does not exist", path))
	}
	return abs, nil
}

func stem(path string) string {
	base := filepath.Base(path)
	base = strings.TrimSuffix(base, filepath.Ext(base))
	if r := []rune(base); len(r) > maxStem {
		return string(r[:maxStem])
	}
	return base
}

func pointLabel(n int, path string, at float64) string {
	return strconv.Itoa(n) + " " + stem(path) + " " + fmtNum(at) + "s"
}

// readInput reads a report or a points file from path, or from stdin for "-",
// refusing anything that is not a regular file or is over maxInput bytes.
func readInput(path string, streams cli.IO) ([]byte, error) {
	var r io.Reader
	switch {
	case path == "-":
		if streams.Stdin == nil {
			return nil, cli.Usage(errors.New("no standard input to read"))
		}
		r = streams.Stdin
	default:
		fi, err := os.Stat(path)
		switch {
		case err != nil:
			return nil, cli.Usage(fmt.Errorf("cannot read %s: %w", path, err))
		case !fi.Mode().IsRegular():
			return nil, cli.Usage(fmt.Errorf("%s is not a regular file", path))
		}
		f, err := os.Open(path)
		if err != nil {
			return nil, cli.Usage(fmt.Errorf("cannot read %s: %w", path, err))
		}
		defer f.Close()
		r = f
	}
	b, err := io.ReadAll(io.LimitReader(r, maxInput+1))
	switch {
	case err != nil:
		return nil, cli.Usage(fmt.Errorf("reading %s: %w", path, err))
	case len(b) > maxInput:
		return nil, cli.Usage(fmt.Errorf("%s is larger than %d MiB", path, maxInput>>20))
	}
	return b, nil
}

// requireRegular exits 2 for a file a tile would decode that is not a regular
// file.
func requireRegular(path string) error {
	fi, err := os.Stat(path)
	switch {
	case err != nil:
		return cli.Usage(fmt.Errorf("cannot read %s: %w", path, err))
	case !fi.Mode().IsRegular():
		return cli.Usage(fmt.Errorf("%s is not a regular file", path))
	}
	return nil
}

func candidateRequests(path string, streams cli.IO) ([]request, error) {
	b, err := readInput(path, streams)
	if err != nil {
		return nil, err
	}
	located, err := ReadLocated(bytes.NewReader(b))
	if err != nil {
		return nil, cli.Usage(err)
	}
	var reqs []request
	checked := map[string]bool{}
	for _, f := range located {
		if len(f.At) == 0 {
			label, note := "NO CANDIDATE: "+stem(f.Path), "the detector found no candidate in this file"
			if f.Error != "" {
				label, note = "ERROR: "+stem(f.Path), "the detector failed on this file: "+f.Error
			}
			reqs = append(reqs, request{tile: SheetTile{File: f.Path, Label: label, Placeholder: true, Note: note}})
			continue
		}
		if !checked[f.Path] {
			if err := requireRegular(f.Path); err != nil {
				return nil, err
			}
			checked[f.Path] = true
		}
		starred := false
		for _, at := range f.At {
			at := at
			label := pointLabel(len(reqs)+1, f.Path, at)
			if !starred && f.Best != nil && at == *f.Best {
				label, starred = label+" *", true
			}
			reqs = append(reqs, request{at: at, tile: SheetTile{File: f.Path, At: &at, Label: label}})
		}
	}
	return reqs, nil
}

func pointRequests(path string, streams cli.IO) ([]request, error) {
	b, err := readInput(path, streams)
	if err != nil {
		return nil, err
	}
	var reqs []request
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Buffer(nil, maxInput)
	checked := map[string]bool{}
	for line := 1; sc.Scan(); line++ {
		text := strings.TrimRight(sc.Text(), "\r")
		if strings.TrimSpace(text) == "" {
			continue
		}
		fields := strings.SplitN(text, "\t", 3)
		if len(fields) < 2 || fields[0] == "" {
			return nil, cli.Usage(fmt.Errorf("%s line %d: want PATH<TAB>SECONDS[<TAB>LABEL]", path, line))
		}
		at, err := strconv.ParseFloat(strings.TrimSpace(fields[1]), 64)
		if err != nil || !finite(at) || at < 0 {
			return nil, cli.Usage(fmt.Errorf("%s line %d: %q is not a time in seconds of at least 0", path, line, fields[1]))
		}
		if !checked[fields[0]] {
			if err := requireRegular(fields[0]); err != nil {
				return nil, fmt.Errorf("%s line %d: %w", path, line, err)
			}
			checked[fields[0]] = true
		}
		label := pointLabel(len(reqs)+1, fields[0], at)
		if len(fields) == 3 {
			label = fields[2]
		}
		reqs = append(reqs, request{at: at, tile: SheetTile{File: fields[0], At: &at, Label: label}})
	}
	if err := sc.Err(); err != nil {
		return nil, cli.Usage(fmt.Errorf("reading %s: %w", path, err))
	}
	return reqs, nil
}

func chapterRequests(ctx context.Context, t Tools, path string, offset float64, width int) ([]request, frames.Box, error) {
	if err := requireRegular(path); err != nil {
		return nil, frames.Box{}, err
	}
	chapters, err := ffprobe.Chapters(ctx, t, path)
	if err != nil {
		return nil, frames.Box{}, err
	}
	if len(chapters) == 0 {
		return nil, frames.Box{}, cli.Usage(fmt.Errorf("%s has no chapters", path))
	}
	box, err := frames.FitBox(ctx, t, path, width, frames.Band{})
	if err != nil {
		return nil, frames.Box{}, err
	}
	reqs := make([]request, len(chapters))
	for i, c := range chapters {
		at := round3(c.Start + offset)
		reqs[i] = request{at: at, tile: SheetTile{
			File: path, At: &at, Label: "ch" + strconv.Itoa(i+1) + " " + strconv.FormatFloat(c.Start, 'f', 1, 64),
		}}
	}
	return reqs, box, nil
}

// checkSubtitles requires subtitle stream s:n of every file a tile will be
// decoded from to exist and to be a bitmap codec, so a text stream (which the
// overlay filter cannot burn in) is refused before any decode.
func checkSubtitles(ctx context.Context, t Tools, reqs []request, n int) error {
	seen := map[string]bool{}
	for _, r := range reqs {
		if r.tile.Placeholder || seen[r.tile.File] {
			continue
		}
		seen[r.tile.File] = true
		streams, err := ffprobe.Streams(ctx, t, r.tile.File)
		if err != nil {
			return err
		}
		idx := 0
		found := false
		for _, st := range streams {
			if st.CodecType != "subtitle" {
				continue
			}
			if idx == n {
				if !bitmapSubtitles[st.CodecName] {
					return cli.Usage(fmt.Errorf("subtitle stream s:%d of %s is %s, not a bitmap codec (hdmv_pgs_subtitle, dvd_subtitle or dvb_subtitle), so it cannot be burned in", n, r.tile.File, st.CodecName))
				}
				found = true
				break
			}
			idx++
		}
		if !found {
			return cli.Usage(fmt.Errorf("%s has %d subtitle streams, so there is no s:%d", r.tile.File, idx, n))
		}
	}
	return nil
}

func sheetSeams(ctx context.Context, t Tools, a sheetArgs, parts []string, streams cli.IO) error {
	if len(parts) < 2 {
		return cli.Usage(errors.New("--seams needs at least two parts, in order"))
	}
	for i, p := range parts {
		if err := requireRegular(p); err != nil {
			return err
		}
		abs, err := filepath.Abs(p)
		if err != nil {
			return cli.Usage(fmt.Errorf("cannot resolve %s: %w", p, err))
		}
		parts[i] = abs
	}
	box, err := frames.FitBox(ctx, t, parts[0], a.width, frames.Band{})
	if err != nil {
		return err
	}
	a.cols = seamCols
	if err := frames.CheckSheet(box, a.cols, (len(parts)-1)*seamCols); err != nil {
		return cli.Usage(err)
	}

	rep := SheetReport{Out: a.out, Mode: "seams"}
	var tiles []frames.Tile
	for k := 0; k+1 < len(parts); k++ {
		row, err := seamRow(ctx, t, box, k+1, parts[k], parts[k+1])
		if err != nil {
			return err
		}
		for _, c := range row {
			c.tile.N = len(tiles) + 1
			tiles = append(tiles, frames.Tile{Img: c.img, Label: c.tile.Label})
			rep.Tiles = append(rep.Tiles, c.tile)
		}
	}
	return finishSheet(streams, a, box, tiles, rep)
}

// cell is a seam tile with the frame it holds, nil for a placeholder.
type cell struct {
	tile SheetTile
	img  *image.RGBA
}

// seamRow is one cut: the last frames of part k (n is its 1-based number) and
// the first of part k+1. A part that yields fewer frames than a tile needs
// leaves that tile a placeholder with the reason.
func seamRow(ctx context.Context, t Tools, box frames.Box, n int, a, b string) ([]cell, error) {
	row := make([]cell, 0, seamCols)

	eof := tailEOF
	var ring []*image.RGBA
	_, tailErr := frames.RGBFrames(ctx, t, a, frames.TileOptions{Box: box, SeekEOF: &eof}, func(_ int, img *image.RGBA) error {
		if len(ring) == tailFrames {
			ring = ring[1:]
		}
		ring = append(ring, img)
		return nil
	})
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	for _, w := range []struct {
		frame, label string
		back         int
	}{{"last-24", "last-24", 24}, {"last-12", "last-12", 12}, {"last", "LAST", 0}} {
		c := cell{tile: SheetTile{File: a, Frame: w.frame, Label: "p" + strconv.Itoa(n) + " " + w.label}}
		switch {
		case tailErr != nil:
			c.fail(tailErr.Error())
		case len(ring) <= w.back:
			c.fail(fmt.Sprintf("the last 6 s of %s hold %d frames, and this tile is %d from the end", a, len(ring), w.back))
		default:
			c.img = ring[len(ring)-1-w.back]
		}
		row = append(row, c)
	}

	head := map[int]*image.RGBA{}
	got, headErr := frames.RGBFrames(ctx, t, b, frames.TileOptions{Box: box, MaxFrames: headFrames}, func(i int, img *image.RGBA) error {
		if i%headStep == 0 {
			head[i] = img
		}
		return nil
	})
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	for i := 0; i < headFrames; i += headStep {
		name := "f" + strconv.Itoa(i)
		c := cell{tile: SheetTile{File: b, Frame: name, Label: "p" + strconv.Itoa(n+1) + " " + name}}
		switch {
		case headErr != nil:
			c.fail(headErr.Error())
		case got <= i:
			c.fail(fmt.Sprintf("%s decoded %d frames, and this tile is frame %d", b, got, i))
		default:
			c.img = head[i]
		}
		row = append(row, c)
	}
	return row, nil
}

func (c *cell) fail(msg string) {
	c.img = nil
	c.tile.Placeholder, c.tile.Error = true, msg
}
