//go:build integration

package cards

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"image"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reclyptor/mediaops/internal/cli"
	"github.com/reclyptor/mediaops/kit/proc"
)

// These run the real ffmpeg through proc.Exec; a missing binary fails them
// rather than skipping, because a skipped proof is a proof nobody ran.

// buildClip writes a 20 s 320x180 clip: 6 s of moving testsrc2, a 5 s held
// red card with a white box, 5 s moving again, and a 4 s card that is still
// held when the file ends. The card is 6..11 s and 16..20 s.
//
// This is the only place in the package where an encoder runs: the clip is
// built from lavfi sources, never from the toolset.
func buildClip(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Fatalf("ffmpeg is required to run these tests: %v", err)
	}
	card := func(d string) string {
		return "color=c=0xB01818:s=320x180:r=25:d=" + d + ",drawbox=x=100:y=70:w=120:h=40:color=white:t=fill"
	}
	path := filepath.Join(t.TempDir(), "clip.mkv")
	out, err := exec.Command("ffmpeg", "-nostdin", "-v", "error",
		"-f", "lavfi", "-i", "testsrc2=d=6:s=320x180:r=25",
		"-f", "lavfi", "-i", card("5"),
		"-f", "lavfi", "-i", "testsrc2=d=5:s=320x180:r=25",
		"-f", "lavfi", "-i", card("4"),
		"-filter_complex", "[0][1][2][3]concat=n=4:v=1:a=0",
		"-c:v", "libx264", "-preset", "ultrafast", "-pix_fmt", "yuv420p", "-g", "25", path).CombinedOutput()
	if err != nil {
		t.Fatalf("building the clip: %v\n%s", err, out)
	}
	return path
}

func inCard(t0 float64) bool { return (t0 >= 6 && t0 < 11) || (t0 >= 16 && t0 < 20) }

func TestRealFindLocatesTheHeldCard(t *testing.T) {
	clip := buildClip(t)
	for _, extra := range [][]string{nil, {"--static", "--tol", "0.5"}} {
		args := append([]string{"find", clip, "--lo", "0", "--hi", "20", "--minv", "170", "--maxu", "115"}, extra...)
		var rep FindReport
		out, code := run(t, &proc.Exec{}, args...)
		if err := jsonInto(out, &rep); err != nil {
			t.Fatalf("%v\n%s", err, out)
		}
		f := rep.Files[0]
		if code != cli.ExitOK || f.Error != "" || f.Samples != 20 || len(f.Candidates) != 2 {
			t.Fatalf("%v: exit %d, %+v\n%s", extra, code, f, out)
		}
		for _, c := range f.Candidates {
			if !inCard(c.Start) || !inCard(c.End) || !inCard(c.At) {
				t.Errorf("%v: candidate %+v is outside the card", extra, c)
			}
		}
		if first := f.Candidates[0]; first.Start != 6 || first.End != 10 || first.Frames != 5 || first.At != 8 {
			t.Errorf("%v: first candidate = %+v", extra, first)
		}
		if f.Best == nil || !inCard(*f.Best) {
			t.Errorf("%v: best = %v", extra, f.Best)
		}
	}
}

func TestRealFindColourFlagsExcludeMovingFootage(t *testing.T) {
	clip := buildClip(t)
	// the moving testsrc2 averages V~127, so these bounds hold nothing there
	var rep FindReport
	out, code := run(t, &proc.Exec{}, "find", clip, "--lo", "0", "--hi", "6", "--minv", "170")
	if err := jsonInto(out, &rep); err != nil {
		t.Fatal(err)
	}
	if code != cli.ExitIncomplete || rep.Files[0].Samples != 6 || len(rep.Files[0].Candidates) != 0 {
		t.Fatalf("exit %d, %+v", code, rep.Files[0])
	}
}

func TestRealFreezeLocatesTheCardAndNotesTheOpenOne(t *testing.T) {
	clip := buildClip(t)
	var rep FreezeReport
	out, code := run(t, &proc.Exec{}, "freeze", clip, "--lo", "0", "--hi", "20", "--minv", "170", "--maxu", "115", "--minymax", "200")
	if err := jsonInto(out, &rep); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	f := rep.Files[0]
	if code != cli.ExitOK || f.Error != "" || len(f.Candidates) != 1 {
		t.Fatalf("exit %d, %+v\n%s", code, f, out)
	}
	c := f.Candidates[0]
	if c.Start < 5.9 || c.Start > 6.1 || c.At < 8 || c.At > 9 || c.YMax < 230 || c.VAvg < 180 {
		t.Errorf("candidate = %+v", c)
	}
	if f.Best == nil || *f.Best != c.At {
		t.Errorf("best = %v", f.Best)
	}
	if len(f.Notes) != 1 {
		t.Fatalf("the card still held at the end of the window should be a note: %+v", f)
	}

	// With the window ending inside the first card there is no completed
	// freeze at all, only the note.
	out, code = run(t, &proc.Exec{}, "freeze", clip, "--lo", "0", "--hi", "9", "--minv", "170")
	if err := jsonInto(out, &rep); err != nil {
		t.Fatal(err)
	}
	if code != cli.ExitIncomplete || len(rep.Files[0].Candidates) != 0 || len(rep.Files[0].Notes) != 1 {
		t.Fatalf("exit %d, %+v", code, rep.Files[0])
	}
}

