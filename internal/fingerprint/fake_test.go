package fingerprint

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/reclyptor/mediaops/internal/dhash"
	"github.com/reclyptor/mediaops/kit/proc"
)

var (
	framesOnce sync.Once
	framesRaw  []byte
)

// grayFrames returns the dhash parity fixture's raw 9x8 frames.
func grayFrames(t testing.TB) []byte {
	t.Helper()
	framesOnce.Do(func() {
		b, err := os.ReadFile(filepath.Join("..", "dhash", "testdata", "frames.gray"))
		if err != nil {
			t.Fatal(err)
		}
		framesRaw = b
	})
	if framesRaw == nil {
		t.Fatal("frames.gray was not loaded")
	}
	return framesRaw
}

// fakePiper stands in for ffmpeg: it runs every argv through the real guard
// and answers a decode with a few fixture frames chosen by the file's name,
// so different files hash differently and the same file always the same.
type fakePiper struct {
	t   testing.TB
	raw []byte

	// onCall, when set, runs at the start of every decode, before any bytes
	// are produced. A non-nil error ends the decode with it.
	onCall func(path string, preview bool) error
	// failing maps a path to the stderr of an ffmpeg that exits 1 on it.
	failing map[string]string
	// silent paths decode to no frames at all; silentPreview ones only for the
	// preview pass.
	silent        map[string]bool
	silentPreview map[string]bool

	mu    sync.Mutex
	calls []string
}

func newFake(t testing.TB) *fakePiper {
	return &fakePiper{t: t, raw: grayFrames(t)}
}

func (f *fakePiper) Pipe(ctx context.Context, argv []string, consume func(io.Reader) error) ([]byte, int, error) {
	f.t.Helper()
	if err := proc.Check(argv); err != nil {
		f.t.Errorf("guard refused %q: %v", argv, err)
	}
	var path string
	preview := false
	for i, a := range argv {
		switch a {
		case "-i":
			path = argv[i+1]
		case "-ss":
			preview = true
		}
	}
	f.mu.Lock()
	f.calls = append(f.calls, path)
	f.mu.Unlock()

	if err := ctx.Err(); err != nil {
		return nil, -1, err
	}
	if f.onCall != nil {
		if err := f.onCall(path, preview); err != nil {
			return nil, -1, err
		}
	}
	if msg, ok := f.failing[path]; ok {
		return []byte(msg), 1, nil
	}
	var out []byte
	if !f.silent[path] && !(preview && f.silentPreview[path]) {
		out = f.framesFor(path, preview)
	}
	if err := consume(bytes.NewReader(out)); err != nil {
		return nil, 0, err
	}
	return nil, 0, nil
}

func (f *fakePiper) framesFor(path string, preview bool) []byte {
	sum := 0
	for _, c := range []byte(path) {
		sum += int(c)
	}
	off := 7 + sum%35 // past the edge-case frames, room for 13 more
	n := 12
	if preview {
		n = 6
		off++
	}
	return f.raw[off*dhash.FrameSize : (off+n)*dhash.FrameSize]
}

func (f *fakePiper) decodes() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

// media makes the named regular files in dir and returns their paths.
func media(t testing.TB, dir string, names ...string) []string {
	t.Helper()
	var out []string
	for _, n := range names {
		p := filepath.Join(dir, n)
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		out = append(out, p)
	}
	return out
}
