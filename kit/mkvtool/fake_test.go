package mkvtool

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/reclyptor/mediaops/kit/proc"
)

// fakeTrack is one track of the in-memory file the stateful fake serves.
type fakeTrack struct {
	typ   string // ffprobe's codec_type: video, audio or subtitle
	lang  string
	title string
	def   bool
	uid   string
	// simples are the Simple tags of the track's Matroska tag, as XML nodes.
	simples []node
}

// fakeMkv is a proc.Runner that keeps one Matroska file in memory. mkvpropedit
// mutates it and ffprobe, mkvmerge and mkvextract render it, so an op's
// read-back sees what the edit did. The ignore* switches make an edit a no-op
// and failEdit makes mkvpropedit exit with a code, to exercise the read-back
// and exit handling.
type fakeMkv struct {
	title  string
	tracks []*fakeTrack
	// mkvExtraAudio makes mkvmerge list audio tracks ffprobe does not see.
	mkvExtraAudio int

	ignoreFlags, ignoreLang, ignoreTags bool
	renameOnEdit                        bool
	failEdit                            int

	calls    [][]string
	tagFiles []string // contents of every --tags file at the time it was read
	tagPaths []string
}

var _ proc.Runner = (*fakeMkv)(nil)

func (f *fakeMkv) Stream(context.Context, []string, func(string)) (int, error) {
	panic("not used")
}

func (f *fakeMkv) ran(program string) bool {
	for _, c := range f.calls {
		if c[0] == program {
			return true
		}
	}
	return false
}

func (f *fakeMkv) programs() []string {
	var out []string
	for _, c := range f.calls {
		out = append(out, c[0])
	}
	return out
}

func (f *fakeMkv) Run(_ context.Context, argv ...string) (proc.Result, error) {
	f.calls = append(f.calls, argv)
	if err := proc.Check(argv); err != nil {
		return proc.Result{}, err
	}
	switch argv[0] {
	case "ffprobe":
		return proc.Result{Stdout: f.ffprobe()}, nil
	case "mkvmerge":
		return proc.Result{Stdout: f.mkvmerge()}, nil
	case "mkvextract":
		return proc.Result{Stdout: f.mkvextract()}, nil
	case "mkvpropedit":
		return f.mkvpropedit(argv[2:])
	}
	return proc.Result{}, fmt.Errorf("fake: unexpected %q", argv)
}

func (f *fakeMkv) ffprobe() []byte {
	var streams []map[string]any
	for i, t := range f.tracks {
		tags := map[string]string{}
		if t.lang != "" {
			tags["language"] = t.lang
		}
		if t.title != "" {
			tags["title"] = t.title
		}
		for _, s := range t.simples {
			if name, _ := s.childText("Name"); name == "LANGUAGE" {
				// ffmpeg exposes a LANGUAGE track tag next to the Language element.
				tags["LANGUAGE"], _ = s.childText("String")
			}
		}
		def := 0
		if t.def {
			def = 1
		}
		streams = append(streams, map[string]any{
			"index": i, "codec_type": t.typ, "codec_name": "x",
			"disposition": map[string]int{"default": def}, "tags": tags,
		})
	}
	out, _ := json.Marshal(map[string]any{"streams": streams})
	return out
}

func (f *fakeMkv) mkvmerge() []byte {
	var tracks []string
	add := func(id int, typ, uid, name, lang string, def bool) {
		tracks = append(tracks, fmt.Sprintf(
			`{"id": %d, "type": %q, "properties": {"uid": %s, "track_name": %q, "language": %q, "default_track": %t}}`,
			id, typ, uid, name, lang, def))
	}
	for i, t := range f.tracks {
		typ := t.typ
		if typ == "subtitle" {
			typ = "subtitles"
		}
		add(i, typ, t.uid, t.title, t.lang, t.def)
	}
	for i := 0; i < f.mkvExtraAudio; i++ {
		add(len(f.tracks)+i, "audio", strconv.Itoa(9000+i), "", "und", false)
	}
	return fmt.Appendf(nil, `{"container": {"properties": {"title": %q}}, "tracks": [%s]}`, f.title, strings.Join(tracks, ","))
}

