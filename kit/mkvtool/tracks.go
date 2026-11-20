package mkvtool

import (
	"context"
	"fmt"
	"slices"

	"github.com/reclyptor/mediaops/kit/ffprobe"
	"github.com/reclyptor/mediaops/kit/proc"
)

// trackType is a kind of track mkvpropedit addresses by its order within the
// kind, as track:aN or track:vN.
type trackType struct {
	// name is the type as ffprobe and mkvmerge print it, and as messages say it.
	name string
	// article is the type with its indefinite article, for messages.
	article string
	// letter is what mkvpropedit puts before the order.
	letter byte
}

var (
	audioTracks = trackType{name: "audio", article: "an audio", letter: 'a'}
	videoTracks = trackType{name: "video", article: "a video", letter: 'v'}
)

// layout is a file's streams as ffprobe numbers them, with the audio, video
// and subtitle ones in the order mkvpropedit counts them. Cover art that
// ffprobe lists as a video stream is not a video track and is left out of
// video.
type layout struct {
	streams []ffprobe.Stream
	byIndex map[int]ffprobe.Stream
	audio   []int
	video   []int
	subs    []int
	ident   Identification
}

// readLayout reads file with ffprobe and mkvmerge and refuses when the two
// disagree on how many audio, video or subtitle tracks it has: mkvpropedit addresses
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
		case "video":
			if !s.AttachedPic {
				l.video = append(l.video, s.Index)
			}
		case "subtitle":
			l.subs = append(l.subs, s.Index)
		}
	}
	var mkvAudio, mkvVideo, mkvSubs int
	for _, t := range ident.Tracks {
		switch t.Type {
		case "audio":
			mkvAudio++
		case "video":
			mkvVideo++
		case "subtitles":
			mkvSubs++
		}
	}
	if len(l.audio) != mkvAudio || len(l.subs) != mkvSubs {
		return layout{}, fmt.Errorf("ffprobe sees %d audio and %d subtitle streams in %s but mkvmerge sees %d and %d",
			len(l.audio), len(l.subs), file, mkvAudio, mkvSubs)
	}
	if len(l.video) != mkvVideo {
		return layout{}, fmt.Errorf("ffprobe sees %d video streams in %s but mkvmerge sees %d", len(l.video), file, mkvVideo)
	}
	return l, nil
}

// is reports whether s is a track of type tt. Cover art is a video stream to
// ffprobe but no track.
func (tt trackType) is(s ffprobe.Stream) bool {
	return s.CodecType == tt.name && !s.AttachedPic
}

// indexes is the ffprobe indexes of the streams of type tt, in track order.
func (l layout) indexes(tt trackType) []int {
	if tt == videoTracks {
		return l.video
	}
	return l.audio
}

// ordinal is the 1-based position of a stream among the streams of type tt,
// which is the N of mkvpropedit's track:aN or track:vN. The index must be one
// of l.indexes(tt).
func (l layout) ordinal(tt trackType, index int) int { return slices.Index(l.indexes(tt), index) + 1 }

// selector is how mkvpropedit names the ordinal-th track of type tt.
func (tt trackType) selector(ordinal int) string {
	return fmt.Sprintf("track:%c%d", tt.letter, ordinal)
}

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
