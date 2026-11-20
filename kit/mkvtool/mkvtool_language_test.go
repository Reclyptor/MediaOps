package mkvtool

import (
	"context"
	"os"
	"reflect"
	"strings"
	"testing"
)

func TestSetAudioLanguagesRewritesLanguageTag(t *testing.T) {
	f := sample()
	if err := SetAudioLanguages(context.Background(), f, "/x/a.mkv", map[int]string{1: "jpn", 2: "eng"}); err != nil {
		t.Fatal(err)
	}

	wantPrograms := []string{
		"ffprobe", "mkvmerge", "mkvpropedit", // read, edit
		"mkvextract", "mkvpropedit", "mkvextract", // retag audio 1 (has a LANGUAGE tag), verify
		"mkvextract", // audio 2 has no tags at all
		"ffprobe",
	}
	if got := f.programs(); !reflect.DeepEqual(got, wantPrograms) {
		t.Fatalf("programs = %q, want %q", got, wantPrograms)
	}
	wantEdit := []string{"mkvpropedit", "/x/a.mkv",
		"--edit", "track:a1", "--set", "language=jpn", "--edit", "track:a2", "--set", "language=eng"}
	if !reflect.DeepEqual(f.calls[2], wantEdit) {
		t.Fatalf("language edit = %q, want %q", f.calls[2], wantEdit)
	}
	if got := f.calls[3]; !reflect.DeepEqual(got, []string{"mkvextract", "/x/a.mkv", "tags", "/dev/stdout"}) {
		t.Fatalf("extract = %q", got)
	}
	tagEdit := f.calls[4]
	if len(tagEdit) != 4 || tagEdit[2] != "--tags" || !strings.HasPrefix(tagEdit[3], "track:a1:") ||
		!strings.Contains(tagEdit[3], "mkvtool-tags-") || !strings.HasSuffix(tagEdit[3], ".xml") {
		t.Fatalf("tag edit = %q", tagEdit)
	}

	if len(f.tagFiles) != 1 {
		t.Fatalf("%d tag files written, want 1", len(f.tagFiles))
	}
	doc := f.tagFiles[0]
	if !strings.HasPrefix(doc, `<?xml version="1.0"?><Tags>`) || strings.Contains(doc, "TrackUID") {
		t.Fatalf("tag file = %s", doc)
	}
	if _, err := os.Stat(f.tagPaths[0]); !os.IsNotExist(err) {
		t.Fatalf("temporary tag file survived: %v", err)
	}

	got := f.tracks[1]
	if got.lang != "jpn" || !got.def || got.title != "English 5.1" {
		t.Fatalf("track 1 = %+v", got)
	}
	if v, _ := got.simples[0].childText("String"); v != "jpn" {
		t.Fatalf("LANGUAGE tag = %q, want jpn", v)
	}
	enc := got.simples[1]
	if name, _ := enc.childText("Name"); name != "ENCODER" {
		t.Fatalf("unrelated tag lost: %+v", got.simples)
	}
	if v, _ := enc.childText("String"); v != "libfoo" {
		t.Fatalf("ENCODER = %q", v)
	}
	nested := enc.child("Simple")
	if nested == nil {
		t.Fatalf("nested Simple lost: %+v", enc)
	}
	if v, _ := nested.childText("String"); v != "1.2" {
		t.Fatalf("nested Simple = %q", v)
	}
	if f.tracks[2].lang != "eng" {
		t.Fatalf("track 2 language = %q", f.tracks[2].lang)
	}
}

func TestSetAudioLanguagesSkipsTracksWithoutLanguageTag(t *testing.T) {
	f := sample()
	if err := SetAudioLanguages(context.Background(), f, "/x/a.mkv", map[int]string{2: "eng"}); err != nil {
		t.Fatal(err)
	}
	if len(f.tagFiles) != 0 {
		t.Fatalf("tags rewritten for a track without a LANGUAGE tag: %q", f.tagFiles)
	}
	if f.tracks[1].lang != "eng" || f.tracks[2].lang != "eng" {
		t.Fatal("other tracks changed")
	}
}

