package audit

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/reclyptor/mediaops/internal/library"
	"github.com/reclyptor/mediaops/kit/ffprobe"
	"github.com/reclyptor/mediaops/kit/proc"
)

// SeriesResult is one series directory's audit.
type SeriesResult struct {
	Dir      string    `json:"dir"`
	Name     string    `json:"name"`
	Files    int       `json:"files"`
	Findings []Finding `json:"findings"`
	Clean    bool      `json:"clean"`
	// Skipped names the video-named entries that were not regular files and so
	// were never opened.
	Skipped []string `json:"skipped,omitempty"`
}

// probed is one mkv's container title and streams, or why they could not be
// read.
type probed struct {
	info ffprobe.Info
	err  error
}

// AuditSeries checks one series directory against the promoted-series rules.
// Enumeration is by video, never by ".mkv": a series of 25 .ogm episodes loose
// in its root once read as EMPTY, and the proposed fix was to write 25 new
// episodes over a rip the user already had. The track conventions are
// mkvpropedit concepts, so TITLE, AUDIO and SUBS apply to .mkv only, and the
// files they cannot cover are named by LEGACY instead of silently dropped.
func AuditSeries(ctx context.Context, r proc.Runner, dir string, workers int) (SeriesResult, error) {
	name := filepath.Base(dir)
	tree, err := walkVideos(dir)
	if err != nil {
		return SeriesResult{}, fmt.Errorf("walking %s: %w", dir, err)
	}
	res := SeriesResult{Dir: dir, Name: name, Files: len(tree.files), Skipped: tree.skipped, Findings: []Finding{}}
	if len(tree.files) == 0 {
		res.Findings = append(res.Findings, Finding{Code: "EMPTY", Path: dir, Detail: "no video files"})
		return res, nil
	}

	var mkvs, legacy []string
	for _, f := range tree.files {
		if strings.EqualFold(filepath.Ext(f), ".mkv") {
			mkvs = append(mkvs, f)
		} else {
			legacy = append(legacy, f)
		}
	}
	add := func(code, path, detail, scope string) {
		res.Findings = append(res.Findings, Finding{Code: code, Path: path, Detail: detail, Scope: scope})
	}

	if len(legacy) > 0 {
		exts := map[string]bool{}
		for _, f := range legacy {
			exts[strings.TrimPrefix(strings.ToLower(filepath.Ext(f)), ".")] = true
		}
		names := make([]string, 0, len(exts))
		for e := range exts {
			names = append(names, e)
		}
		sort.Strings(names)
		add("LEGACY", dir, fmt.Sprintf("%d non-Matroska files (%s), track conventions not applied, e.g. %s",
			len(legacy), strings.Join(names, ","), filepath.Base(legacy[0])), "")
	}

	probes := make([]probed, len(mkvs))
	if err := forEach(ctx, workers, len(mkvs), func(i int) {
		probes[i].info, probes[i].err = ffprobe.Probe(ctx, r, mkvs[i])
	}); err != nil {
		return SeriesResult{}, err
	}
	for i, f := range mkvs {
		if probes[i].err != nil {
			add("UNREADABLE", f, probes[i].err.Error(), "")
			continue
		}
		res.Findings = append(res.Findings, checkTracks(dir, name, f, probes[i].info)...)
	}

	res.Findings = append(res.Findings, checkNaming(dir, name, tree.files)...)
	layout, err := checkLayout(dir, tree.files)
	if err != nil {
		return SeriesResult{}, err
	}
	res.Findings = append(res.Findings, layout...)
	res.Clean = len(res.Findings) == 0
	return res, nil
}

// scopeOf separates a series' Extras/ from its content by directory, relative
// to the series so a parent that happens to be named Extras cannot matter.
func scopeOf(rel string) string {
	if slices.Contains(strings.Split(filepath.Dir(rel), string(filepath.Separator)), "Extras") {
		return "extras"
	}
	return "content"
}

