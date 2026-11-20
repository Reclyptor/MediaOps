package manifest

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/reclyptor/mediaops/kit/proc"
)

// headerLen is the fixed size of a fake media file's metadata block, so an edit
// never changes a file's size and a move carries the metadata with the bytes.
const headerLen = 1024

type fakeStream struct {
	Type     string `json:"type"` // ffprobe's codec_type
	Codec    string `json:"codec"`
	Channels int    `json:"channels,omitempty"`
	Width    int    `json:"width,omitempty"`
	Height   int    `json:"height,omitempty"`
	Lang     string `json:"lang,omitempty"`
	Title    string `json:"title,omitempty"`
	Default  bool   `json:"default,omitempty"`
}

// fakeMedia is everything the fake tools know about a file, kept in the file's
// own first headerLen bytes.
type fakeMedia struct {
	Title        string       `json:"title"`
	Duration     float64      `json:"duration"`
	Frames       int64        `json:"frames"`
	DecodeErrors []string     `json:"decode_errors,omitempty"`
	Streams      []fakeStream `json:"streams"`
}

// sampleMedia is video 0, audio 1 (eng, default) and 2 (jpn), subtitles 3
// (eng) and 4 (jpn), 100 frames over 4.2 s.
func sampleMedia() fakeMedia {
	return fakeMedia{
		Duration: 4.2, Frames: 100,
		Streams: []fakeStream{
			{Type: "video", Codec: "h264", Width: 160, Height: 120},
			{Type: "audio", Codec: "aac", Channels: 2, Lang: "eng", Title: "English", Default: true},
			{Type: "audio", Codec: "aac", Channels: 2, Lang: "jpn", Title: "Japanese"},
			{Type: "subtitle", Codec: "subrip", Lang: "eng", Title: "Full"},
			{Type: "subtitle", Codec: "subrip", Lang: "jpn", Title: "Signs"},
		},
	}
}

func (m fakeMedia) bytes(payload string) []byte {
	head, err := json.Marshal(m)
	if err != nil || len(head) > headerLen {
		panic(fmt.Sprintf("fake media header: %v (%d bytes)", err, len(head)))
	}
	return append(append(head, bytes.Repeat([]byte(" "), headerLen-len(head))...), payload...)
}

func readFakeMedia(path string) (fakeMedia, []byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return fakeMedia{}, nil, err
	}
	if len(data) < headerLen {
		return fakeMedia{}, nil, fmt.Errorf("%s is not fake media", path)
	}
	var m fakeMedia
	if err := json.Unmarshal(bytes.TrimSpace(data[:headerLen]), &m); err != nil {
		return fakeMedia{}, nil, fmt.Errorf("%s is not fake media: %w", path, err)
	}
	return m, data[headerLen:], nil
}

func writeFakeMedia(path string, m fakeMedia, payload []byte) error {
	return os.WriteFile(path, append(m.bytes(""), payload...), 0o660)
}

// fakeRunner is a proc.Runner whose media state lives in each file's bytes:
// mkvpropedit rewrites the header, ffprobe, ffmpeg and mkvmerge -J read it, and
// mkvmerge -o copies the file. The ignore and mod switches make an edit or a
// remux come out wrong, to exercise the read-backs.
type fakeRunner struct {
	mu    sync.Mutex
	calls [][]string

	// hook runs at the start of every call, before it is served.
	hook func(argv []string)

	ignoreTitle, ignoreFlags, ignoreLang bool
	// remuxMod alters the media a remux writes.
	remuxMod func(*fakeMedia)
	// remuxFail makes mkvmerge exit with this code and write nothing.
	remuxFail int
}

var _ proc.Runner = (*fakeRunner)(nil)

func (f *fakeRunner) Stream(context.Context, []string, func(string)) (int, error) {
	panic("not used")
}

func (f *fakeRunner) Run(_ context.Context, argv ...string) (proc.Result, error) {
	f.mu.Lock()
	f.calls = append(f.calls, argv)
	hook := f.hook
	f.mu.Unlock()
	if err := proc.Check(argv); err != nil {
		return proc.Result{}, err
	}
	if hook != nil {
		hook(argv)
	}
	res, err := f.serve(argv)
	if err != nil {
		return proc.Result{ExitCode: 2, Stdout: []byte("Error: " + err.Error()), Stderr: []byte(err.Error())}, nil
	}
	return res, nil
}

func (f *fakeRunner) serve(argv []string) (proc.Result, error) {
	last := argv[len(argv)-1]
	has := func(s string) bool {
		for _, a := range argv {
			if a == s || strings.Contains(a, s) {
				return true
			}
		}
		return false
	}
	switch argv[0] {
	case "ffprobe":
		m, _, err := readFakeMedia(last)
		if err != nil {
			return proc.Result{}, err
		}
		switch {
		case has("-show_streams"):
			return proc.Result{Stdout: m.ffprobeStreams()}, nil
		case has("-count_frames"):
			return proc.Result{Stdout: m.ffprobeFrames()}, nil
		case has("format=duration"):
			out, _ := json.Marshal(map[string]any{"format": map[string]string{"duration": strconv.FormatFloat(m.Duration, 'f', 6, 64)}})
			return proc.Result{Stdout: out}, nil
		}
	case "ffmpeg":
		for i, a := range argv {
			if a == "-i" {
				m, _, err := readFakeMedia(argv[i+1])
				if err != nil {
					return proc.Result{}, err
				}
				return proc.Result{Stderr: []byte(strings.Join(m.DecodeErrors, "\n"))}, nil
			}
		}
	case "mkvmerge":
		if argv[1] == "-J" {
			m, _, err := readFakeMedia(last)
			if err != nil {
				return proc.Result{}, err
			}
			return proc.Result{Stdout: m.mkvmergeJSON()}, nil
		}
		return f.remux(argv[3], argv[4])
	case "mkvextract":
		return proc.Result{}, nil
	case "mkvpropedit":
		return f.propedit(argv[1], argv[2:])
	}
	return proc.Result{}, fmt.Errorf("fake: unexpected %q", argv)
}