func TestSetAudioLanguagesKeepsUnnamedSimplesOut(t *testing.T) {
	f := sample()
	f.tracks[1].simples = append(f.tracks[1].simples, simple("", "orphan"))
	if err := SetAudioLanguages(context.Background(), f, "/x/a.mkv", map[int]string{1: "jpn"}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(f.tagFiles[0], "orphan") {
		t.Fatalf("a Simple without a name was written back: %s", f.tagFiles[0])
	}
}

func TestSetAudioLanguagesRefusals(t *testing.T) {
	tests := []struct {
		name    string
		mod     func(*fakeMkv)
		langs   map[int]string
		wantErr string
	}{
		{"empty", nil, map[int]string{}, "no audio languages"},
		{"nil", nil, nil, "no audio languages"},
		{"video stream", nil, map[int]string{0: "jpn"}, "not an audio stream"},
		{"subtitle stream", nil, map[int]string{3: "jpn"}, "not an audio stream"},
		{"missing stream", nil, map[int]string{7: "jpn"}, "not an audio stream"},
		{"one good, one bad index", nil, map[int]string{1: "jpn", 4: "eng"}, "not an audio stream"},
		{"upper case", nil, map[int]string{1: "JPN"}, "three-letter lowercase"},
		{"two letters", nil, map[int]string{1: "ja"}, "three-letter lowercase"},
		{"four letters", nil, map[int]string{1: "jpnx"}, "three-letter lowercase"},
		{"digit", nil, map[int]string{1: "jp1"}, "three-letter lowercase"},
		{"empty code", nil, map[int]string{1: ""}, "three-letter lowercase"},
		{"non-ASCII", nil, map[int]string{1: "jpé"}, "three-letter lowercase"},
		{"trailing newline", nil, map[int]string{1: "jpn\n"}, "three-letter lowercase"},
		{"count mismatch", func(f *fakeMkv) { f.mkvExtraAudio = 1 }, map[int]string{1: "jpn"}, "mkvmerge sees"},
		{"language tag already contradicts", func(f *fakeMkv) {
			f.tracks[1].simples[0] = simple("LANGUAGE", "fre")
		}, map[int]string{1: "jpn"}, "differ"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := sample()
			if tc.mod != nil {
				tc.mod(f)
			}
			err := SetAudioLanguages(context.Background(), f, "/x/a.mkv", tc.langs)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %v, want it to contain %q", err, tc.wantErr)
			}
			if f.ran("mkvpropedit") {
				t.Fatalf("mkvpropedit ran despite the refusal: %q", f.calls)
			}
		})
	}
}

func TestSetAudioLanguagesReadBack(t *testing.T) {
	tests := []struct {
		name    string
		mod     func(*fakeMkv)
		langs   map[int]string
		wantErr string
	}{
		{"language edit ignored", func(f *fakeMkv) { f.ignoreLang = true }, map[int]string{2: "eng"}, "back as"},
		// The tag rewrite lands but the Language element does not, so ffprobe
		// shows two different languages for one stream and the read-back refuses.
		{"language edit ignored on a tagged track", func(f *fakeMkv) { f.ignoreLang = true }, map[int]string{1: "jpn"}, "differ"},
		{"tag write ignored", func(f *fakeMkv) { f.ignoreTags = true }, map[int]string{1: "jpn"}, "tags of audio track 1"},
		{"title changed", func(f *fakeMkv) { f.renameOnEdit = true }, map[int]string{2: "eng"}, "back as"},
		{"mkvpropedit fails", func(f *fakeMkv) { f.failEdit = 2 }, map[int]string{1: "jpn"}, "cannot edit"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := sample()
			tc.mod(f)
			err := SetAudioLanguages(context.Background(), f, "/x/a.mkv", tc.langs)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %v, want it to contain %q", err, tc.wantErr)
			}
			for _, p := range f.tagPaths {
				if _, err := os.Stat(p); !os.IsNotExist(err) {
					t.Fatalf("temporary tag file %s survived", p)
				}
			}
		})
	}
}

func TestSetAudioLanguagesNeedsUID(t *testing.T) {
	f := sample()
	f.tracks[1].uid = "null"
	err := SetAudioLanguages(context.Background(), f, "/x/a.mkv", map[int]string{1: "jpn"})
	if err == nil {
		t.Fatal("expected an error")
	}
}