// checkTracks is TITLE, AUDIO-default-count, AUDIO-not-jpn and
// SUBS-default-count for one readable mkv.
func checkTracks(dir, name, path string, info ffprobe.Info) []Finding {
	rel, _ := filepath.Rel(dir, path)
	scope := scopeOf(rel)
	var out []Finding
	add := func(code, detail string) {
		out = append(out, Finding{Code: code, Path: path, Detail: detail, Scope: scope})
	}

	isMovie := slices.Contains(strings.Split(filepath.Dir(rel), string(filepath.Separator)), "Movies")
	if !isMovie && !titleMatches(info.Format.Title, name) {
		add("TITLE", fmt.Sprintf("container title %q, want %q", info.Format.Title, name))
	}

	var audio, subs []ffprobe.Stream
	for _, s := range info.Streams {
		switch s.CodecType {
		case "audio":
			audio = append(audio, s)
		case "subtitle":
			subs = append(subs, s)
		}
	}
	if len(audio) > 0 {
		defaults := defaultsOf(audio)
		switch {
		case len(defaults) != 1:
			add("AUDIO-default-count", fmt.Sprintf("%d default audio tracks, want exactly 1", len(defaults)))
		case slices.ContainsFunc(audio, func(s ffprobe.Stream) bool { return s.Tags["language"] == "jpn" }) &&
			defaults[0].Tags["language"] != "jpn":
			add("AUDIO-not-jpn", fmt.Sprintf("default audio is %q although a Japanese track exists", defaults[0].Tags["language"]))
		}
	}
	if len(subs) > 0 {
		if n := len(defaultsOf(subs)); n != 1 {
			add("SUBS-default-count", fmt.Sprintf("%d default subtitle tracks, want exactly 1", n))
		}
	}
	return out
}

func defaultsOf(streams []ffprobe.Stream) []ffprobe.Stream {
	var out []ffprobe.Stream
	for _, s := range streams {
		if s.Default {
			out = append(out, s)
		}
	}
	return out
}

// checkNaming is LOOSE, SHAPE, PREFIX and NUM. Every directory holding video
// is walked, not just Season*: a series with 25 episodes loose in its root was
// invisible to these checks, so absence of the expected folder must not mean
// absence of the check.
func checkNaming(dir, series string, files []string) []Finding {
	byDir := map[string][]string{}
	for _, f := range files {
		byDir[filepath.Dir(f)] = append(byDir[filepath.Dir(f)], filepath.Base(f))
	}
	dirs := make([]string, 0, len(byDir))
	for d := range byDir {
		dirs = append(dirs, d)
	}
	sort.Strings(dirs)

	var out []Finding
	for _, d := range dirs {
		names := byDir[d]
		sort.Strings(names)
		rel, _ := filepath.Rel(dir, d)
		top, _, _ := strings.Cut(rel, string(filepath.Separator))
		switch {
		case top == "Extras":
			continue // original filenames are correct here
		case rel == ".":
			out = append(out, Finding{Code: "LOOSE", Path: d,
				Detail: fmt.Sprintf("%d video files in the series root (want Season NN/), e.g. %s", len(names), names[0])})
			continue
		}

		var nums []float64
		for _, n := range names {
			path := filepath.Join(d, n)
			m := episodeName.FindStringSubmatch(n)
			if m == nil {
				out = append(out, Finding{Code: "SHAPE", Path: path,
					Detail: fmt.Sprintf("%s is not %s - SxxExx[ - Title].ext", filepath.Join(rel, n), series)})
				continue
			}
			if m[1] != series {
				out = append(out, Finding{Code: "PREFIX", Path: path,
					Detail: fmt.Sprintf("%s starts with %q, not the series name %q", filepath.Join(rel, n), m[1], series)})
			}
			if strings.HasPrefix(top, "Season") {
				e, _ := strconv.ParseFloat(m[3], 64)
				nums = append(nums, e)
			}
		}
		out = append(out, checkNumbers(d, rel, nums)...)
	}
	return out
}

