package sheet

import (
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/reclyptor/mediaops/internal/cli"
)

func TestTileCount(t *testing.T) {
	ok := []struct {
		start, end, step float64
		want             int
	}{
		{0, 120, 3, 40},
		{0, 6, 2, 3},
		{1422.3, 1425.3, 1, 3},
		{0, 0.3, 0.1, 3},
		{0, 2.9, 3, 1},
		{10, 10.001, 3, 1},
		{0, 119.99, 3, 39},
		{0, 600, 1, 600},
		{0, 601, 1, 601},
	}
	for _, tc := range ok {
		if got, err := tileCount(tc.start, tc.end, tc.step); err != nil || got != tc.want {
			t.Errorf("tileCount(%v, %v, %v) = %d, %v; want %d", tc.start, tc.end, tc.step, got, err, tc.want)
		}
	}
	for _, tc := range [][3]float64{{5, 5, 1}, {6, 5, 1}, {0, 10, 0}, {0, 10, -1}, {0, 1e9, 1}, {0, 10, nan()}, {nan(), 10, 1}} {
		if got, err := tileCount(tc[0], tc[1], tc[2]); err == nil {
			t.Errorf("tileCount(%v) = %d, want an error", tc, got)
		}
	}
}

func nan() float64 { var z float64; return z / z }

func chainBand(w, h int, crop string) string {
	box := strconv.Itoa(w) + ":" + strconv.Itoa(h)
	return "scale=trunc(iw*sar/2)*2:ih,setsar=1," + crop + "scale=" + box + ":force_original_aspect_ratio=decrease,pad=" + box + ":(ow-iw)/2:(oh-ih)/2,format=rgb24"
}

func TestTitlescanArgvDefaultsToTheWholeFrame(t *testing.T) {
	file := touch(t, "e01.mkv")
	out := filepath.Join(t.TempDir(), "scan.png")
	f := newFake(t, 100)
	f.extra = 1
	o := runTitlescan(f, file, out, "10", "22", "--width", "64")
	if o.code != cli.ExitOK {
		t.Fatalf("exit %d: %s%s", o.code, o.stdout, o.stderr)
	}
	if len(f.pipes) != 1 {
		t.Fatalf("%d decodes, want one", len(f.pipes))
	}
	want := []string{"ffmpeg", "-nostdin", "-v", "error", "-ss", "10", "-i", file, "-t", "12", "-map", "0:v:0",
		"-vf", "fps=1/3," + chainBand(64, 36, ""), "-f", "rawvideo", "-"}
	if strings.Join(f.pipes[0], "\x00") != strings.Join(want, "\x00") {
		t.Errorf("argv = %q\nwant   %q", f.pipes[0], want)
	}
	if strings.Contains(strings.Join(f.pipes[0], " "), "crop=") {
		t.Error("the default band cropped the frame")
	}

	var rep TitlescanReport
	decodeReport(t, o, &rep)
	if rep.Start != 10 || rep.End != 22 || rep.Step != 3 || rep.Band != "full" {
		t.Errorf("report = %+v", rep)
	}
	if len(rep.Tiles) != 4 || rep.Cols != 2 || rep.Rows != 2 || rep.Width != 64 || rep.Height != 36 || rep.Errors != 0 {
		t.Fatalf("report = %+v", rep)
	}
	for i, label := range []string{"10s", "13s", "16s", "19s"} {
		if rep.Tiles[i].Label != label || rep.Tiles[i].At != float64(10+3*i) {
			t.Errorf("tile %d = %+v, want %s", i+1, rep.Tiles[i], label)
		}
	}
	if b := readPNG(t, out).Bounds(); b.Dx() != 130 || b.Dy() != 74 {
		t.Errorf("sheet is %v, want 130x74", b)
	}
}

func TestTitlescanBandCropsAndShrinksTheTile(t *testing.T) {
	file := touch(t, "e01.mkv")
	out := filepath.Join(t.TempDir(), "scan.png")
	f := newFake(t, 100)
	o := runTitlescan(f, file, out, "0", "6", "--step", "2", "--band", "0.3:0.7", "--width", "64")
	if o.code != cli.ExitOK {
		t.Fatalf("exit %d: %s%s", o.code, o.stdout, o.stderr)
	}
	crop := "crop=iw:trunc(ih*0.3/2)*2:0:trunc(ih*0.7/2)*2,"
	want := "fps=1/2," + chainBand(64, 10, crop)
	if got := argAfter(f.pipes[0], "-vf"); got != want {
		t.Errorf("-vf = %q\nwant  %q", got, want)
	}
	var rep TitlescanReport
	decodeReport(t, o, &rep)
	if rep.Band != "0.3:0.7" || rep.Height != 10 || rep.Step != 2 || len(rep.Tiles) != 3 || rep.Cols != 2 {
		t.Errorf("report = %+v", rep)
	}
	if got := argAfter(f.pipes[0], "-t"); got != "6" {
		t.Errorf("-t = %q", got)
	}
}

