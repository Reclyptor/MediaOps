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

// audioMeta is what an audio edit must leave alone.
type audioMeta struct {
	Language string
	Title    string
	Default  bool
}

func audioMetaOf(s ffprobe.Stream) audioMeta {
	lang, ok := s.Tags["language"]
	if !ok {
		lang = "und"
	}
	return audioMeta{Language: lang, Title: s.Tags["title"], Default: s.Default}
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
	if len(langs) == 0 {
		return fmt.Errorf("no audio languages to set on %s", file)
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
		if !slices.Contains(l.audio, idx) {
			return fmt.Errorf("stream %d is not an audio stream of %s", idx, file)
		}
	}

	before := map[int]audioMeta{}
	for _, idx := range l.audio {
		before[idx] = audioMetaOf(l.byIndex[idx])
	}

	argv := []string{"mkvpropedit", file}
	for _, idx := range order {
		argv = append(argv, "--edit", fmt.Sprintf("track:a%d", l.audioOrdinal(idx)), "--set", "language="+langs[idx])
	}
	if err := runEdit(ctx, r, file, argv...); err != nil {
		return err
	}

	audioTracks := make([]Track, 0, len(l.audio))
	for _, t := range l.ident.Tracks {
		if t.Type == "audio" {
			audioTracks = append(audioTracks, t)
		}
	}
	for _, idx := range order {
		ord := l.audioOrdinal(idx)
		uid := audioTracks[ord-1].UID
		if uid == "" {
			return fmt.Errorf("audio track %d of %s has no UID, so its tags cannot be matched", ord, file)
		}
		if err := retagLanguage(ctx, r, file, ord, uid, langs[idx]); err != nil {
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
	got := map[int]audioMeta{}
	for _, s := range after {
		if s.CodecType == "audio" {
			got[s.Index] = audioMetaOf(s)
		}
	}
	if !maps.Equal(want, got) {
		return fmt.Errorf("the audio of %s reads back as %+v after setting %+v", file, got, want)
	}
	return checkTitles(file, l.streams, after)
}
