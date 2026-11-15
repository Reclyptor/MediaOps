package sheet

import (
	"image/color"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/reclyptor/mediaops/internal/cli"
)

const chain64 = "scale=trunc(iw*sar/2)*2:ih,setsar=1,scale=64:36:force_original_aspect_ratio=decrease,pad=64:36:(ow-iw)/2:(oh-ih)/2,format=rgb24"

func TestFramesArgvAndReport(t *testing.T) {
	file := touch(t, "e01.mkv")
	out := filepath.Join(t.TempDir(), "sheet.png")
	f := newFake(t, 3000)
	o := runFrames(f, file, out, "2600", "43:30", "2670.5", "--width", "64")
	if o.code != cli.ExitOK {
		t.Fatalf("exit %d: %s%s", o.code, o.stdout, o.stderr)
	}

	if len(f.probes) != 1 || strings.Join(f.probes[0], " ") != "ffprobe -v error -show_streams -of json "+file {
		t.Errorf("probes = %q", f.probes)
	}
	wantPipes := [][]string{}
	for _, at := range []string{"2600", "2610", "2670.5"} {
		wantPipes = append(wantPipes, []string{"ffmpeg", "-nostdin", "-v", "error", "-ss", at, "-i", file,
			"-map", "0:v:0", "-frames:v", "1", "-vf", chain64, "-f", "rawvideo", "-"})
	}
	// 43:30 is 2610 s.
	if len(f.pipes) != 3 {
		t.Fatalf("%d decodes, want 3", len(f.pipes))
	}
	for i, want := range wantPipes {
		if got := strings.Join(f.pipes[i], "\x00"); got != strings.Join(want, "\x00") {
			t.Errorf("decode %d = %q, want %q", i+1, f.pipes[i], want)
		}
	}

	var rep Report
	decodeReport(t, o, &rep)
	if rep.File != file || rep.Out != out || rep.Width != 64 || rep.Height != 36 || rep.Cols != 3 || rep.Rows != 1 || rep.Errors != 0 || len(rep.Tiles) != 3 {
		t.Errorf("report = %+v", rep)
	}
	for i, want := range []TileReport{{1, 2600, "2600s", ""}, {2, 2610, "2610s", ""}, {3, 2670.5, "2670.5s", ""}} {
		if rep.Tiles[i] != want {
			t.Errorf("tile %d = %+v, want %+v", i+1, rep.Tiles[i], want)
		}
	}
	b := readPNG(t, out).Bounds()
	if b.Dx() != 3*64+2*2 || b.Dy() != 36 {
		t.Errorf("sheet is %v, want 196x36", b)
	}
}

func TestFramesPastTheEndIsAPlaceholderAndExit1(t *testing.T) {
	file := touch(t, "e01.mkv")
	out := filepath.Join(t.TempDir(), "sheet.png")
	f := newFake(t, 5)
	o := runFrames(f, file, out, "1", "2", "7", "--width", "64", "--cols", "2")
	if o.code != cli.ExitIncomplete {
		t.Fatalf("exit %d, want 1: %s%s", o.code, o.stdout, o.stderr)
	}
	var rep Report
	decodeReport(t, o, &rep)
	if rep.Errors != 1 || rep.Cols != 2 || rep.Rows != 2 || len(rep.Tiles) != 3 {
		t.Fatalf("report = %+v", rep)
	}
	if rep.Tiles[2].Error != "no frame" || rep.Tiles[2].Label != "7s" || rep.Tiles[0].Error != "" {
		t.Errorf("tiles = %+v", rep.Tiles)
	}
	img := readPNG(t, out)
	if b := img.Bounds(); b.Dx() != 2*64+2 || b.Dy() != 2*36+2 {
		t.Fatalf("sheet is %v, want 130x74", b)
	}
	// Tile 3 is the first of row two. Its lower right corner is clear of the label.
	gray := color.RGBAModel.Convert(img.At(60, 36+2+30))
	if gray != (color.RGBA{40, 40, 40, 255}) {
		t.Errorf("placeholder pixel = %v, want dark gray", gray)
	}
	if px := color.RGBAModel.Convert(img.At(60, 30)); px == gray {
		t.Errorf("tile 1 pixel is the placeholder gray")
	}
}

