package mkvtool

import (
	"context"
	"fmt"
	"maps"
	"regexp"
	"slices"

	"github.com/reclyptor/mediaops/kit/ffprobe"
	"github.com/reclyptor/mediaops/kit/proc"
)

var languageCode = regexp.MustCompile(`^[a-z]{3}$`)

// trackMeta is what a language edit must leave alone.
type trackMeta struct {
	Language string
	Title    string
	Default  bool
}

func trackMetaOf(s ffprobe.Stream) trackMeta {
	lang, ok := s.Tags["language"]
	if !ok {
		lang = "und"
	}
	return trackMeta{Language: lang, Title: s.Tags["title"], Default: s.Default}
}

// SetAudioLanguages sets the language of the audio streams named by ffprobe
// index in langs, each a three-letter lowercase ISO 639-2 code. It also
// rewrites the track's Matroska LANGUAGE tag when it carries one, because
// ffmpeg-based readers prefer that tag over the Language element. Only
// languages change: names and default flags are read before and after and must
// be identical.
//
// It refuses, editing nothing, an empty langs, an index that is not an audio
// stream, a language that is not three lowercase letters, and a file whose
// audio or subtitle count ffprobe and mkvmerge disagree on.
func SetAudioLanguages(ctx context.Context, r proc.Runner, file string, langs map[int]string) error {
	return setLanguages(ctx, r, file, audioTracks, langs)
}

// SetVideoLanguages is SetAudioLanguages for video streams. Cover art that
// ffprobe lists as a video stream is not one, and a file whose video count
// ffprobe and mkvmerge disagree on is refused too.
func SetVideoLanguages(ctx context.Context, r proc.Runner, file string, langs map[int]string) error {
	return setLanguages(ctx, r, file, videoTracks, langs)
}

func setLanguages(ctx context.Context, r proc.Runner, file string, tt trackType, langs map[int]string) error {
	if len(langs) == 0 {
		return fmt.Errorf("no %s languages to set on %s", tt.name, file)
	}
	for idx, lang := range langs {
		if !languageCode.MatchString(lang) {
			return fmt.Errorf("%q for stream %d of %s is not a three-letter lowercase language code", lang, idx, file)
		}
	}
	l, err := readLayout(ctx, r, file)
	if err != nil {
		return err
	}
	order := slices.Sorted(maps.Keys(langs))
	for _, idx := range order {
		if !slices.Contains(l.indexes(tt), idx) {
			return fmt.Errorf("stream %d is not %s stream of %s", idx, tt.article, file)
		}
	}

	before := map[int]trackMeta{}
	for _, idx := range l.indexes(tt) {
		before[idx] = trackMetaOf(l.byIndex[idx])
	}

	argv := []string{"mkvpropedit", file}
	for _, idx := range order {
		argv = append(argv, "--edit", tt.selector(l.ordinal(tt, idx)), "--set", "language="+langs[idx])
	}
	if err := runEdit(ctx, r, file, argv...); err != nil {
		return err
	}

	var tracks []Track
	for _, t := range l.ident.Tracks {
		if t.Type == tt.name {
			tracks = append(tracks, t)
		}
	}
	for _, idx := range order {
		ord := l.ordinal(tt, idx)
		uid := tracks[ord-1].UID
		if uid == "" {
			return fmt.Errorf("%s track %d of %s has no UID, so its tags cannot be matched", tt.name, ord, file)
		}
		if err := retagLanguage(ctx, r, file, tt, ord, uid, langs[idx]); err != nil {
			return err
		}
	}

	after, err := ffprobe.Streams(ctx, r, file)
	if err != nil {
		return err
	}
	want := maps.Clone(before)
	for idx, lang := range langs {
		m := want[idx]
		m.Language = lang
		want[idx] = m
	}
	got := map[int]trackMeta{}
	for _, s := range after {
		if tt.is(s) {
			got[s.Index] = trackMetaOf(s)
		}
	}
	if !maps.Equal(want, got) {
		return fmt.Errorf("the %s of %s reads back as %+v after setting %+v", tt.name, file, got, want)
	}
	return checkTitles(file, l.streams, after)
}
