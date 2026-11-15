package sheet

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/png"
	"io"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/reclyptor/mediaops/internal/cli"
	"github.com/reclyptor/mediaops/kit/proc"
)

// fakeExec answers ffprobe with one video stream and ffmpeg with solid-colour
// frames for a video that is length seconds long. Every argv goes through the
// real guard.
type fakeExec struct {
	t *testing.T
	// width and height are the probed size of the video.
	width, height int
	length        float64
	// extra is how many frames beyond the window a decode would still offer.
	extra int
	// probeExit and exit make ffprobe and ffmpeg fail.
	probeExit, exit int

	probes [][]string
	pipes  [][]string
}

func newFake(t *testing.T, length float64) *fakeExec {
	return &fakeExec{t: t, width: 1920, height: 1080, length: length}
}

func (f *fakeExec) Run(_ context.Context, argv ...string) (proc.Result, error) {
	f.t.Helper()
	if err := proc.Check(argv); err != nil {
		f.t.Errorf("guard refused %q: %v", argv, err)
	}
	f.probes = append(f.probes, argv)
	if f.probeExit != 0 {
		return proc.Result{ExitCode: f.probeExit, Stderr: []byte("moov atom not found")}, nil
	}
	body := `{"streams": [{"index": 0, "codec_type": "video", "width": ` + strconv.Itoa(f.width) +
		`, "height": ` + strconv.Itoa(f.height) + `, "sample_aspect_ratio": "1:1"}]}`
	return proc.Result{Stdout: []byte(body)}, nil
}

func (f *fakeExec) Stream(context.Context, []string, func(string)) (int, error) {
	panic("not used")
}

var padRE = regexp.MustCompile(`pad=(\d+):(\d+):`)

func (f *fakeExec) Pipe(_ context.Context, argv []string, consume func(io.Reader) error) ([]byte, int, error) {
	f.t.Helper()
	if err := proc.Check(argv); err != nil {
		f.t.Errorf("guard refused %q: %v", argv, err)
	}
	f.pipes = append(f.pipes, argv)
	vf := argAfter(argv, "-vf")
	m := padRE.FindStringSubmatch(vf)
	if m == nil {
		f.t.Fatalf("no pad in %q", vf)
	}
	w, _ := strconv.Atoi(m[1])
	h, _ := strconv.Atoi(m[2])
	ss := mustFloat(f.t, argAfter(argv, "-ss"))

	count := 0
	if strings.HasPrefix(vf, "fps=1/") {
		step := mustFloat(f.t, strings.SplitN(strings.TrimPrefix(vf, "fps=1/"), ",", 2)[0])
		window := math.Min(mustFloat(f.t, argAfter(argv, "-t")), f.length-ss)
		if window > 0 {
			count = int(math.Ceil(window/step-1e-9)) + f.extra
		}
	} else if ss < f.length {
		count = 1
	}
	var out bytes.Buffer
	for i := 0; i < count; i++ {
		out.Write(bytes.Repeat([]byte{byte(100 + 20*i), 90, 200}, w*h))
	}
	err := consume(&out)
	return []byte("fake stderr"), f.exit, err
}

func argAfter(argv []string, flag string) string {
	for i, a := range argv {
		if a == flag && i+1 < len(argv) {
			return argv[i+1]
		}
	}
	return ""
}

func mustFloat(t *testing.T, s string) float64 {
	t.Helper()
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		t.Fatalf("%q is not a number", s)
	}
	return v
}

// touch makes an empty regular file named name in a fresh directory.
func touch(t *testing.T, name string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

type outcome struct {
	stdout, stderr string
	code           int
}

func runFrames(ex Exec, args ...string) outcome {
	return runCmd(FramesCommand, ex, args)
}

func runTitlescan(ex Exec, args ...string) outcome {
	return runCmd(TitlescanCommand, ex, args)
}

func runCmd(cmd func(context.Context, Exec, []string, cli.IO) error, ex Exec, args []string) outcome {
	var stdout, stderr bytes.Buffer
	code := cli.Run(context.Background(), "tool", args, cli.IO{Stdout: &stdout, Stderr: &stderr},
		func(ctx context.Context, args []string, streams cli.IO) error { return cmd(ctx, ex, args, streams) })
	return outcome{stdout.String(), stderr.String(), code}
}

func decodeReport(t *testing.T, o outcome, v any) {
	t.Helper()
	if err := json.Unmarshal([]byte(o.stdout), v); err != nil {
		t.Fatalf("stdout is not the report: %v\n%s", err, o.stdout)
	}
}

func readPNG(t *testing.T, path string) image.Image {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	img, err := png.Decode(f)
	if err != nil {
		t.Fatalf("%s is not a PNG: %v", path, err)
	}
	return img
}