// checkNumbers is NUM for one Season directory: episodes run 1..N with none
// missing and none repeated.
func checkNumbers(dir, rel string, nums []float64) []Finding {
	if len(nums) == 0 {
		return nil
	}
	var out []Finding
	hi := slices.Max(nums)
	have := map[int]bool{}
	seen := map[float64]bool{}
	dup := false
	for _, n := range nums {
		if n == float64(int(n)) {
			have[int(n)] = true
		}
		dup = dup || seen[n]
		seen[n] = true
	}
	var missing []string
	for n := 1; n <= int(hi); n++ {
		if !have[n] {
			missing = append(missing, strconv.Itoa(n))
		}
	}
	if len(missing) > 0 {
		shown := missing[:min(len(missing), 8)]
		out = append(out, Finding{Code: "NUM", Path: dir,
			Detail: fmt.Sprintf("%s missing %s of 1..%d", rel, strings.Join(shown, ","), int(hi))})
	}
	if dup {
		out = append(out, Finding{Code: "NUM", Path: dir, Detail: rel + " duplicate episode numbers"})
	}
	return out
}

// checkLayout is LAYOUT. Extras/Extras/ is sanctioned (SKILL.md, "Carry
// non-video assets forward"), and a subdirectory holding no video is no
// video-layout defect at all: soundtracks, scans and commentary legitimately
// live under Extras.
func checkLayout(dir string, files []string) ([]Finding, error) {
	holdsVideo := func(d string) bool {
		return slices.ContainsFunc(files, func(f string) bool { return strings.HasPrefix(f, d+string(filepath.Separator)) })
	}
	var out []Finding

	extras := filepath.Join(dir, "Extras")
	entries, err := readDirIfExists(extras)
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		p := filepath.Join(extras, e.Name())
		if e.IsDir() && e.Name() != "Movies" && e.Name() != "Extras" &&
			!strings.HasPrefix(e.Name(), "Season") && holdsVideo(p) {
			out = append(out, Finding{Code: "LAYOUT", Path: p,
				Detail: fmt.Sprintf("Extras/%s holds video (want Season NN/Disc NN/)", e.Name())})
		}
		if e.Type().IsRegular() && library.IsVideo(e.Name()) {
			out = append(out, Finding{Code: "LAYOUT", Path: extras,
				Detail: "Extras/ has loose files (want Season NN/Disc NN/)"})
			break
		}
	}

	specials := filepath.Join(dir, "Specials")
	entries, err = readDirIfExists(specials)
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		if e.Type().IsRegular() && library.IsVideo(e.Name()) {
			out = append(out, Finding{Code: "LAYOUT", Path: specials,
				Detail: "Specials/ has loose files (want Season NN/)"})
			break
		}
		if e.IsDir() && !strings.HasPrefix(e.Name(), "Season") && e.Name() != "Movies" {
			out = append(out, Finding{Code: "LAYOUT", Path: filepath.Join(specials, e.Name()),
				Detail: fmt.Sprintf("Specials/%s (want Season NN/ or Movies/)", e.Name())})
		}
	}
	return out, nil
}

// readDirIfExists lists dir sorted by name, or nothing when it is not a
// directory.
func readDirIfExists(dir string) ([]os.DirEntry, error) {
	info, err := os.Lstat(dir)
	if os.IsNotExist(err) || (err == nil && !info.IsDir()) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return os.ReadDir(dir)
}

// AuditSeriesDirs audits each directory in turn, probing each series with at
// most workers calls at once.
func AuditSeriesDirs(ctx context.Context, r proc.Runner, dirs []string, workers int) ([]SeriesResult, error) {
	out := make([]SeriesResult, 0, len(dirs))
	for _, d := range dirs {
		res, err := AuditSeries(ctx, r, d, workers)
		if err != nil {
			return nil, err
		}
		out = append(out, res)
	}
	return out, nil
}
