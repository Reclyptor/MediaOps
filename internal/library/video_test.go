package library

import (
	"slices"
	"strings"
	"testing"
)

// probeTracksExts is the enumeration in anime-normalize's probe-tracks.sh, as
// of this commit. When that script learns a container, this list and videoExts
// change together, or the completeness check goes blind to it.
var probeTracksExts = []string{
	"mkv", "ogm", "avi", "mp4", "m2ts", "ts", "m4v", "wmv",
	"rmvb", "mov", "flv", "webm", "divx", "mpg", "mpeg", "vob",
}

func TestVideoExtsMatchProbeTracks(t *testing.T) {
	var got []string
	for _, e := range videoExts {
		got = append(got, strings.TrimPrefix(e, "."))
	}
	if !slices.Equal(got, probeTracksExts) {
		t.Fatalf("videoExts = %v, probe-tracks.sh has %v", got, probeTracksExts)
	}
}

func TestIsVideo(t *testing.T) {
	for _, ext := range probeTracksExts {
		for _, name := range []string{"ep." + ext, "EP." + strings.ToUpper(ext), "a.b." + ext} {
			if !IsVideo(name) {
				t.Errorf("IsVideo(%q) = false", name)
			}
		}
	}
	for _, name := range []string{
		"cover.jpg", "OST.flac", "booklet.pdf", "notes.nfo", "ep.srt", "ep.mkv.partial",
		"mkv", ".mkv.bak", "ep.mkvx", "ep", "",
	} {
		// ".mkv" alone is an extension with no name; a hidden file of that
		// name is still a video container.
		if IsVideo(name) {
			t.Errorf("IsVideo(%q) = true", name)
		}
	}
}
