package mkvtool

import (
	"context"
	"fmt"
	"slices"

	"github.com/reclyptor/mediaops/kit/ffprobe"
	"github.com/reclyptor/mediaops/kit/proc"
)

// layout is a file's streams as ffprobe numbers them, with the audio and
// subtitle ones in the order mkvpropedit counts them.
type layout struct {
	streams []ffprobe.Stream
	byIndex map[int]ffprobe.Stream
	audio   []int
	subs    []int
	ident   Identification
}

// readLayout reads file with ffprobe and mkvmerge and refuses when the two
// disagree on how many audio or subtitle tracks it has: mkvpropedit addresses
// tracks by their order within a type, and that order is only trusted when
// both tools see the same tracks.
func readLayout(ctx context.Context, r proc.Runner, file string) (layout, error) {
	streams, err := ffprobe.Streams(ctx, r, file)
	if err != nil {
		return layout{}, err
	}
	ident, err := Identify(ctx, r, file)
	if err != nil {
		return layout{}, err
	}
	slices.SortFunc(streams, func(a, b ffprobe.Stream) int { return a.Index - b.Index })
	l := layout{streams: streams, byIndex: map[int]ffprobe.Stream{}, ident: ident}
	for _, s := range streams {
		l.byIndex[s.Index] = s
		switch s.CodecType {
		case "audio":
			l.audio = append(l.audio, s.Index)
		case "subtitle":
			l.subs = append(l.subs, s.Index)
		}
	}
	var mkvAudio, mkvSubs int
	for _, t := range ident.Tracks {
		switch t.Type {
		case "audio":
			mkvAudio++
		case "subtitles":
			mkvSubs++
		}
	}
	if len(l.audio) != mkvAudio || len(l.subs) != mkvSubs {
		return layout{}, fmt.Errorf("ffprobe sees %d audio and %d subtitle streams in %s but mkvmerge sees %d and %d",
			len(l.audio), len(l.subs), file, mkvAudio, mkvSubs)
	}
	return l, nil
}

// audioOrdinal is the 1-based position of an audio stream among the audio
// streams, which is the N of mkvpropedit's track:aN. The index must be one of
// l.audio.
func (l layout) audioOrdinal(index int) int { return slices.Index(l.audio, index) + 1 }

// checkTitles fails when any stream of after has a different title tag than it
// had in before. Track names are never changed.
func checkTitles(file string, before, after []ffprobe.Stream) error {
	if len(before) != len(after) {
		return fmt.Errorf("%s had %d streams before the edit and has %d after", file, len(before), len(after))
	}
	afterByIndex := map[int]ffprobe.Stream{}
	for _, s := range after {
		afterByIndex[s.Index] = s
	}
	for _, b := range before {
		a, ok := afterByIndex[b.Index]
		if !ok {
			return fmt.Errorf("stream %d of %s is gone after the edit", b.Index, file)
		}
		bt, bOK := b.Tags["title"]
		at, aOK := a.Tags["title"]
		if bt != at || bOK != aOK {
			return fmt.Errorf("the title of stream %d of %s changed from %q to %q", b.Index, file, bt, at)
		}
	}
	return nil
}
