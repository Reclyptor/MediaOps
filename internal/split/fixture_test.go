package split

import (
	"bytes"
	"context"
	"crypto/sha256"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/reclyptor/mediaops/internal/cli"
	"github.com/reclyptor/mediaops/kit/proc"
)

// fixtureRoot holds the one source MKV every test splits. It is built once
// per test binary run and removed by TestMain.
var (
	fixtureRoot string
	fixtureOnce sync.Once
	fixtureSrc  string
	fixtureErr  string
)

func TestMain(m *testing.M) {
	root, err := os.MkdirTemp("", "mkv-split-test-")
	if err != nil {
		panic(err)
	}
	fixtureRoot = root
	code := m.Run()
	os.RemoveAll(root)
	os.Exit(code)
}

// synth runs a tool directly to build test data. This is the only encoder in
// the package's tests, as in kit/mkvtool/integration_test.go: the source needs
// lavfi test sources encoded into it, and nothing outside _test files can
// reach it. Every other helper here stream-copies. mkvtoolnix exits 1 for
// warnings.
func synth(t testing.TB, name string, args ...string) {
	t.Helper()
	out, err := exec.Command(name, args...).CombinedOutput()
	if ee, ok := err.(*exec.ExitError); ok && ee.ExitCode() == 1 && strings.HasPrefix(name, "mkv") {
		err = nil
	}
	if err != nil {
		t.Fatalf("%s %q: %v\n%s", name, args, err, out)
	}
}

const chapterMeta = `;FFMETADATA1
[CHAPTER]
TIMEBASE=1/1000
START=0
END=5000
title=One
[CHAPTER]
TIMEBASE=1/1000
START=5000
END=10000
title=Two
[CHAPTER]
TIMEBASE=1/1000
START=10000
END=15000
title=Three
[CHAPTER]
TIMEBASE=1/1000
START=15000
END=20000
title=Four
`

const cues = `1
00:00:01,000 --> 00:00:03,000
One

2
00:00:06,000 --> 00:00:08,000
Two

3
00:00:11,000 --> 00:00:13,000
Three

4
00:00:16,000 --> 00:00:18,000
Four
`

// source returns a 20 second MKV: H.264 with AUD NALs and a closed GOP every
// second exactly (24 fps), PCM audio, SRT cues, and a chapter every 5
// seconds. Its keyframes are at whole seconds and its chapters on them. mkvmerge
// re-frames the PCM, so verify must compare it as bytes.
func source(t testing.TB) string {
	t.Helper()
	fixtureOnce.Do(func() {
		dir := filepath.Join(fixtureRoot, "source")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			fixtureErr = err.Error()
			return
		}
		meta, srt, out := filepath.Join(dir, "chapters.ffmeta"), filepath.Join(dir, "cues.srt"), filepath.Join(dir, "source.mkv")
		if err := os.WriteFile(meta, []byte(chapterMeta), 0o644); err != nil {
			fixtureErr = err.Error()
			return
		}
		if err := os.WriteFile(srt, []byte(cues), 0o644); err != nil {
			fixtureErr = err.Error()
			return
		}
		cmd := exec.Command("ffmpeg", "-nostdin", "-v", "error", "-y",
			"-f", "lavfi", "-i", "testsrc2=size=640x360:rate=24:duration=20",
			"-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000:duration=20",
			"-i", srt, "-i", meta,
			"-map", "0:v", "-map", "1:a", "-map", "2:s", "-map_chapters", "3",
			"-c:v", "libx264", "-preset", "ultrafast", "-crf", "8", "-pix_fmt", "yuv420p",
			"-x264-params", "aud=1:keyint=24:min-keyint=24:scenecut=0",
			"-c:a", "pcm_s16le", "-ac", "2", "-c:s", "srt", out)
		if b, err := cmd.CombinedOutput(); err != nil {
			fixtureErr = "ffmpeg: " + err.Error() + "\n" + string(b)
			return
		}
		fixtureSrc = out
	})
	if fixtureErr != "" {
		t.Fatalf("building the source fixture (ffmpeg and mkvtoolnix must be installed): %s", fixtureErr)
	}
	return fixtureSrc
}

// recorder is the real proc.Exec that remembers every argv it was asked to run.
type recorder struct {
	*proc.Exec
	mu    sync.Mutex
	argvs [][]string
}

func newRecorder() *recorder { return &recorder{Exec: &proc.Exec{}} }

func (r *recorder) Run(ctx context.Context, argv ...string) (proc.Result, error) {
	r.mu.Lock()
	r.argvs = append(r.argvs, append([]string(nil), argv...))
	r.mu.Unlock()
	return r.Exec.Run(ctx, argv...)
}

// mkvmergeArgv returns the one mkvmerge command that was run.
func (r *recorder) mkvmergeArgv(t testing.TB) []string {
	t.Helper()
	var found [][]string
	for _, a := range r.argvs {
		if a[0] == "mkvmerge" {
			found = append(found, a)
		}
	}
	if len(found) != 1 {
		t.Fatalf("mkvmerge ran %d times, want once: %q", len(found), found)
	}
	return found[0]
}

// runCLI runs mkv-split's Command as the process would and returns its exit
// code, stdout and stderr.
func runCLI(t testing.TB, ex Tools, args ...string) (int, string, string) {
	t.Helper()
	var out, errb bytes.Buffer
	code := cli.Run(context.Background(), "mkv-split", args, cli.IO{Stdout: &out, Stderr: &errb},
		func(ctx context.Context, args []string, streams cli.IO) error { return Command(ctx, ex, args, streams) })
	return code, out.String(), errb.String()
}

// fingerprint is the source's size, mtime and content hash.
type fingerprint struct {
	size  int64
	mtime int64
	sum   [32]byte
}

func fingerprintOf(t testing.TB, path string) fingerprint {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return fingerprint{info.Size(), info.ModTime().UnixNano(), sha256.Sum256(b)}
}

// dirNames lists the entries of dir.
func dirNames(t testing.TB, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}
