package audit

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/reclyptor/mediaops/internal/cli"
	"github.com/reclyptor/mediaops/internal/inventory"
	"github.com/reclyptor/mediaops/kit/paths"
	"github.com/reclyptor/mediaops/kit/proc"
)

type fakeStream struct {
	Type, Codec, Lang, Title string
	Channels, Width, Height  int
	Default                  bool
}

// fakeMedia is what the fake ffprobe knows about one file.
type fakeMedia struct {
	Title    string
	Duration float64
	Streams  []fakeStream
	// Warn is what ffprobe prints on stderr while parsing the container.
	Warn []string
	// Fail makes every ffprobe call on the file exit 1.
	Fail bool
}

// goodMedia is a correctly prepared episode: Japanese audio default, English
// full subtitles default.
func goodMedia(title string) fakeMedia {
	return fakeMedia{
		Title: title, Duration: 1440,
		Streams: []fakeStream{
			{Type: "video", Codec: "h264", Width: 1920, Height: 1080, Default: true},
			{Type: "audio", Codec: "flac", Channels: 2, Lang: "jpn", Title: "Japanese", Default: true},
			{Type: "audio", Codec: "ac3", Channels: 6, Lang: "eng", Title: "English"},
			{Type: "subtitle", Codec: "subrip", Lang: "eng", Title: "Full", Default: true},
			{Type: "subtitle", Codec: "subrip", Lang: "eng", Title: "Signs"},
		},
	}
}

// clone copies the streams so a test can edit one without touching goodMedia's.
func (m fakeMedia) clone() fakeMedia {
	m.Streams = slices.Clone(m.Streams)
	return m
}

func (m fakeMedia) probeJSON() []byte {
	var streams []map[string]any
	for i, s := range m.Streams {
		tags := map[string]string{}
		if s.Lang != "" {
			tags["language"] = s.Lang
		}
		if s.Title != "" {
			tags["title"] = s.Title
		}
		def := 0
		if s.Default {
			def = 1
		}
		streams = append(streams, map[string]any{
			"index": i, "codec_type": s.Type, "codec_name": s.Codec, "channels": s.Channels,
			"width": s.Width, "height": s.Height, "disposition": map[string]int{"default": def}, "tags": tags,
		})
	}
	format := map[string]any{"filename": "x", "size": "1000", "duration": fmt.Sprintf("%.3f", m.Duration)}
	if m.Title != "" {
		format["tags"] = map[string]string{"TITLE": m.Title}
	}
	out, _ := json.Marshal(map[string]any{"streams": streams, "chapters": []any{}, "format": format})
	return out
}

// fakeFF answers ffprobe from a table keyed by path, records every path it was
// asked about and the most calls it ever had in flight.
type fakeFF struct {
	mu    sync.Mutex
	media map[string]fakeMedia
	asked []string
	delay time.Duration

	inflight, peak atomic.Int32
}

func (f *fakeFF) Run(_ context.Context, argv ...string) (proc.Result, error) {
	if argv[0] != "ffprobe" {
		return proc.Result{}, fmt.Errorf("unexpected program %q", argv[0])
	}
	path := argv[len(argv)-1]
	n := f.inflight.Add(1)
	defer f.inflight.Add(-1)
	for {
		old := f.peak.Load()
		if n <= old || f.peak.CompareAndSwap(old, n) {
			break
		}
	}
	if f.delay > 0 {
		time.Sleep(f.delay)
	}

	f.mu.Lock()
	f.asked = append(f.asked, path)
	m, ok := f.media[path]
	f.mu.Unlock()
	if !ok || m.Fail {
		return proc.Result{ExitCode: 1, Stderr: []byte("Invalid data found when processing input")}, nil
	}
	if slices.Contains(argv, "-show_format") {
		return proc.Result{Stdout: m.probeJSON()}, nil
	}
	out := fmt.Sprintf(`{"format": {"duration": "%.3f"}}`, m.Duration)
	return proc.Result{Stdout: []byte(out), Stderr: []byte(strings.Join(m.Warn, "\n"))}, nil
}

func (f *fakeFF) Stream(context.Context, []string, func(string)) (int, error) {
	panic("mkv-audit never streams")
}

func (f *fakeFF) wasAsked(path string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Contains(f.asked, path)
}

// fixture is a MEDIA_ROOT in a temp dir with a fake ffprobe.
type fixture struct {
	t    *testing.T
	root string
	ff   *fakeFF
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	return &fixture{t: t, root: t.TempDir(), ff: &fakeFF{media: map[string]fakeMedia{}}}
}

func (fx *fixture) deps() Deps {
	return Deps{Paths: paths.Config{MediaRoot: fx.root}, Runner: fx.ff}
}

// shareName is the pipeline share's directory name, which the fixtures spell
// SHARE so that it is only ever written in kit/paths.
var shareName = filepath.Base(paths.Config{}.Share())

// path is rel under the fixture root, with a leading SHARE segment standing for
// the pipeline share.
func (fx *fixture) path(rel string) string {
	if rel == "SHARE" || strings.HasPrefix(rel, "SHARE/") {
		rel = shareName + rel[len("SHARE"):]
	}
	return filepath.Join(fx.root, rel)
}

// plain writes a file the fake ffprobe knows nothing about.
func (fx *fixture) plain(rel string) string {
	fx.t.Helper()
	p := fx.path(rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		fx.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("0123456789"), 0o644); err != nil {
		fx.t.Fatal(err)
	}
	return p
}

// put writes a file and teaches the fake ffprobe its media.
func (fx *fixture) put(rel string, m fakeMedia) string {
	fx.t.Helper()
	p := fx.plain(rel)
	fx.ff.media[p] = m
	return p
}

func (fx *fixture) mkdir(rel string) string {
	fx.t.Helper()
	p := fx.path(rel)
	if err := os.MkdirAll(p, 0o755); err != nil {
		fx.t.Fatal(err)
	}
	return p
}

func codes(fs []Finding) []string {
	out := make([]string, len(fs))
	for i, f := range fs {
		out[i] = f.Code
	}
	slices.Sort(out)
	return out
}

func count(fs []Finding, code string) int {
	n := 0
	for _, f := range fs {
		if f.Code == code {
			n++
		}
	}
	return n
}

func find(t *testing.T, fs []Finding, code string) Finding {
	t.Helper()
	for _, f := range fs {
		if f.Code == code {
			return f
		}
	}
	t.Fatalf("no %s finding in %v", code, codes(fs))
	return Finding{}
}

// run is the command body with captured streams: exit code, stdout, stderr.
func run(t *testing.T, d Deps, args ...string) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := cli.Run(context.Background(), "mkv-audit", args, cli.IO{Stdout: &stdout, Stderr: &stderr},
		func(ctx context.Context, args []string, streams cli.IO) error { return Command(ctx, d, args, streams) })
	return code, stdout.String(), stderr.String()
}

func inventoryStreams(m fakeMedia) []inventory.Stream {
	var out []inventory.Stream
	opt := func(s string) *string {
		if s == "" {
			return nil
		}
		return &s
	}
	for i, s := range m.Streams {
		out = append(out, inventory.Stream{Index: i, Type: s.Type, Codec: s.Codec, Lang: opt(s.Lang), Title: opt(s.Title),
			Default: s.Default, Channels: s.Channels, Width: s.Width, Height: s.Height})
	}
	return out
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}
