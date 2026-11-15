package staticcheck

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/reclyptor/mediaops/internal/cli"
	"github.com/reclyptor/mediaops/internal/dhash"
	"github.com/reclyptor/mediaops/kit/proc"
)

type fixture struct {
	Cases  map[string][]string `json:"staticcheck_cases"`
	Stdout string              `json:"staticcheck_stdout"`
}

func loadFixture(t *testing.T) fixture {
	t.Helper()
	b, err := os.ReadFile("../dhash/testdata/python.json")
	if err != nil {
		t.Fatal(err)
	}
	var f fixture
	if err := json.Unmarshal(b, &f); err != nil {
		t.Fatal(err)
	}
	return f
}

func hashesOf(t *testing.T, hex []string) []uint64 {
	t.Helper()
	out := make([]uint64, len(hex))
	for i, s := range hex {
		h, err := dhash.Parse(s)
		if err != nil {
			t.Fatal(err)
		}
		out[i] = h
	}
	return out
}

// frameFor builds a gray frame whose dHash is h: a set bit steps the next
// pixel down, a clear bit repeats the pixel.
func frameFor(h uint64) []byte {
	px := make([]byte, dhash.FrameSize)
	bit := 63
	for row := 0; row < dhash.Height; row++ {
		v := byte(200)
		px[row*dhash.Width] = v
		for col := 1; col < dhash.Width; col++ {
			if h>>bit&1 == 1 {
				v--
			}
			bit--
			px[row*dhash.Width+col] = v
		}
	}
	return px
}

func rawOf(hashes []uint64) []byte {
	var raw []byte
	for _, h := range hashes {
		raw = append(raw, frameFor(h)...)
	}
	return raw
}

func TestFrameForRoundTrips(t *testing.T) {
	for _, h := range []uint64{0, ^uint64(0), 0x55aa55aa55aa55aa, 0x2aac26bbaeb1442b} {
		if got := dhash.Hash(frameFor(h)); got != h {
			t.Errorf("frameFor(%016x) hashes to %016x", h, got)
		}
	}
}

var verdictLine = regexp.MustCompile(`^\s*(\S+)\s+(\S+)\s+(\S+)\s+(STATIC|content|UNREADABLE)\s+(\S+)$`)

func TestClassifyMatchesPython(t *testing.T) {
	f := loadFixture(t)
	seen := 0
	for _, line := range strings.Split(f.Stdout, "\n") {
		m := verdictLine.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		seen++
		name := m[5]
		rep, ok := classify(hashesOf(t, f.Cases[name]))
		if m[4] == "UNREADABLE" {
			if ok {
				t.Errorf("%s classified, Python said unreadable", name)
			}
			continue
		}
		if !ok {
			t.Errorf("%s not classified", name)
			continue
		}
		want := Content
		if m[4] == "STATIC" {
			want = Static
		}
		if got := fmt.Sprintf("%d %d %.2f %s", rep.Frames, rep.Distinct, rep.Ratio, rep.Verdict); got != fmt.Sprintf("%s %s %s %s", m[1], m[2], m[3], want) {
			t.Errorf("%s = %q, Python printed %q", name, got, line)
		}
	}
	if seen != 4 {
		t.Errorf("parsed %d verdict lines, want 4", seen)
	}
}

func TestClassifyBoundaryAndRounding(t *testing.T) {
	// 3 distinct of 20 is exactly 0.15: static. 4 of 26 is 0.1538: content,
	// reported as 0.1538 though it would print 0.15.
	edge := make([]uint64, 20)
	for i := range edge {
		edge[i] = uint64(i % 3)
	}
	if r, _ := classify(edge); r.Verdict != Static || r.Ratio != 0.15 {
		t.Errorf("edge = %+v", r)
	}
	over := make([]uint64, 26)
	for i := range over {
		over[i] = uint64(i % 4)
	}
	if r, _ := classify(over); r.Verdict != Content || r.Ratio != 0.1538 {
		t.Errorf("just over = %+v", r)
	}
	if _, ok := classify(nil); ok {
		t.Error("no hashes classified")
	}
}