func TestRealFailureIsTheFilesError(t *testing.T) {
	clip := buildClip(t)
	bad := filepath.Join(filepath.Dir(clip), "bad.mkv")
	whole, err := os.ReadFile(clip)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bad, whole[:40], 0o644); err != nil {
		t.Fatal(err)
	}
	var rep FindReport
	out, code := run(t, &proc.Exec{}, "find", bad, "--lo", "0", "--hi", "5")
	if err := jsonInto(out, &rep); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if code != cli.ExitIncomplete || rep.Files[0].Error == "" {
		t.Fatalf("exit %d, %+v", code, rep.Files[0])
	}
}

// synth runs a tool directly to build test media; like buildClip it is the
// only kind of place an encoder runs.
func synth(t *testing.T, name string, args ...string) {
	t.Helper()
	if _, err := exec.LookPath(name); err != nil {
		t.Fatalf("%s is required to run these tests: %v", name, err)
	}
	out, err := exec.Command(name, args...).CombinedOutput()
	if ee, ok := err.(*exec.ExitError); ok && ee.ExitCode() == 1 && strings.HasPrefix(name, "mkv") {
		err = nil // mkvtoolnix exits 1 for warnings
	}
	if err != nil {
		t.Fatalf("%s %q: %v\n%s", name, args, err, out)
	}
}

func realSheet(t *testing.T, stdin string, args ...string) (SheetReport, string, int) {
	t.Helper()
	var out, errOut bytes.Buffer
	err := Command(context.Background(), &proc.Exec{}, append([]string{"sheet"}, args...), cli.IO{Stdin: strings.NewReader(stdin), Stdout: &out, Stderr: &errOut})
	var rep SheetReport
	if out.Len() > 0 {
		if err := jsonInto(out.String(), &rep); err != nil {
			t.Fatalf("%v\n%s", err, out.String())
		}
	}
	msg := ""
	if err != nil {
		msg = err.Error()
	}
	return rep, msg, cli.Code(err)
}

// pngSize decodes a written sheet, which also proves it is a valid PNG.
func pngSize(t *testing.T, path string) image.Point {
	t.Helper()
	return decodePNG(t, path).Bounds().Size()
}

func TestRealSheetOfFindCandidates(t *testing.T) {
	clip := buildClip(t)
	found, code := run(t, &proc.Exec{}, "find", clip, "--lo", "0", "--hi", "20", "--minv", "170", "--maxu", "115")
	if code != cli.ExitOK {
		t.Fatalf("find exit %d\n%s", code, found)
	}
	out := filepath.Join(t.TempDir(), "cards.png")
	rep, msg, code := realSheet(t, found, out, "--candidates", "-", "--width", "64")
	if code != cli.ExitOK {
		t.Fatalf("exit %d: %s", code, msg)
	}
	if len(rep.Tiles) != 2 || rep.Errors != 0 || rep.Cols != 2 || rep.Rows != 1 || rep.Tiles[0].Placeholder {
		t.Fatalf("%+v", rep)
	}
	if got := pngSize(t, out); got != image.Pt(2*64+2, 36) {
		t.Errorf("sheet is %v", got)
	}
	// the first tile is the red card, V high and U low
	img := decodePNG(t, out)
	r, g, b, _ := img.At(4, 30).RGBA()
	if r>>8 < 120 || g>>8 > 60 || b>>8 > 60 {
		t.Errorf("tile 1 corner = %d %d %d, want the card's red", r>>8, g>>8, b>>8)
	}
}

func TestRealSheetOfChapters(t *testing.T) {
	dir := t.TempDir()
	meta := filepath.Join(dir, "meta.txt")
	chapters := ";FFMETADATA1\n"
	for _, c := range [][2]int{{0, 4000}, {4000, 8000}, {8000, 12000}} {
		chapters += fmt.Sprintf("[CHAPTER]\nTIMEBASE=1/1000\nSTART=%d\nEND=%d\ntitle=c%d\n", c[0], c[1], c[0])
	}
	if err := os.WriteFile(meta, []byte(chapters), 0o644); err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(dir, "ch.mkv")
	synth(t, "ffmpeg", "-nostdin", "-v", "error", "-f", "lavfi", "-i", "testsrc2=d=12:s=320x180:r=25", "-i", meta,
		"-map", "0:v", "-map_metadata", "1", "-c:v", "libx264", "-preset", "ultrafast", "-pix_fmt", "yuv420p", "-g", "25", src)

	out := filepath.Join(dir, "chap.png")
	rep, msg, code := realSheet(t, "", out, "--chapters", src, "--width", "64")
	if code != cli.ExitOK {
		t.Fatalf("exit %d: %s", code, msg)
	}
	if len(rep.Tiles) != 3 || rep.Cols != 3 || rep.Tiles[1].Label != "ch2 4.0" || *rep.Tiles[1].At != 7 {
		t.Fatalf("%+v", rep)
	}
	if got := pngSize(t, out); got != image.Pt(3*64+4, 36) {
		t.Errorf("sheet is %v", got)
	}

	plain := buildClip(t)
	if _, msg, code := realSheet(t, "", out, "--chapters", plain, "--width", "64"); code != cli.ExitUsage || !strings.Contains(msg, "no chapters") {
		t.Errorf("a file with no chapters: exit %d: %s", code, msg)
	}
}