func TestTitlescanBandSpellingsOfTheWholeFrame(t *testing.T) {
	file := touch(t, "e01.mkv")
	for _, band := range []string{"full", "1", "1.0", "1.00"} {
		f := newFake(t, 100)
		out := filepath.Join(t.TempDir(), "scan.png")
		o := runTitlescan(f, file, out, "0", "3", "--band", band, "--width", "64")
		if o.code != cli.ExitOK || strings.Contains(strings.Join(f.pipes[0], " "), "crop=") {
			t.Errorf("band %s: exit %d, argv %q", band, o.code, f.pipes)
		}
	}
	f := newFake(t, 100)
	o := runTitlescan(f, file, filepath.Join(t.TempDir(), "scan.png"), "0", "3", "--band", "0.55", "--width", "64")
	if got := argAfter(f.pipes[0], "-vf"); o.code != cli.ExitOK || !strings.Contains(got, "crop=iw:trunc(ih*0.55/2)*2:0:trunc(ih*0/2)*2") {
		t.Errorf("exit %d, -vf %q", o.code, got)
	}
}

func TestTitlescanWindowPastTheEndLeavesPlaceholders(t *testing.T) {
	file := touch(t, "e01.mkv")
	out := filepath.Join(t.TempDir(), "scan.png")
	f := newFake(t, 5)
	o := runTitlescan(f, file, out, "3", "12", "--width", "64")
	if o.code != cli.ExitIncomplete {
		t.Fatalf("exit %d, want 1: %s%s", o.code, o.stdout, o.stderr)
	}
	var rep TitlescanReport
	decodeReport(t, o, &rep)
	if len(rep.Tiles) != 3 || rep.Errors != 2 || rep.Tiles[0].Error != "" || rep.Tiles[1].Error != "no frame" || rep.Tiles[2].Error != "no frame" {
		t.Errorf("report = %+v", rep)
	}
	readPNG(t, out)
}

func TestTitlescanRefusesBeforeDecoding(t *testing.T) {
	file := touch(t, "e01.mkv")
	dir := t.TempDir()
	out := filepath.Join(dir, "scan.png")
	cases := map[string][]string{
		"no arguments":     {},
		"no end":           {file, out, "0"},
		"end equals start": {file, out, "5", "5"},
		"end before start": {file, out, "9", "5"},
		"zero step":        {file, out, "0", "9", "--step", "0"},
		"negative step":    {file, out, "0", "9", "--step", "-1"},
		"bad band":         {file, out, "0", "9", "--band", "bottom"},
		"band past frame":  {file, out, "0", "9", "--band", "0.5:0.6"},
		"bad start":        {file, out, "x", "9"},
		"bad end":          {file, out, "0", "1:60"},
		"too many tiles":   {file, out, "0", "2000"},
		"odd width":        {file, out, "0", "9", "--width", "341"},
		"a directory":      {dir, out, "0", "9"},
		"out not png":      {file, filepath.Join(dir, "scan.jpg"), "0", "9"},
		"out dir absent":   {file, filepath.Join(dir, "absent", "scan.png"), "0", "9"},
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			f := newFake(t, 100)
			o := runTitlescan(f, args...)
			if o.code != cli.ExitUsage || o.stdout != "" || len(f.probes)+len(f.pipes) != 0 {
				t.Errorf("exit %d, stdout %q, %d probes, %d decodes: %s", o.code, o.stdout, len(f.probes), len(f.pipes), o.stderr)
			}
		})
	}
}

func TestTitlescanFailuresAreExit4(t *testing.T) {
	file := touch(t, "e01.mkv")
	for name, set := range map[string]func(*fakeExec){
		"ffprobe fails": func(f *fakeExec) { f.probeExit = 1 },
		// A decode that already has its N frames is killed, so only one that
		// ends short can show its exit status.
		"ffmpeg fails": func(f *fakeExec) { f.exit, f.length = 1, 0 },
	} {
		t.Run(name, func(t *testing.T) {
			out := filepath.Join(t.TempDir(), "scan.png")
			f := newFake(t, 100)
			set(f)
			o := runTitlescan(f, file, out, "0", "6", "--width", "64")
			if o.code != cli.ExitInternal || o.stdout != "" {
				t.Errorf("exit %d, stdout %q: %s", o.code, o.stdout, o.stderr)
			}
		})
	}
}

func TestTitlescanUsageExplainsTheWholeFrameDefault(t *testing.T) {
	o := runTitlescan(newFake(t, 1))
	for _, want := range []string{"DEFAULT", "false negative", "never a default", "ALWAYS run this"} {
		if !strings.Contains(o.stderr, want) {
			t.Errorf("usage lacks %q", want)
		}
	}
}
