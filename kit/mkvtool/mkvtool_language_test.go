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
