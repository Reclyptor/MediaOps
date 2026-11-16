package audit

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/reclyptor/mediaops/internal/inventory"
	"github.com/reclyptor/mediaops/internal/manifest"
	"github.com/reclyptor/mediaops/kit/ffprobe"
)

// streamAt finds a stream by its absolute ffprobe index.
func streamAt(streams []ffprobe.Stream, index int) (ffprobe.Stream, bool) {
	for _, s := range streams {
		if s.Index == index {
			return s, true
		}
	}
	return ffprobe.Stream{}, false
}

// checkFlags reads a flags op back: the chosen audio track is the only default
// audio, and the chosen subtitle track (none, for a null) the only default
// subtitle.
func checkFlags(streams []ffprobe.Stream, audio int, sub *int) []string {
	var out []string
	check := func(kind string, want *int) {
		if want != nil {
			s, ok := streamAt(streams, *want)
			switch {
			case !ok:
				out = append(out, fmt.Sprintf("stream %d, the intended default %s track, does not exist", *want, kind))
			case s.CodecType != kind:
				out = append(out, fmt.Sprintf("stream %d is %s, not %s", *want, s.CodecType, kind))
			case !s.Default:
				out = append(out, fmt.Sprintf("%s stream %d is not default", kind, *want))
			}
		}
		for _, s := range streams {
			if s.CodecType == kind && s.Default && (want == nil || s.Index != *want) {
				out = append(out, fmt.Sprintf("%s stream %d is default, and should not be", kind, s.Index))
			}
		}
	}
	check("audio", &audio)
	check("subtitle", sub)
	return out
}

// checkLangs reads an audio_lang op back from the streams' language tags.
func checkLangs(streams []ffprobe.Stream, want map[int]string) []string {
	idx := make([]int, 0, len(want))
	for i := range want {
		idx = append(idx, i)
	}
	sort.Ints(idx)
	var out []string
	for _, i := range idx {
		s, ok := streamAt(streams, i)
		switch {
		case !ok:
			out = append(out, fmt.Sprintf("stream %d does not exist", i))
		case s.CodecType != "audio":
			out = append(out, fmt.Sprintf("stream %d is %s, not audio", i, s.CodecType))
		case s.Tags["language"] != want[i]:
			out = append(out, fmt.Sprintf("audio stream %d is %q, the manifest says %q", i, s.Tags["language"], want[i]))
		}
	}
	return out
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// compareTracks lists how the tracks of a file now differ from its --before
// record, default flags aside: index, type, codec, channels, language and
// title. A language an audio_lang op was asked to set is the expected one.
func compareTracks(before []inventory.Stream, after []ffprobe.Stream, relang map[int]string) []string {
	if len(before) != len(after) {
		return []string{fmt.Sprintf("%d tracks before, %d now", len(before), len(after))}
	}
	var out []string
	for i, b := range before {
		a := after[i]
		lang := deref(b.Lang)
		if want, ok := relang[b.Index]; ok && b.Type == "audio" {
			lang = want
		}
		for _, d := range []struct {
			field   string
			was, is any
		}{
			{"index", b.Index, a.Index},
			{"type", b.Type, a.CodecType},
			{"codec", b.Codec, a.CodecName},
			{"channels", b.Channels, a.Channels},
			{"language", lang, a.Tags["language"]},
			{"title", deref(b.Title), a.Tags["title"]},
		} {
			if d.was != d.is {
				out = append(out, fmt.Sprintf("track %d %s was %v, is %v", b.Index, d.field, d.was, d.is))
			}
		}
	}
	return out
}

// signature is a file's full track layout, default flags included, as one
// comparable string.
func signature(streams []ffprobe.Stream) string {
	parts := make([]string, len(streams))
	for i, s := range streams {
		parts[i] = fmt.Sprintf("%d:%s:%s:%dch:%dx%d:%s:%q:%t", s.Index, s.CodecType, s.CodecName, s.Channels,
			s.Width, s.Height, s.Tags["language"], s.Tags["title"], s.Default)
	}
	return strings.Join(parts, " ")
}

// seasonFile is an episode landed in a Season directory and its signature.
type seasonFile struct{ path, sig string }

// seasonFindings is one finding per Season directory whose episodes do not
// share a single full track signature.
func seasonFindings(seasons map[string][]seasonFile) []Finding {
	dirs := make([]string, 0, len(seasons))
	for d := range seasons {
		dirs = append(dirs, d)
	}
	sort.Strings(dirs)

	var out []Finding
	for _, d := range dirs {
		groups := map[string][]string{}
		for _, f := range seasons[d] {
			groups[f.sig] = append(groups[f.sig], filepath.Base(f.path))
		}
		if len(groups) < 2 {
			continue
		}
		sigs := make([]string, 0, len(groups))
		for s := range groups {
			sigs = append(sigs, s)
		}
		sort.Slice(sigs, func(i, j int) bool {
			if len(groups[sigs[i]]) != len(groups[sigs[j]]) {
				return len(groups[sigs[i]]) > len(groups[sigs[j]])
			}
			return sigs[i] < sigs[j]
		})
		parts := make([]string, len(sigs))
		for i, s := range sigs {
			names := groups[s]
			sort.Strings(names)
			parts[i] = fmt.Sprintf("%d files (e.g. %s) have [%s]", len(names), names[0], s)
		}
		out = append(out, Finding{Code: "SEASON-SIGNATURE", Path: d,
			Detail: fmt.Sprintf("%d distinct track signatures: %s", len(groups), strings.Join(parts, "; "))})
	}
	return out
}

// seriesDirs is every series directory under MEDIA_ROOT that one of paths lies
// in, sorted and without duplicates, leaving out the staging tree and any
// directory that is not there (its destination check says so). It fails when
// the staging tree cannot be resolved, rather than audit it as a series.
func (d Deps) seriesDirs(paths []string) ([]string, error) {
	media := filepath.Clean(d.Paths.MediaRoot)
	staging, err := d.Paths.Staging()
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var out []string
	for _, p := range paths {
		dir, ok := manifest.SeriesDir(media, p)
		if !ok || seen[dir] {
			continue
		}
		seen[dir] = true
		if rel, err := filepath.Rel(staging, dir); err == nil && rel != ".." && !strings.HasPrefix(rel, "../") {
			continue
		}
		if isDir(dir) {
			out = append(out, dir)
		}
	}
	sort.Strings(out)
	return out, nil
}