// remux serves `mkvmerge -q -o DST SRC`: dst is src's bytes, carried over
// whole, unless remuxMod says otherwise.
func (f *fakeRunner) remux(dst, src string) (proc.Result, error) {
	if f.remuxFail != 0 {
		return proc.Result{ExitCode: f.remuxFail, Stdout: []byte("Error: mkvmerge failed")}, nil
	}
	m, payload, err := readFakeMedia(src)
	if err != nil {
		return proc.Result{}, err
	}
	if f.remuxMod != nil {
		f.remuxMod(&m)
	}
	return proc.Result{}, writeFakeMedia(dst, m, payload)
}

func (m fakeMedia) ffprobeStreams() []byte {
	var out []map[string]any
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
		st := map[string]any{"index": i, "codec_type": s.Type, "codec_name": s.Codec, "disposition": map[string]int{"default": def}, "tags": tags}
		if s.Channels > 0 {
			st["channels"] = s.Channels
		}
		if s.Width > 0 {
			st["width"], st["height"] = s.Width, s.Height
		}
		out = append(out, st)
	}
	b, _ := json.Marshal(map[string]any{"streams": out})
	return b
}

func (m fakeMedia) ffprobeFrames() []byte {
	hasVideo := false
	for _, s := range m.Streams {
		hasVideo = hasVideo || s.Type == "video"
	}
	var streams []map[string]string
	if hasVideo {
		n := strconv.FormatInt(m.Frames, 10)
		streams = append(streams, map[string]string{"nb_read_frames": n, "nb_read_packets": n})
	}
	b, _ := json.Marshal(map[string]any{"streams": streams})
	return b
}

func (m fakeMedia) mkvmergeJSON() []byte {
	var tracks []map[string]any
	for i, s := range m.Streams {
		typ := s.Type
		if typ == "subtitle" {
			typ = "subtitles"
		}
		tracks = append(tracks, map[string]any{"id": i, "type": typ, "properties": map[string]any{
			"uid": 1000 + i, "track_name": s.Title, "language": s.Lang, "default_track": s.Default,
		}})
	}
	b, _ := json.Marshal(map[string]any{"container": map[string]any{"properties": map[string]string{"title": m.Title}}, "tracks": tracks})
	return b
}

// propedit serves `mkvpropedit FILE --edit info|track:aN|track:vN|track:sN --set k=v ...`.
func (f *fakeRunner) propedit(file string, args []string) (proc.Result, error) {
	m, payload, err := readFakeMedia(file)
	if err != nil {
		return proc.Result{}, err
	}
	var cur *fakeStream
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--edit":
			i++
			cur = nil
			if target, ok := strings.CutPrefix(args[i], "track:"); ok {
				if cur = m.track(target); cur == nil {
					return proc.Result{}, fmt.Errorf("no such track %s", target)
				}
			}
		case "--set":
			i++
			k, v, _ := strings.Cut(args[i], "=")
			switch {
			case cur == nil && k == "title":
				if !f.ignoreTitle {
					m.Title = v
				}
			case k == "flag-default":
				if !f.ignoreFlags {
					cur.Default = v == "1"
				}
			case k == "language":
				if !f.ignoreLang {
					cur.Lang = v
				}
			}
		default:
			return proc.Result{}, fmt.Errorf("unexpected %s", args[i])
		}
	}
	return proc.Result{}, writeFakeMedia(file, m, payload)
}

func (m *fakeMedia) track(spec string) *fakeStream {
	typ := map[byte]string{'a': "audio", 'v': "video", 's': "subtitle"}[spec[0]]
	n, _ := strconv.Atoi(spec[1:])
	for i := range m.Streams {
		if m.Streams[i].Type == typ {
			if n--; n == 0 {
				return &m.Streams[i]
			}
		}
	}
	return nil
}

// count is how many calls started with the program and, when file is not
// empty, named file as their first path argument.
func (f *fakeRunner) count(program, file string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		if c[0] != program {
			continue
		}
		if file == "" || (len(c) > 1 && c[1] == file) {
			n++
		}
	}
	return n
}

func (f *fakeRunner) total() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

// writeMedia writes a fake media file at path, creating its directory, with
// mods applied to the sample, and returns the path.
func writeMedia(t *testing.T, path string, mods ...func(*fakeMedia)) string {
	t.Helper()
	m := sampleMedia()
	for _, mod := range mods {
		mod(&m)
	}
	if err := os.MkdirAll(dirOf(path), 0o770); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, m.bytes("payload of "+path), 0o660); err != nil {
		t.Fatal(err)
	}
	return path
}

func dirOf(path string) string { return path[:strings.LastIndexByte(path, '/')] }

// media reads path's fake metadata.
func media(t *testing.T, path string) fakeMedia {
	t.Helper()
	m, _, err := readFakeMedia(path)
	if err != nil {
		t.Fatal(err)
	}
	return m
}