func TestRealSheetOfSeams(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "whole.mkv")
	synth(t, "ffmpeg", "-nostdin", "-v", "error", "-f", "lavfi", "-i", "testsrc2=d=12:s=320x180:r=25",
		"-c:v", "libx264", "-preset", "ultrafast", "-pix_fmt", "yuv420p", "-g", "25", src)
	synth(t, "mkvmerge", "-o", filepath.Join(dir, "part.mkv"), "--split", "timestamps:00:00:04,00:00:08", src)
	parts := []string{filepath.Join(dir, "part-001.mkv"), filepath.Join(dir, "part-002.mkv"), filepath.Join(dir, "part-003.mkv")}

	out := filepath.Join(dir, "seams.png")
	rep, msg, code := realSheet(t, "", append([]string{out, "--seams", "--width", "64"}, parts...)...)
	if code != cli.ExitOK {
		t.Fatalf("exit %d: %s", code, msg)
	}
	if len(rep.Tiles) != 16 || rep.Cols != 8 || rep.Rows != 2 || rep.Errors != 0 {
		t.Fatalf("%+v", rep)
	}
	if got := pngSize(t, out); got != image.Pt(8*64+14, 2*36+2) {
		t.Errorf("sheet is %v", got)
	}
	for _, tile := range rep.Tiles {
		if tile.Placeholder {
			t.Errorf("placeholder %+v", tile)
		}
	}
}

// vobsubIndex writes a VobSub index and sub file with one white 160x40 block
// at (20,100) from start for about two seconds.
func vobsubIndex(t *testing.T, dir string, start float64) string {
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

func TestRealSheetBurnsInABitmapSubtitle(t *testing.T) {
	dir := t.TempDir()
	idx := vobsubIndex(t, dir, 7)
	src := filepath.Join(dir, "subs.mkv")
	synth(t, "ffmpeg", "-nostdin", "-v", "error", "-f", "lavfi", "-i", "testsrc2=duration=10:size=320x180:rate=10",
		"-i", idx, "-map", "0:v", "-map", "1:s", "-c:v", "libx264", "-preset", "ultrafast", "-pix_fmt", "yuv420p", "-c:s", "copy", src)

	points := src + "\t8\n"
	plain, burned := filepath.Join(dir, "plain.png"), filepath.Join(dir, "burned.png")
	if _, msg, code := realSheet(t, points, plain, "--points", "-", "--width", "320"); code != cli.ExitOK {
		t.Fatalf("plain: exit %d: %s", code, msg)
	}
	if _, msg, code := realSheet(t, points, burned, "--points", "-", "--width", "320", "--subs", "0"); code != cli.ExitOK {
		t.Fatalf("burned: exit %d: %s", code, msg)
	}
	white := func(path string) bool {
		r, g, b, _ := decodePNG(t, path).At(100, 120).RGBA()
		return r>>8 > 240 && g>>8 > 240 && b>>8 > 240
	}
	if !white(burned) || white(plain) {
		t.Errorf("the block pixel: burned %v, plain %v", white(burned), white(plain))
	}

	if _, msg, code := realSheet(t, points, burned, "--points", "-", "--width", "320", "--subs", "1"); code != cli.ExitUsage || !strings.Contains(msg, "s:1") {
		t.Errorf("--subs 1: exit %d: %s", code, msg)
	}
}

func TestRealSheetPlaceholderForAFrameThatIsNotThere(t *testing.T) {
	clip := buildClip(t)
	out := filepath.Join(t.TempDir(), "x.png")
	rep, _, code := realSheet(t, clip+"\t8\n"+clip+"\t99999\n", out, "--points", "-", "--width", "64")
	if code != cli.ExitIncomplete || rep.Errors != 1 || rep.Tiles[0].Placeholder || !rep.Tiles[1].Placeholder {
		t.Fatalf("exit %d, %+v", code, rep)
	}
	if got := pngSize(t, out); got != image.Pt(2*64+2, 36) {
		t.Errorf("sheet is %v", got)
	}
}
