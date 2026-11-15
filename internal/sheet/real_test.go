//go:build integration

package sheet

import (
	"bytes"
	"image/color"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/reclyptor/mediaops/internal/cli"
	"github.com/reclyptor/mediaops/kit/proc"
)

// These run the real ffmpeg through proc.Exec; a missing binary fails them
// rather than skipping, because a skipped proof is a proof nobody ran.

// buildClip writes a 6 s 320x180 clip from lavfi testsrc2. This is the only
// place in the package where an encoder runs, and it never runs outside tests.
func buildClip(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Fatalf("ffmpeg is required to run these tests: %v", err)
	}
	path := filepath.Join(t.TempDir(), "clip.mkv")
	out, err := exec.Command("ffmpeg", "-nostdin", "-v", "error",
		"-f", "lavfi", "-i", "testsrc2=d=6:s=320x180:r=25",
		"-c:v", "libx264", "-preset", "ultrafast", "-pix_fmt", "yuv420p", "-g", "25", path).CombinedOutput()
	if err != nil {
		t.Fatalf("building the clip: %v\n%s", err, out)
	}
	return path
}

func withUmask(t *testing.T, mask int) {
	t.Helper()
	old := syscall.Umask(mask)
	t.Cleanup(func() { syscall.Umask(old) })
}

func TestRealFramesWithATimePastTheEnd(t *testing.T) {
	withUmask(t, 0o007)
	clip := buildClip(t)
	out := filepath.Join(t.TempDir(), "frames.png")
	o := runFrames(&proc.Exec{}, clip, out, "1", "2", "7", "--width", "160")
	if o.code != cli.ExitIncomplete {
		t.Fatalf("exit %d, want 1: %s%s", o.code, o.stdout, o.stderr)
	}
	var rep Report
	decodeReport(t, o, &rep)
	if rep.Width != 160 || rep.Height != 90 || rep.Cols != 3 || rep.Rows != 1 || rep.Errors != 1 {
		t.Fatalf("report = %+v", rep)
	}
	if rep.Tiles[2].Error != "no frame" || rep.Tiles[0].Error != "" || rep.Tiles[1].Error != "" {
		t.Errorf("tiles = %+v", rep.Tiles)
	}

	img := readPNG(t, out)
	if b := img.Bounds(); b.Dx() != 3*160+4 || b.Dy() != 90 {
		t.Fatalf("sheet is %v, want 484x90", b)
	}
	gray := color.RGBA{40, 40, 40, 255}
	if got := color.RGBAModel.Convert(img.At(2*162+150, 80)); got != gray {
		t.Errorf("tile 3 pixel = %v, want the placeholder gray", got)
	}
	if got := color.RGBAModel.Convert(img.At(150, 80)); got == gray {
		t.Error("tile 1 is a placeholder")
	}
	fi, err := os.Stat(out)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o660 {
		t.Errorf("sheet mode = %v, want 0660", fi.Mode().Perm())
	}
}

func TestRealTitlescan(t *testing.T) {
	withUmask(t, 0o007)
	clip := buildClip(t)
	dir := t.TempDir()
	full, banded := filepath.Join(dir, "full.png"), filepath.Join(dir, "band.png")

	o := runTitlescan(&proc.Exec{}, clip, full, "0", "6", "--step", "2", "--width", "160")
	if o.code != cli.ExitOK {
		t.Fatalf("exit %d: %s%s", o.code, o.stdout, o.stderr)
	}
	var rep TitlescanReport
	decodeReport(t, o, &rep)
	if len(rep.Tiles) != 3 || rep.Cols != 2 || rep.Rows != 2 || rep.Errors != 0 || rep.Band != "full" || rep.Height != 90 {
		t.Fatalf("report = %+v", rep)
	}
	if b := readPNG(t, full).Bounds(); b.Dx() != 2*160+2 || b.Dy() != 2*90+2 {
		t.Errorf("sheet is %v, want 322x182", b)
	}

	o = runTitlescan(&proc.Exec{}, clip, banded, "0", "6", "--step", "2", "--width", "160", "--band", "0.5:0.5")
	if o.code != cli.ExitOK {
		t.Fatalf("banded: exit %d: %s%s", o.code, o.stdout, o.stderr)
	}
	var brep TitlescanReport
	decodeReport(t, o, &brep)
	if brep.Band != "0.5:0.5" || brep.Height != 46 || len(brep.Tiles) != 3 || brep.Errors != 0 {
		t.Fatalf("banded report = %+v", brep)
	}
	if b := readPNG(t, banded).Bounds(); b.Dx() != 322 || b.Dy() != 2*46+2 {
		t.Errorf("banded sheet is %v, want 322x94", b)
	}
	a, _ := os.ReadFile(full)
	b, _ := os.ReadFile(banded)
	if bytes.Equal(a, b) {
		t.Error("the band changed nothing")
	}
	fi, err := os.Stat(full)
	if err != nil || fi.Mode().Perm() != 0o660 {
		t.Errorf("sheet mode = %v, %v; want 0660", fi.Mode().Perm(), err)
	}
}

func TestRealTitlescanPastTheEnd(t *testing.T) {
	clip := buildClip(t)
	out := filepath.Join(t.TempDir(), "scan.png")
	o := runTitlescan(&proc.Exec{}, clip, out, "4", "12", "--step", "2", "--width", "160")
	if o.code != cli.ExitIncomplete {
		t.Fatalf("exit %d, want 1: %s%s", o.code, o.stdout, o.stderr)
	}
	var rep TitlescanReport
	decodeReport(t, o, &rep)
	if len(rep.Tiles) != 4 || rep.Errors != 3 || rep.Tiles[0].Error != "" || rep.Tiles[1].Error != "no frame" || rep.Tiles[3].Error != "no frame" {
		t.Errorf("report = %+v", rep)
	}
}
