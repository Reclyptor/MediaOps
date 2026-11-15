package cards

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/reclyptor/mediaops/internal/cli"
	"github.com/reclyptor/mediaops/kit/proc"
)

// sheetTools answers ffprobe from canned JSON and serves raw frames to Pipe,
// running every argv through the real guard.
type sheetTools struct {
	t        *testing.T
	streams  map[string]string // path -> ffprobe -show_streams JSON
	chapters map[string]string // path -> ffprobe -show_chapters JSON
	// pipe returns the raw frames for a decode, given its argv.
	pipe func(argv []string) (stdout []byte, stderr []byte, exit int)

	mu    sync.Mutex
	pipes [][]string
	runs  [][]string
}

func (s *sheetTools) Run(_ context.Context, argv ...string) (proc.Result, error) {
	if err := proc.Check(argv); err != nil {
		s.t.Errorf("guard refused %q: %v", argv, err)
	}
	s.mu.Lock()
	s.runs = append(s.runs, argv)
	s.mu.Unlock()
	path := argv[len(argv)-1]
	var body string
	switch {
	case argv[0] != "ffprobe":
		s.t.Errorf("unexpected run %q", argv)
	case strings.Contains(strings.Join(argv, " "), "-show_chapters"):
		body = s.chapters[path]
	default:
		body = s.streams[path]
	}
	if body == "" {
		return proc.Result{ExitCode: 1, Stderr: []byte("no such file")}, nil
	}
	return proc.Result{Stdout: []byte(body)}, nil
}

func (s *sheetTools) Pipe(_ context.Context, argv []string, consume func(io.Reader) error) ([]byte, int, error) {
	if err := proc.Check(argv); err != nil {
		s.t.Errorf("guard refused %q: %v", argv, err)
	}
	s.mu.Lock()
	s.pipes = append(s.pipes, argv)
	s.mu.Unlock()
	out, errOut, exit := s.pipe(argv)
	return errOut, exit, consume(bytes.NewReader(out))
}

func (s *sheetTools) Stream(context.Context, []string, func(string)) (int, error) {
	panic("not used")
}

// tileBytes is n solid rgb24 frames of w x h, frame i filled with grey(i).
func tileBytes(w, h, n int, grey func(i int) byte) []byte {
	var b []byte
	for i := range n {
		b = append(b, bytes.Repeat([]byte{grey(i)}, w*h*3)...)
	}
	return b
}

// atGrey serves one frame whose grey is the -ss of the decode.
func atGrey(w, h int) func([]string) ([]byte, []byte, int) {
	return func(argv []string) ([]byte, []byte, int) {
		at, _ := strconv.ParseFloat(argAfter(argv, "-ss"), 64)
		return tileBytes(w, h, 1, func(int) byte { return byte(at) }), nil, 0
	}
}

func videoJSON(w, h int) string {
	return fmt.Sprintf(`{"streams":[{"index":0,"codec_type":"video","codec_name":"h264","width":%d,"height":%d,"sample_aspect_ratio":"1:1"}]}`, w, h)
}

func subJSON(codecs ...string) string {
	s := `{"streams":[{"index":0,"codec_type":"video","codec_name":"h264","width":1280,"height":720},{"index":1,"codec_type":"audio","codec_name":"aac"}`
	for i, c := range codecs {
		s += fmt.Sprintf(`,{"index":%d,"codec_type":"subtitle","codec_name":%q}`, i+2, c)
	}
	return s + `]}`
}

func sheetRun(t *testing.T, tools Tools, stdin string, args ...string) (SheetReport, string, int) {
	t.Helper()
	var out, errOut bytes.Buffer
	err := Command(context.Background(), tools, append([]string{"sheet"}, args...), cli.IO{Stdin: strings.NewReader(stdin), Stdout: &out, Stderr: &errOut})
	var rep SheetReport
	if out.Len() > 0 {
		if err := json.Unmarshal(out.Bytes(), &rep); err != nil {
			t.Fatalf("stdout is not a sheet report: %v\n%s", err, out.String())
		}
	}
	msg := ""
	if err != nil {
		msg = err.Error()
	}
	return rep, msg, cli.Code(err)
}

