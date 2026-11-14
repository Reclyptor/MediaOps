package mkvtool

import (
	"context"
	"fmt"
	"maps"
	"slices"

	"github.com/reclyptor/mediaops/kit/ffprobe"
	"github.com/reclyptor/mediaops/kit/proc"
)

// SetDefaultFlags makes the audio stream with ffprobe index audio the only
// default audio, and the subtitle stream with index *sub the only default
// subtitle; a nil sub clears every subtitle default. It edits flags only, never
// a track name, and reads the file back to prove each disposition and title.
//
// It refuses, editing nothing, when audio is not an audio stream, *sub is not
// a subtitle stream, or ffprobe and mkvmerge disagree on the track counts.
func SetDefaultFlags(ctx context.Context, r proc.Runner, file string, audio int, sub *int) error {
	l, err := readLayout(ctx, r, file)
	if err != nil {
		return err
	}
	if !slices.Contains(l.audio, audio) {
		return fmt.Errorf("stream %d is not an audio stream of %s", audio, file)
	}
	if sub != nil && !slices.Contains(l.subs, *sub) {
		return fmt.Errorf("stream %d is not a subtitle stream of %s", *sub, file)
	}

	want := map[int]bool{}
	argv := []string{"mkvpropedit", file}
	for n, idx := range l.audio {
		want[idx] = idx == audio
		argv = append(argv, "--edit", fmt.Sprintf("track:a%d", n+1), "--set", "flag-default="+flagValue(want[idx]))
	}
	for n, idx := range l.subs {
		want[idx] = sub != nil && idx == *sub
		argv = append(argv, "--edit", fmt.Sprintf("track:s%d", n+1), "--set", "flag-default="+flagValue(want[idx]))
	}
	if err := runEdit(ctx, r, file, argv...); err != nil {
		return err
	}

	after, err := ffprobe.Streams(ctx, r, file)
	if err != nil {
		return err
	}
	got := map[int]bool{}
	for _, s := range after {
		if _, edited := want[s.Index]; edited && (s.CodecType == "audio" || s.CodecType == "subtitle") {
			got[s.Index] = s.Default
		}
	}
	if !maps.Equal(want, got) {
		return fmt.Errorf("the default flags of %s read back as %v after setting %v", file, got, want)
	}
	return checkTitles(file, l.streams, after)
}

func flagValue(on bool) string {
	if on {
		return "1"
	}
	return "0"
}