// videoSample is sample with a LANGUAGE tag, an unrelated tag and a nested tag
// on its video track, which is eng.
func videoSample() *fakeMkv {
	f := sample()
	f.tracks[0].lang = "eng"
	f.tracks[0].title = "Main"
	f.tracks[0].def = true
	f.tracks[0].simples = []node{
		simple("LANGUAGE", "eng"),
		simple("ENCODER", "libfoo", simple("VERSION", "1.2")),
	}
	return f
}

func TestSetVideoLanguagesRewritesLanguageTag(t *testing.T) {
	f := videoSample()
	if err := SetVideoLanguages(context.Background(), f, "/x/a.mkv", map[int]string{0: "jpn"}); err != nil {
		t.Fatal(err)
	}

	wantPrograms := []string{
		"ffprobe", "mkvmerge", "mkvpropedit", // read, edit
		"mkvextract", "mkvpropedit", "mkvextract", // retag video 1 (has a LANGUAGE tag), verify
		"ffprobe",
	}
	if got := f.programs(); !reflect.DeepEqual(got, wantPrograms) {
		t.Fatalf("programs = %q, want %q", got, wantPrograms)
	}
	wantEdit := []string{"mkvpropedit", "/x/a.mkv", "--edit", "track:v1", "--set", "language=jpn"}
	if !reflect.DeepEqual(f.calls[2], wantEdit) {
		t.Fatalf("language edit = %q, want %q", f.calls[2], wantEdit)
	}
	tagEdit := f.calls[4]
	if len(tagEdit) != 4 || tagEdit[2] != "--tags" || !strings.HasPrefix(tagEdit[3], "track:v1:") {
		t.Fatalf("tag edit = %q", tagEdit)
	}
	if len(f.tagFiles) != 1 || strings.Contains(f.tagFiles[0], "TrackUID") {
		t.Fatalf("tag files = %q", f.tagFiles)
	}
	if _, err := os.Stat(f.tagPaths[0]); !os.IsNotExist(err) {
		t.Fatalf("temporary tag file survived: %v", err)
	}

	got := f.tracks[0]
	if got.lang != "jpn" || !got.def || got.title != "Main" {
		t.Fatalf("video track = %+v", got)
	}
	if v, _ := got.simples[0].childText("String"); v != "jpn" {
		t.Fatalf("LANGUAGE tag = %q, want jpn", v)
	}
	if name, _ := got.simples[1].childText("Name"); name != "ENCODER" || got.simples[1].child("Simple") == nil {
		t.Fatalf("unrelated tags lost: %+v", got.simples)
	}
	if f.tracks[1].lang != "eng" || f.tracks[2].lang != "jpn" {
		t.Fatal("audio tracks changed")
	}
}

func TestSetVideoLanguagesSkipsTracksWithoutLanguageTag(t *testing.T) {
	f := sample()
	if err := SetVideoLanguages(context.Background(), f, "/x/a.mkv", map[int]string{0: "jpn"}); err != nil {
		t.Fatal(err)
	}
	if len(f.tagFiles) != 0 {
		t.Fatalf("tags rewritten for a track without a LANGUAGE tag: %q", f.tagFiles)
	}
	if f.tracks[0].lang != "jpn" {
		t.Fatalf("video language = %q", f.tracks[0].lang)
	}
}

func TestSetVideoLanguagesIgnoresCoverArt(t *testing.T) {
	f := sample()
	f.tracks = append([]*fakeTrack{{typ: "video", lang: "eng", attachedPic: true, uid: "7"}}, f.tracks...)
	// The cover is stream 0, so the real video track is stream 1 and the
	// first video track mkvpropedit knows.
	if err := SetVideoLanguages(context.Background(), f, "/x/a.mkv", map[int]string{1: "jpn"}); err != nil {
		t.Fatal(err)
	}
	if got := f.calls[2]; !reflect.DeepEqual(got, []string{"mkvpropedit", "/x/a.mkv", "--edit", "track:v1", "--set", "language=jpn"}) {
		t.Fatalf("language edit = %q", got)
	}
	if f.tracks[0].lang != "eng" || f.tracks[1].lang != "jpn" {
		t.Fatalf("languages = %q, %q", f.tracks[0].lang, f.tracks[1].lang)
	}
}