func (f *fakeMkv) mkvextract() []byte {
	var b strings.Builder
	b.WriteString("\xef\xbb\xbf<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n<Tags>\n")
	for _, t := range f.tracks {
		if len(t.simples) == 0 {
			continue
		}
		fmt.Fprintf(&b, "  <Tag>\n    <Targets>\n      <TargetTypeValue>50</TargetTypeValue>\n      <TrackUID>%s</TrackUID>\n    </Targets>\n", t.uid)
		for _, s := range t.simples {
			out, _ := xml.MarshalIndent(s, "    ", "  ")
			b.Write(out)
			b.WriteString("\n")
		}
		b.WriteString("  </Tag>\n")
	}
	b.WriteString("</Tags>\n")
	if len(f.tracks) == 0 || !f.anyTags() {
		return nil
	}
	return []byte(b.String())
}

func (f *fakeMkv) anyTags() bool {
	for _, t := range f.tracks {
		if len(t.simples) > 0 {
			return true
		}
	}
	return false
}

func (f *fakeMkv) track(spec string) *fakeTrack {
	typ := map[byte]string{'a': "audio", 's': "subtitle"}[spec[0]]
	n, _ := strconv.Atoi(spec[1:])
	for _, t := range f.tracks {
		if t.typ == typ {
			if n--; n == 0 {
				return t
			}
		}
	}
	return nil
}

func (f *fakeMkv) mkvpropedit(args []string) (proc.Result, error) {
	if f.failEdit != 0 {
		return proc.Result{ExitCode: f.failEdit, Stdout: []byte("Error: cannot edit")}, nil
	}
	var cur *fakeTrack
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--edit":
			i++
			cur = nil
			if target, ok := strings.CutPrefix(args[i], "track:"); ok {
				if cur = f.track(target); cur == nil {
					return proc.Result{ExitCode: 2, Stdout: []byte("Error: no such track")}, nil
				}
			}
		case "--set":
			i++
			k, v, _ := strings.Cut(args[i], "=")
			switch {
			case cur == nil && k == "title":
				f.title = v
			case k == "flag-default" && !f.ignoreFlags:
				cur.def = v == "1"
			case k == "language" && !f.ignoreLang:
				cur.lang = v
			}
			if f.renameOnEdit && cur != nil {
				cur.title += "!"
			}
		case "--tags":
			i++
			spec := strings.TrimPrefix(args[i], "track:")
			target, path, _ := strings.Cut(spec, ":")
			body, err := os.ReadFile(path)
			if err != nil {
				return proc.Result{ExitCode: 2, Stdout: []byte("Error: " + err.Error())}, nil
			}
			f.tagFiles = append(f.tagFiles, string(body))
			f.tagPaths = append(f.tagPaths, path)
			var root node
			if err := xml.Unmarshal(body, &root); err != nil {
				return proc.Result{ExitCode: 2, Stdout: []byte("Error: " + err.Error())}, nil
			}
			t := f.track(target)
			if t == nil {
				return proc.Result{ExitCode: 2, Stdout: []byte("Error: no such track")}, nil
			}
			if f.ignoreTags {
				continue
			}
			t.simples = nil
			for _, tag := range root.Children {
				for _, c := range tag.Children {
					if c.XMLName.Local == "Simple" {
						c.dropIndentation()
						t.simples = append(t.simples, c)
					}
				}
			}
		default:
			return proc.Result{ExitCode: 2, Stdout: []byte("Error: unexpected " + args[i])}, nil
		}
	}
	return proc.Result{}, nil
}

func simple(name, value string, nested ...node) node {
	n := node{XMLName: xml.Name{Local: "Simple"}, Children: []node{
		{XMLName: xml.Name{Local: "Name"}, Text: name},
		{XMLName: xml.Name{Local: "String"}, Text: value},
	}}
	n.Children = append(n.Children, nested...)
	return n
}

// sample is a file with video 0, audio 1 (eng, default, carries a LANGUAGE tag
// plus an unrelated and a nested tag), audio 2 (jpn), subtitles 3 and 4.
func sample() *fakeMkv {
	return &fakeMkv{
		title: "Show",
		tracks: []*fakeTrack{
			{typ: "video", uid: "9007199254740993"},
			{typ: "audio", lang: "eng", title: "English 5.1", def: true, uid: "18446744073709551557", simples: []node{
				simple("LANGUAGE", "eng"),
				simple("ENCODER", "libfoo", simple("VERSION", "1.2")),
			}},
			{typ: "audio", lang: "jpn", title: "Japanese", uid: "18446744073709551558"},
			{typ: "subtitle", lang: "eng", title: "Full", uid: "5"},
			{typ: "subtitle", lang: "jpn", title: "Signs", def: true, uid: "6"},
		},
	}
}