func decodePNG(t *testing.T, path string) image.Image {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	img, err := png.Decode(f)
	if err != nil {
		t.Fatal(err)
	}
	return img
}

func grey(img image.Image, x, y int) int {
	r, _, _, _ := img.At(x, y).RGBA()
	return int(r >> 8)
}

// reportFor writes a find report for the given files as JSON.
func findReportJSON(t *testing.T, files ...FindFile) string {
	t.Helper()
	b, err := json.Marshal(FindReport{Mode: "find", Files: files, Total: len(files)})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func chain(w, h int) string {
	return fmt.Sprintf("scale=trunc(iw*sar/2)*2:ih,setsar=1,scale=%[1]d:%[2]d:force_original_aspect_ratio=decrease,pad=%[1]d:%[2]d:(ow-iw)/2:(oh-ih)/2,format=rgb24", w, h)
}

var chain32x18 = chain(32, 18)

func TestSheetCandidatesDrawsOneTilePerCandidateAndPlaceholdersForTheRest(t *testing.T) {
	a, b, c := touch(t, "Show E01.mkv"), touch(t, "e02.mkv"), touch(t, "e03.mkv")
	best := 20.0
	report := findReportJSON(t,
		FindFile{Path: a, Candidates: []FindCandidate{{At: 10}, {At: 20}}, Best: &best},
		FindFile{Path: b, Candidates: []FindCandidate{}},
		FindFile{Path: c, Error: "ffmpeg exited 1: boom"},
	)
	tools := &sheetTools{t: t, pipe: atGrey(64, 36)}
	out := filepath.Join(t.TempDir(), "cards.png")

	rep, msg, code := sheetRun(t, tools, report, out, "--candidates", "-", "--width", "64")
	if code != cli.ExitOK {
		t.Fatalf("exit %d: %s", code, msg)
	}
	var labels []string
	for _, tile := range rep.Tiles {
		labels = append(labels, tile.Label)
	}
	want := []string{"1 Show E01 10s", "2 Show E01 20s *", "NO CANDIDATE: e02", "ERROR: e03"}
	if !reflect.DeepEqual(labels, want) {
		t.Fatalf("labels = %q, want %q", labels, want)
	}
	if rep.Errors != 0 || rep.Mode != "candidates" || rep.Width != 64 || rep.Height != 36 || rep.Cols != 2 || rep.Rows != 2 {
		t.Errorf("report = %+v", rep)
	}
	if !rep.Tiles[2].Placeholder || rep.Tiles[2].Note == "" || rep.Tiles[2].Error != "" || !rep.Tiles[3].Placeholder || !strings.Contains(rep.Tiles[3].Note, "boom") {
		t.Errorf("placeholders = %+v %+v", rep.Tiles[2], rep.Tiles[3])
	}
	if rep.Tiles[1].At == nil || *rep.Tiles[1].At != 20 || rep.Tiles[1].File != a || rep.Tiles[1].N != 2 {
		t.Errorf("tile 2 = %+v", rep.Tiles[1])
	}

	wantArgv := []string{"ffmpeg", "-nostdin", "-v", "error", "-ss", "10", "-i", a, "-map", "0:v:0", "-frames:v", "1", "-vf", chain(64, 36), "-f", "rawvideo", "-"}
	if len(tools.pipes) != 2 || !reflect.DeepEqual(tools.pipes[0], wantArgv) {
		t.Fatalf("pipes = %q", tools.pipes)
	}

	img := decodePNG(t, out)
	if sz := img.Bounds().Size(); sz != image.Pt(2*64+2, 2*36+2) {
		t.Fatalf("sheet is %v", sz)
	}
	// below the label, the first tile is grey 10 and the second grey 20
	if g := grey(img, 32, 30); g != 10 {
		t.Errorf("tile 1 grey = %d, want 10", g)
	}
	if g := grey(img, 66+32, 30); g != 20 {
		t.Errorf("tile 2 grey = %d, want 20", g)
	}
}

func TestSheetFramePastTheEndIsAPlaceholderAndExit1(t *testing.T) {
	a := touch(t, "e01.mkv")
	tools := &sheetTools{t: t, pipe: func([]string) ([]byte, []byte, int) { return nil, nil, 0 }}
	out := filepath.Join(t.TempDir(), "x.png")
	pts := a + "\t99999\n"
	rep, _, code := sheetRun(t, tools, pts, out, "--points", "-", "--width", "32")
	if code != cli.ExitIncomplete || rep.Errors != 1 {
		t.Fatalf("exit %d, %+v", code, rep)
	}
	if tile := rep.Tiles[0]; !tile.Placeholder || tile.Error != "no frame at 99999s" || tile.Label != "1 e01 99999s" {
		t.Errorf("tile = %+v", tile)
	}
	if _, err := os.Stat(out); err != nil {
		t.Errorf("the sheet was not written: %v", err)
	}
}

func TestSheetDecodeFailureIsAPlaceholderAndExit1(t *testing.T) {
	a := touch(t, "e01.mkv")
	tools := &sheetTools{t: t, pipe: func([]string) ([]byte, []byte, int) { return nil, []byte("Invalid data found"), 183 }}
	rep, _, code := sheetRun(t, tools, a+"\t5\n", filepath.Join(t.TempDir(), "x.png"), "--points", "-", "--width", "32")
	if code != cli.ExitIncomplete || !strings.Contains(rep.Tiles[0].Error, "Invalid data found") {
		t.Fatalf("exit %d, %+v", code, rep.Tiles)
	}
}

func TestSheetPointsLabels(t *testing.T) {
	a, b := touch(t, "a-very-long-name-that-goes-past-twenty-four-runes.mkv"), touch(t, "b.mkv")
	tools := &sheetTools{t: t, pipe: atGrey(32, 18)}
	pts := "\n" + a + "\t12.5\n\n" + b + "\t0\tmy label\r\n"
	rep, msg, code := sheetRun(t, tools, pts, filepath.Join(t.TempDir(), "x.png"), "--points", "-", "--width", "32")
	if code != cli.ExitOK {
		t.Fatalf("exit %d: %s", code, msg)
	}
	if got := rep.Tiles[0].Label; got != "1 a-very-long-name-that-go 12.5s" {
		t.Errorf("label = %q", got)
	}
	if got := rep.Tiles[1].Label; got != "my label" {
		t.Errorf("label = %q", got)
	}
}

func TestSheetPointsFromAFile(t *testing.T) {
	a := touch(t, "a.mkv")
	list := filepath.Join(t.TempDir(), "points.tsv")
	if err := os.WriteFile(list, []byte(a+"\t3\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tools := &sheetTools{t: t, pipe: atGrey(32, 18)}
	rep, msg, code := sheetRun(t, tools, "", filepath.Join(t.TempDir(), "x.png"), "--points", list, "--width", "32")
	if code != cli.ExitOK || len(rep.Tiles) != 1 {
		t.Fatalf("exit %d: %s", code, msg)
	}
}

func TestSheetUsageErrorsDecodeNothingAndWriteNothing(t *testing.T) {
	media := touch(t, "a.mkv")
	dir := t.TempDir()
	out := filepath.Join(dir, "o.png")
	good := media + "\t5\n"
	cases := []struct {
		name  string
		stdin string
		args  []string
	}{
		{"no mode", good, []string{out}},
		{"two modes", good, []string{out, "--points", "-", "--candidates", "-"}},
		{"no OUT", good, []string{"--points", "-"}},
		{"OUT is not a png", good, []string{filepath.Join(dir, "o.jpg"), "--points", "-"}},
		{"OUT is a media name", good, []string{media + ".mkv", "--points", "-"}},
		{"OUT directory missing", good, []string{filepath.Join(dir, "no", "o.png"), "--points", "-"}},
		{"OUT is a directory", good, []string{filepath.Join(dir, "d.png"), "--points", "-"}},
		{"extra positional", good, []string{out, "extra", "--points", "-"}},
		{"offset outside chapters", good, []string{out, "--points", "-", "--offset", "3"}},
		{"subs with seams", "", []string{out, "--seams", media, media, "--subs", "0"}},
		{"cols with seams", "", []string{out, "--seams", media, media, "--cols", "4"}},
		{"one seam part", "", []string{out, "--seams", media}},
		{"odd width", good, []string{out, "--points", "-", "--width", "33"}},
		{"negative cols", good, []string{out, "--points", "-", "--cols", "-1"}},
		{"no points", "\n\n", []string{out, "--points", "-"}},
		{"point with no time", media + "\n", []string{out, "--points", "-"}},
		{"point with a text time", media + "\tsoon\n", []string{out, "--points", "-"}},
		{"point before zero", media + "\t-1\n", []string{out, "--points", "-"}},
		{"point at NaN", media + "\tNaN\n", []string{out, "--points", "-"}},
		{"point at Inf", media + "\tInf\n", []string{out, "--points", "-"}},
		{"point in a directory", dir + "\t1\n", []string{out, "--points", "-"}},
		{"point in a missing file", filepath.Join(dir, "gone.mkv") + "\t1\n", []string{out, "--points", "-"}},
		{"report that is not JSON", "nope", []string{out, "--candidates", "-"}},
		{"report of another mode", `{"mode":"points","files":[]}`, []string{out, "--candidates", "-"}},
		{"empty report", `{"mode":"find","files":[]}`, []string{out, "--candidates", "-"}},
		{"report file is a directory", "", []string{out, "--candidates", dir}},
		{"chapters of a directory", "", []string{out, "--chapters", dir}},
		{"too many tiles", strings.Repeat(good, 601), []string{out, "--points", "-"}},
		{"a sheet over the pixel cap", strings.Repeat(good, 600), []string{out, "--points", "-", "--width", "20000"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if err := os.Mkdir(filepath.Join(dir, "d.png"), 0o755); err != nil && !os.IsExist(err) {
				t.Fatal(err)
			}
			rep, msg, code := sheetRun(t, never{t}, c.stdin, c.args...)
			if code != cli.ExitUsage {
				t.Errorf("exit %d (%s), want 2", code, msg)
			}
			if rep.Out != "" {
				t.Errorf("a report was printed: %+v", rep)
			}
			if _, err := os.Stat(out); err == nil {
				t.Error("a sheet was written")
			}
		})
	}
}

func TestSheetMalformedPointNamesTheLine(t *testing.T) {
	media := touch(t, "a.mkv")
	_, msg, code := sheetRun(t, never{t}, media+"\t1\n"+media+"\tsoon\n", filepath.Join(t.TempDir(), "o.png"), "--points", "-")
	if code != cli.ExitUsage || !strings.Contains(msg, "line 2") {
		t.Fatalf("exit %d: %s", code, msg)
	}
}

func TestSheetSubtitleBurnIn(t *testing.T) {
	a := touch(t, "e01.mkv")
	tools := &sheetTools{t: t, streams: map[string]string{a: subJSON("hdmv_pgs_subtitle", "subrip")}, pipe: atGrey(32, 18)}
	out := filepath.Join(t.TempDir(), "o.png")
	pts := a + "\t10\n"

	rep, msg, code := sheetRun(t, tools, pts, out, "--points", "-", "--width", "32", "--subs", "0")
	if code != cli.ExitOK || len(rep.Tiles) != 1 {
		t.Fatalf("exit %d: %s", code, msg)
	}
	want := []string{"ffmpeg", "-nostdin", "-v", "error", "-ss", "4", "-i", a,
		"-filter_complex", "[0:v:0][0:s:0]overlay," + chain32x18 + "[v]", "-map", "[v]",
		"-ss", "6", "-frames:v", "1", "-f", "rawvideo", "-"}
	if !reflect.DeepEqual(tools.pipes[0], want) {
		t.Errorf("argv = %q\nwant %q", tools.pipes[0], want)
	}

	for _, c := range []struct{ subs, want string }{
		{"1", "subrip"},
		{"2", "no s:2"},
	} {
		tools.pipes = nil
		_, msg, code = sheetRun(t, tools, pts, out, "--points", "-", "--width", "32", "--subs", c.subs)
		if code != cli.ExitUsage || !strings.Contains(msg, c.want) || !strings.Contains(msg, a) || len(tools.pipes) != 0 {
			t.Errorf("--subs %s: exit %d (%s), decodes %d", c.subs, code, msg, len(tools.pipes))
		}
	}
}

func TestSheetSubtitleCodecsThatCanBeBurnedIn(t *testing.T) {
	a := touch(t, "e01.mkv")
	for _, codec := range []string{"hdmv_pgs_subtitle", "dvd_subtitle", "dvb_subtitle"} {
		tools := &sheetTools{t: t, streams: map[string]string{a: subJSON(codec)}, pipe: atGrey(32, 18)}
		_, msg, code := sheetRun(t, tools, a+"\t1\n", filepath.Join(t.TempDir(), "o.png"), "--points", "-", "--width", "32", "--subs", "0")
		if code != cli.ExitOK {
			t.Errorf("%s: exit %d: %s", codec, code, msg)
		}
	}
	for _, codec := range []string{"subrip", "ass", "webvtt", "mov_text"} {
		tools := &sheetTools{t: t, streams: map[string]string{a: subJSON(codec)}, pipe: atGrey(32, 18)}
		_, _, code := sheetRun(t, tools, a+"\t1\n", filepath.Join(t.TempDir(), "o.png"), "--points", "-", "--width", "32", "--subs", "0")
		if code != cli.ExitUsage {
			t.Errorf("%s: exit %d, want 2", codec, code)
		}
	}
}

func TestSheetChapters(t *testing.T) {
	a := touch(t, "e01.mkv")
	chapters := `{"chapters":[
		{"start_time":"0.000000","end_time":"60"},
		{"start_time":"60.04","end_time":"120"},
		{"start_time":"1325.5","end_time":"1400"}]}`
	tools := &sheetTools{t: t, streams: map[string]string{a: videoJSON(1280, 720)}, chapters: map[string]string{a: chapters}, pipe: atGrey(32, 18)}
	out := filepath.Join(t.TempDir(), "o.png")

	rep, msg, code := sheetRun(t, tools, "", out, "--chapters", a, "--width", "32")
	if code != cli.ExitOK {
		t.Fatalf("exit %d: %s", code, msg)
	}
	var labels []string
	var ats []float64
	for _, tile := range rep.Tiles {
		labels = append(labels, tile.Label)
		ats = append(ats, *tile.At)
	}
	if want := []string{"ch1 0.0", "ch2 60.0", "ch3 1325.5"}; !reflect.DeepEqual(labels, want) {
		t.Errorf("labels = %q", labels)
	}
	if want := []float64{3, 63.04, 1328.5}; !reflect.DeepEqual(ats, want) {
		t.Errorf("at = %v, want %v", ats, want)
	}
	if rep.Cols != 3 || rep.Rows != 1 || rep.Height != 18 {
		t.Errorf("report = %+v", rep)
	}

	tools.pipes = nil
	rep, _, _ = sheetRun(t, tools, "", out, "--chapters", a, "--width", "32", "--offset", "0.5", "--cols", "2")
	if rep.Cols != 2 || rep.Rows != 2 || *rep.Tiles[0].At != 0.5 {
		t.Errorf("offset 0.5 cols 2: %+v", rep)
	}

	tools.chapters[a] = `{"chapters":[]}`
	_, msg, code = sheetRun(t, tools, "", out, "--chapters", a, "--width", "32")
	if code != cli.ExitUsage || !strings.Contains(msg, a) || !strings.Contains(msg, "no chapters") {
		t.Errorf("no chapters: exit %d: %s", code, msg)
	}
}

func TestSheetChaptersDefaultToSevenColumns(t *testing.T) {
	a := touch(t, "e01.mkv")
	var ch []string
	for i := range 9 {
		ch = append(ch, fmt.Sprintf(`{"start_time":"%d","end_time":"%d"}`, i*10, i*10+10))
	}
	tools := &sheetTools{t: t, streams: map[string]string{a: videoJSON(1280, 720)},
		chapters: map[string]string{a: `{"chapters":[` + strings.Join(ch, ",") + `]}`}, pipe: atGrey(32, 18)}
	rep, _, _ := sheetRun(t, tools, "", filepath.Join(t.TempDir(), "o.png"), "--chapters", a, "--width", "32")
	if rep.Cols != 7 || rep.Rows != 2 {
		t.Fatalf("%+v", rep)
	}
}

// seamTools serves tail frames (grey = index) with tailN frames and head
// frames with headN frames, keyed by the part's path.
func seamTools(t *testing.T, parts []string, tailN, headN int) *sheetTools {
	t.Helper()
	streams := map[string]string{}
	for _, p := range parts {
		streams[p] = videoJSON(1280, 720)
	}
	return &sheetTools{t: t, streams: streams, pipe: func(argv []string) ([]byte, []byte, int) {
		n := headN
		if argAfter(argv, "-sseof") != "" {
			n = tailN
		}
		return tileBytes(64, 36, n, func(i int) byte { return byte(i * 4) }), nil, 0
	}}
}

func TestSheetSeams(t *testing.T) {
	p1, p2, p3 := touch(t, "d1-001.mkv"), touch(t, "d1-002.mkv"), touch(t, "d1-003.mkv")
	tools := seamTools(t, []string{p1, p2, p3}, 30, 49)
	out := filepath.Join(t.TempDir(), "seams.png")

	rep, msg, code := sheetRun(t, tools, "", out, "--seams", p1, p2, p3, "--width", "64")
	if code != cli.ExitOK {
		t.Fatalf("exit %d: %s", code, msg)
	}
	if rep.Mode != "seams" || rep.Cols != 8 || rep.Rows != 2 || rep.Width != 64 || rep.Height != 36 || len(rep.Tiles) != 16 {
		t.Fatalf("report = %+v", rep)
	}
	var labels, names []string
	for _, tile := range rep.Tiles[:8] {
		labels, names = append(labels, tile.Label), append(names, tile.Frame)
	}
	if want := []string{"p1 last-24", "p1 last-12", "p1 LAST", "p2 f0", "p2 f12", "p2 f24", "p2 f36", "p2 f48"}; !reflect.DeepEqual(labels, want) {
		t.Errorf("labels = %q", labels)
	}
	if want := []string{"last-24", "last-12", "last", "f0", "f12", "f24", "f36", "f48"}; !reflect.DeepEqual(names, want) {
		t.Errorf("frames = %q", names)
	}
	if rep.Tiles[8].Label != "p2 last-24" || rep.Tiles[8].File != p2 || rep.Tiles[11].File != p3 {
		t.Errorf("second row = %+v", rep.Tiles[8:])
	}

	wantTail := []string{"ffmpeg", "-nostdin", "-v", "error", "-sseof", "-6", "-i", p1, "-map", "0:v:0",
		"-vf", "scale=trunc(iw*sar/2)*2:ih,setsar=1,scale=64:36:force_original_aspect_ratio=decrease,pad=64:36:(ow-iw)/2:(oh-ih)/2,format=rgb24", "-f", "rawvideo", "-"}
	if !reflect.DeepEqual(tools.pipes[0], wantTail) {
		t.Errorf("tail argv = %q", tools.pipes[0])
	}
	if got := strings.Join(tools.pipes[1], " "); !strings.Contains(got, "-i "+p2+" -map 0:v:0 -frames:v 49 -vf") || strings.Contains(got, "-sseof") {
		t.Errorf("head argv = %s", got)
	}

	// tail frames are 30 long with grey i*4: the last 25 are 5..29, so last-24
	// is frame 5, last-12 frame 17 and LAST frame 29; the head tiles are
	// frames 0, 12, 24, 36 and 48
	img := decodePNG(t, out)
	if sz := img.Bounds().Size(); sz != image.Pt(8*64+7*2, 2*36+2) {
		t.Fatalf("sheet is %v", sz)
	}
	for c, want := range []int{20, 68, 116, 0, 48, 96, 144, 192} {
		if got := grey(img, c*66+32, 30); got != want {
			t.Errorf("tile %d grey = %d, want %d", c+1, got, want)
		}
	}
}

func TestSheetSeamsShortPartsLeavePlaceholders(t *testing.T) {
	p1, p2 := touch(t, "d1-001.mkv"), touch(t, "d1-002.mkv")
	// 20 tail frames: last-24 does not exist; 30 head frames: f36 and f48 do not
	tools := seamTools(t, []string{p1, p2}, 20, 30)
	rep, _, code := sheetRun(t, tools, "", filepath.Join(t.TempDir(), "s.png"), "--seams", p1, p2, "--width", "64")
	if code != cli.ExitIncomplete || rep.Errors != 3 {
		t.Fatalf("exit %d, errors %d", code, rep.Errors)
	}
	var bad []string
	for _, tile := range rep.Tiles {
		if tile.Placeholder {
			bad = append(bad, tile.Frame)
			if tile.Error == "" {
				t.Errorf("placeholder without a reason: %+v", tile)
			}
		}
	}
	if want := []string{"last-24", "f36", "f48"}; !reflect.DeepEqual(bad, want) {
		t.Errorf("placeholders = %q, want %q", bad, want)
	}
}

func TestSheetSeamsDecodeFailureFailsItsTiles(t *testing.T) {
	p1, p2 := touch(t, "d1-001.mkv"), touch(t, "d1-002.mkv")
	tools := seamTools(t, []string{p1, p2}, 30, 49)
	inner := tools.pipe
	tools.pipe = func(argv []string) ([]byte, []byte, int) {
		if argAfter(argv, "-sseof") != "" {
			return nil, []byte("Invalid data"), 183
		}
		return inner(argv)
	}
	rep, _, code := sheetRun(t, tools, "", filepath.Join(t.TempDir(), "s.png"), "--seams", p1, p2, "--width", "64")
	if code != cli.ExitIncomplete || rep.Errors != 3 || !strings.Contains(rep.Tiles[0].Error, "Invalid data") {
		t.Fatalf("exit %d, %+v", code, rep)
	}
}

func TestSheetWritesNothingWhenOutIsUnwritable(t *testing.T) {
	a := touch(t, "a.mkv")
	tools := &sheetTools{t: t, pipe: atGrey(32, 18)}
	out := filepath.Join(t.TempDir(), "o.png")
	if err := os.Mkdir(out, 0o755); err != nil {
		t.Fatal(err)
	}
	_, _, code := sheetRun(t, tools, a+"\t1\n", out, "--points", "-", "--width", "32")
	if code != cli.ExitUsage || len(tools.pipes) != 0 {
		t.Fatalf("exit %d, decodes %d", code, len(tools.pipes))
	}
}

func TestReadLocatedMarksTheFirstBestOnly(t *testing.T) {
	a := touch(t, "e01.mkv")
	best := 10.0
	report := findReportJSON(t, FindFile{Path: a, Candidates: []FindCandidate{{At: 10}, {At: 10}}, Best: &best})
	rep, _, _ := sheetRun(t, &sheetTools{t: t, pipe: atGrey(32, 18)}, report, filepath.Join(t.TempDir(), "o.png"), "--candidates", "-", "--width", "32")
	if rep.Tiles[0].Label != "1 e01 10s *" || rep.Tiles[1].Label != "2 e01 10s" {
		t.Fatalf("labels = %q, %q", rep.Tiles[0].Label, rep.Tiles[1].Label)
	}
}