func TestSetVideoLanguagesRefusals(t *testing.T) {
	tests := []struct {
		name    string
		mod     func(*fakeMkv)
		langs   map[int]string
		wantErr string
	}{
		{"empty", nil, map[int]string{}, "no video languages"},
		{"nil", nil, nil, "no video languages"},
		{"audio stream", nil, map[int]string{1: "jpn"}, "not a video stream"},
		{"subtitle stream", nil, map[int]string{3: "jpn"}, "not a video stream"},
		{"missing stream", nil, map[int]string{7: "jpn"}, "not a video stream"},
		{"one good, one bad index", nil, map[int]string{0: "jpn", 1: "eng"}, "not a video stream"},
		{"cover art", func(f *fakeMkv) {
			f.tracks = append(f.tracks, &fakeTrack{typ: "video", attachedPic: true, uid: "7"})
		}, map[int]string{5: "jpn"}, "not a video stream"},
		{"upper case", nil, map[int]string{0: "JPN"}, "three-letter lowercase"},
		{"two letters", nil, map[int]string{0: "ja"}, "three-letter lowercase"},
		{"four letters", nil, map[int]string{0: "jpnx"}, "three-letter lowercase"},
		{"digit", nil, map[int]string{0: "jp1"}, "three-letter lowercase"},
		{"empty code", nil, map[int]string{0: ""}, "three-letter lowercase"},
		{"non-ASCII", nil, map[int]string{0: "jpé"}, "three-letter lowercase"},
		{"trailing newline", nil, map[int]string{0: "jpn\n"}, "three-letter lowercase"},
		{"count mismatch", func(f *fakeMkv) { f.mkvExtraVideo = 1 }, map[int]string{0: "jpn"}, "mkvmerge sees"},
		{"language tag already contradicts", func(f *fakeMkv) {
			f.tracks[0].simples = []node{simple("LANGUAGE", "fre")}
			f.tracks[0].lang = "eng"
		}, map[int]string{0: "jpn"}, "differ"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := sample()
			if tc.mod != nil {
				tc.mod(f)
			}
			err := SetVideoLanguages(context.Background(), f, "/x/a.mkv", tc.langs)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %v, want it to contain %q", err, tc.wantErr)
			}
			if f.ran("mkvpropedit") {
				t.Fatalf("mkvpropedit ran despite the refusal: %q", f.calls)
			}
		})
	}
}

func TestSetVideoLanguagesReadBack(t *testing.T) {
	tests := []struct {
		name    string
		mod     func(*fakeMkv)
		wantErr string
	}{
		{"language edit ignored", func(f *fakeMkv) { f.ignoreLang = true; f.tracks[0].simples = nil }, "back as"},
		{"language edit ignored on a tagged track", func(f *fakeMkv) { f.ignoreLang = true }, "differ"},
		{"tag write ignored", func(f *fakeMkv) { f.ignoreTags = true }, "tags of video track 1"},
		{"title changed", func(f *fakeMkv) { f.renameOnEdit = true; f.tracks[0].simples = nil }, "back as"},
		{"mkvpropedit fails", func(f *fakeMkv) { f.failEdit = 2 }, "cannot edit"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := videoSample()
			tc.mod(f)
			err := SetVideoLanguages(context.Background(), f, "/x/a.mkv", map[int]string{0: "jpn"})
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %v, want it to contain %q", err, tc.wantErr)
			}
			for _, p := range f.tagPaths {
				if _, err := os.Stat(p); !os.IsNotExist(err) {
					t.Fatalf("temporary tag file %s survived", p)
				}
			}
		})
	}
}

func TestSetVideoLanguagesNeedsUID(t *testing.T) {
	f := videoSample()
	f.tracks[0].uid = "null"
	if err := SetVideoLanguages(context.Background(), f, "/x/a.mkv", map[int]string{0: "jpn"}); err == nil {
		t.Fatal("expected an error")
	}
}