// fakePiper serves raw frames per path and records argv through the real guard.
type fakePiper struct {
	t      *testing.T
	frames map[string][]byte
	exits  map[string]int
	calls  atomic.Int32
	gate   func(ctx context.Context, path string) error
}

func (f *fakePiper) Pipe(ctx context.Context, argv []string, consume func(io.Reader) error) ([]byte, int, error) {
	f.t.Helper()
	f.calls.Add(1)
	if err := proc.Check(argv); err != nil {
		f.t.Errorf("guard refused %q: %v", argv, err)
	}
	path := argv[slicesIndex(argv, "-i")+1]
	if f.gate != nil {
		if err := f.gate(ctx, path); err != nil {
			return nil, -1, err
		}
	}
	if err := consume(bytes.NewReader(f.frames[filepath.Base(path)])); err != nil {
		return nil, -1, err
	}
	if code := f.exits[filepath.Base(path)]; code != 0 {
		return []byte("Invalid data found"), code, nil
	}
	return nil, 0, nil
}

func slicesIndex(s []string, v string) int {
	for i, x := range s {
		if x == v {
			return i
		}
	}
	return -1
}

func touch(t *testing.T, names ...string) []string {
	t.Helper()
	dir := t.TempDir()
	paths := make([]string, len(names))
	for i, n := range names {
		paths[i] = filepath.Join(dir, n)
		if err := os.WriteFile(paths[i], []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return paths
}

func run(t *testing.T, p proc.Piper, args ...string) (Report, string, error) {
	t.Helper()
	var out, errb bytes.Buffer
	err := Command(context.Background(), p, args, cli.IO{Stdout: &out, Stderr: &errb})
	var rep Report
	if out.Len() > 0 {
		if jerr := json.Unmarshal(out.Bytes(), &rep); jerr != nil {
			t.Fatalf("stdout is not one JSON value: %v\n%s", jerr, out.String())
		}
	}
	return rep, out.String(), err
}

func TestCommandReportsFixtureVerdicts(t *testing.T) {
	f := loadFixture(t)
	names := []string{"still.mkv", "content.mkv", "edge.mkv"}
	paths := touch(t, names...)
	frames := map[string][]byte{}
	for _, n := range names {
		frames[n] = rawOf(hashesOf(t, f.Cases[n]))
	}
	p := &fakePiper{t: t, frames: frames}
	rep, _, err := run(t, p, append(paths, "--workers", "2")...)
	if err != nil {
		t.Fatal(err)
	}
	want := []FileReport{
		{Path: paths[0], Frames: 53, Distinct: 2, Ratio: 0.0377, Verdict: Static},
		{Path: paths[1], Frames: 50, Distinct: 50, Ratio: 1, Verdict: Content},
		{Path: paths[2], Frames: 20, Distinct: 3, Ratio: 0.15, Verdict: Static},
	}
	if len(rep.Files) != len(want) {
		t.Fatalf("files = %+v", rep.Files)
	}
	for i := range want {
		if rep.Files[i] != want[i] {
			t.Errorf("file %d = %+v, want %+v", i, rep.Files[i], want[i])
		}
	}
	if rep.Static != 2 || rep.Content != 1 || rep.Unreadable != 0 {
		t.Errorf("tallies = %d/%d/%d", rep.Static, rep.Content, rep.Unreadable)
	}
}

func TestCommandUnreadableExitsOne(t *testing.T) {
	paths := touch(t, "empty.mkv", "bad.mkv", "ok.mkv")
	p := &fakePiper{
		t:      t,
		frames: map[string][]byte{"bad.mkv": rawOf([]uint64{1, 2}), "ok.mkv": rawOf([]uint64{1, 2, 3})},
		exits:  map[string]int{"bad.mkv": 1},
	}
	rep, _, err := run(t, p, paths...)
	if code := cli.Code(err); code != cli.ExitIncomplete {
		t.Fatalf("exit = %d (%v), want %d", code, err, cli.ExitIncomplete)
	}
	if rep.Unreadable != 2 || rep.Content != 1 || rep.Static != 0 {
		t.Errorf("tallies = %d/%d/%d", rep.Static, rep.Content, rep.Unreadable)
	}
	if rep.Files[0].Error == "" || rep.Files[0].Verdict != "" {
		t.Errorf("empty file = %+v", rep.Files[0])
	}
	if !strings.Contains(rep.Files[1].Error, "Invalid data found") {
		t.Errorf("failed decode lost its stderr: %+v", rep.Files[1])
	}
	if rep.Files[2].Verdict != Content {
		t.Errorf("a good file next to bad ones = %+v", rep.Files[2])
	}
}

func TestCommandRefusesNonRegularBeforeDecoding(t *testing.T) {
	good := touch(t, "a.mkv")[0]
	for name, arg := range map[string]string{
		"missing":   filepath.Join(t.TempDir(), "nope.mkv"),
		"directory": t.TempDir(),
	} {
		p := &fakePiper{t: t}
		_, out, err := run(t, p, good, arg)
		if code := cli.Code(err); code != cli.ExitUsage {
			t.Errorf("%s: exit = %d, want %d", name, code, cli.ExitUsage)
		}
		if err == nil || !strings.Contains(err.Error(), arg) {
			t.Errorf("%s: error does not name the path: %v", name, err)
		}
		if p.calls.Load() != 0 || out != "" {
			t.Errorf("%s: decoded or printed before refusing", name)
		}
	}
}

func TestCommandUsage(t *testing.T) {
	for _, args := range [][]string{nil, {"--workers", "0", "x"}, {"--bogus", "x"}} {
		_, _, err := run(t, &fakePiper{t: t}, args...)
		if cli.Code(err) != cli.ExitUsage {
			t.Errorf("%q: err = %v", args, err)
		}
	}
}

func TestCommandKeepsArgumentOrderAndBoundsWorkers(t *testing.T) {
	names := make([]string, 12)
	frames := map[string][]byte{}
	for i := range names {
		names[i] = fmt.Sprintf("f%02d.mkv", i)
		frames[names[i]] = rawOf([]uint64{uint64(i), uint64(i + 100)})
	}
	paths := touch(t, names...)
	var mu sync.Mutex
	inflight, peak := 0, 0
	p := &fakePiper{t: t, frames: frames, gate: func(ctx context.Context, _ string) error {
		mu.Lock()
		inflight++
		peak = max(peak, inflight)
		mu.Unlock()
		time.Sleep(10 * time.Millisecond)
		mu.Lock()
		inflight--
		mu.Unlock()
		return nil
	}}
	rep, _, err := run(t, p, append(paths, "--workers", "3")...)
	if err != nil {
		t.Fatal(err)
	}
	for i, r := range rep.Files {
		if r.Path != paths[i] {
			t.Errorf("result %d is %s, want %s", i, r.Path, paths[i])
		}
	}
	if peak > 3 || peak < 2 {
		t.Errorf("peak concurrency = %d with --workers 3", peak)
	}
}

func TestCommandHonoursCancellation(t *testing.T) {
	names := []string{"a.mkv", "b.mkv", "c.mkv", "d.mkv"}
	paths := touch(t, names...)
	ctx, cancel := context.WithCancel(context.Background())
	p := &fakePiper{t: t, gate: func(ctx context.Context, _ string) error {
		cancel()
		<-ctx.Done()
		return ctx.Err()
	}}
	var out bytes.Buffer
	err := Command(ctx, p, append(paths, "--workers", "1"), cli.IO{Stdout: &out})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if out.Len() != 0 {
		t.Errorf("printed a report after cancellation: %s", out.String())
	}
	if n := p.calls.Load(); n != 1 {
		t.Errorf("%d decodes started after cancel, want 1", n)
	}
}