func TestFramesRefusesBeforeDecoding(t *testing.T) {
	file := touch(t, "e01.mkv")
	dir := t.TempDir()
	out := filepath.Join(dir, "sheet.png")
	existingDir := filepath.Join(dir, "d.png")
	if err := os.Mkdir(existingDir, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link.png")
	if err := os.Symlink(file, link); err != nil {
		t.Fatal(err)
	}
	many := []string{file, out}
	for i := 0; i <= 600; i++ {
		many = append(many, strconv.Itoa(i))
	}
	cases := map[string][]string{
		"no arguments":         {},
		"no time":              {file, out},
		"a missing file":       {filepath.Join(dir, "nope.mkv"), out, "1"},
		"a directory as file":  {dir, out, "1"},
		"out not a png":        {file, filepath.Join(dir, "sheet.jpg"), "1"},
		"out is a directory":   {file, existingDir, "1"},
		"out is a symlink":     {file, link, "1"},
		"out directory absent": {file, filepath.Join(dir, "absent", "sheet.png"), "1"},
		"a negative time":      {file, out, "-1"},
		"a bad time":           {file, out, "1:60"},
		"601 tiles":            many,
		"odd width":            {file, out, "1", "--width", "65"},
		"zero width":           {file, out, "1", "--width", "0"},
		"zero columns":         {file, out, "1", "--cols", "0"},
		"negative columns":     {file, out, "1", "--cols", "-2"},
		"an unknown flag":      {file, out, "1", "--bogus"},
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			f := newFake(t, 100)
			o := runFrames(f, args...)
			if o.code != cli.ExitUsage {
				t.Errorf("exit %d, want 2: %s%s", o.code, o.stdout, o.stderr)
			}
			if o.stdout != "" {
				t.Errorf("stdout = %q, want nothing", o.stdout)
			}
			if len(f.probes)+len(f.pipes) != 0 {
				t.Errorf("a child ran: %q %q", f.probes, f.pipes)
			}
			if _, err := os.Stat(out); err == nil {
				t.Errorf("%s was written", out)
			}
		})
	}
}

func TestFramesRefusesAnOversizedSheetAfterTheProbe(t *testing.T) {
	file := touch(t, "e01.mkv")
	out := filepath.Join(t.TempDir(), "sheet.png")
	f := newFake(t, 100)
	args := []string{file, out, "--width", "4000"}
	for i := 0; i < 600; i++ {
		args = append(args, "1")
	}
	o := runFrames(f, args...)
	if o.code != cli.ExitUsage || len(f.probes) != 1 || len(f.pipes) != 0 {
		t.Fatalf("exit %d, %d probes, %d decodes: %s", o.code, len(f.probes), len(f.pipes), o.stderr)
	}
}

func TestFramesFailuresAreExit4WithNoSheet(t *testing.T) {
	file := touch(t, "e01.mkv")
	for name, set := range map[string]func(*fakeExec){
		"ffprobe fails": func(f *fakeExec) { f.probeExit = 1 },
		"ffmpeg fails":  func(f *fakeExec) { f.exit = 1 },
	} {
		t.Run(name, func(t *testing.T) {
			out := filepath.Join(t.TempDir(), "sheet.png")
			f := newFake(t, 100)
			set(f)
			o := runFrames(f, file, out, "1", "2", "--width", "64")
			if o.code != cli.ExitInternal || !strings.Contains(o.stderr, "FATAL") {
				t.Errorf("exit %d, stderr %q; want 4 and a FATAL line", o.code, o.stderr)
			}
			if o.stdout != "" {
				t.Errorf("stdout = %q", o.stdout)
			}
			if _, err := os.Stat(out); err == nil {
				t.Error("a sheet was written")
			}
		})
	}
}

func TestFramesColumnsDefaultToThreeAtMost(t *testing.T) {
	file := touch(t, "e01.mkv")
	for n, wantCols := range map[int]int{1: 1, 2: 2, 3: 3, 4: 3, 7: 3} {
		out := filepath.Join(t.TempDir(), "sheet.png")
		args := []string{file, out, "--width", "64"}
		for i := 0; i < n; i++ {
			args = append(args, "1")
		}
		var rep Report
		o := runFrames(newFake(t, 100), args...)
		decodeReport(t, o, &rep)
		if rep.Cols != wantCols || rep.Rows != (n+wantCols-1)/wantCols {
			t.Errorf("%d tiles: %d cols x %d rows, want %d cols", n, rep.Cols, rep.Rows, wantCols)
		}
	}
}

func TestFramesReplacesAnExistingSheet(t *testing.T) {
	file := touch(t, "e01.mkv")
	out := filepath.Join(t.TempDir(), "sheet.png")
	if err := os.WriteFile(out, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	if o := runFrames(newFake(t, 100), file, out, "1", "--width", "64"); o.code != cli.ExitOK {
		t.Fatalf("exit %d: %s", o.code, o.stderr)
	}
	readPNG(t, out)
}

func TestFramesHelpTextKeepsTheGrammar(t *testing.T) {
	o := runFrames(newFake(t, 1))
	for _, want := range []string{"ED credits", "next-episode preview", "CUT", "cold open", "title card", "OUTGOING"} {
		if !strings.Contains(o.stderr, want) {
			t.Errorf("usage lacks %q", want)
		}
	}
}
