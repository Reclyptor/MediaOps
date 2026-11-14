package manifest

import (
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/reclyptor/mediaops/internal/library"
)

var (
	// A series folder's film: <Series> - S99EXX - <Title (YYYY)>[ - partN].ext
	seriesFilm = regexp.MustCompile(`^(.+?) - S99E(\d{2}) - (.+?)(?: - part\d+)?\.[^.]+$`)
	// A non-series folder's film: <Title (YYYY)> - S99EXX[ - partN].ext
	loneFilm  = regexp.MustCompile(`^(.+?) - S99E(\d{2})(?: - part\d+)?\.[^.]+$`)
	filmYear  = regexp.MustCompile(`\((19|20)\d{2}\)`)
	partNamed = regexp.MustCompile(` - part\d+\.[^.]+$`)
)

// auditFilms is manifest-simulate.py's post-state audit, read through the
// overlay so sources in staging count: for every series folder
// MEDIA_ROOT/<share>/<category>/<series> whose Movies/ an op touches, every
// entry of Movies/ must be a correctly named video and the S99 numbers must
// run 1..max.
func (c Config) auditFilms(m *Manifest, ov *overlay) []Finding {
	media := filepath.Clean(c.Paths.MediaRoot)
	series := map[string]bool{}
	for _, op := range m.Ops {
		for _, p := range []string{op.Src, op.Dst, op.ArchiveDst, op.Path} {
			if dir, ok := seriesOf(media, p); ok {
				series[dir] = true
			}
		}
	}
	dirs := make([]string, 0, len(series))
	for d := range series {
		dirs = append(dirs, d)
	}
	sort.Strings(dirs)

	var findings []Finding
	for _, dir := range dirs {
		findings = append(findings, auditSeries(dir, ov)...)
	}
	return findings
}

// SeriesDir is the series folder MEDIA_ROOT/<share>/<category>/<series> that p
// is or lies under. It reports false for anything shallower or outside media.
func SeriesDir(media, p string) (string, bool) {
	if p == "" || !strictlyWithin(media, p) {
		return "", false
	}
	rel, _ := filepath.Rel(media, p)
	parts := strings.SplitN(rel, string(filepath.Separator), 4)
	if len(parts) < 3 {
		return "", false
	}
	return filepath.Join(media, parts[0], parts[1], parts[2]), true
}

// FilmTitle is the "Title (YYYY)" a Movies/ file name carries, in a series
// folder's shape or, when lone, in a non-series folder's. A ` - partN` suffix
// is not part of it.
func FilmTitle(name string, lone bool) (string, bool) {
	if lone {
		if m := loneFilm.FindStringSubmatch(name); m != nil {
			return m[1], true
		}
		return "", false
	}
	if m := seriesFilm.FindStringSubmatch(name); m != nil {
		return m[3], true
	}
	return "", false
}

// seriesOf is the series folder p is, or is under Movies/ of, three levels
// below MEDIA_ROOT.
func seriesOf(media, p string) (string, bool) {
	dir, ok := SeriesDir(media, p)
	if !ok {
		return "", false
	}
	if p == dir {
		return dir, true
	}
	rel, _ := filepath.Rel(dir, p)
	if rel == "Movies" || strings.HasPrefix(rel, "Movies"+string(filepath.Separator)) {
		return dir, true
	}
	return "", false
}

func auditSeries(dir string, ov *overlay) []Finding {
	movies := filepath.Join(dir, "Movies")
	if !ov.isDir(movies) {
		return nil
	}
	name := filepath.Base(dir)
	lone := isLoneFilmDir(dir, ov)

	var findings []Finding
	flag := func(path, tag, format string, args ...any) {
		findings = append(findings, Finding{Path: path, Problem: tag + ": " + fmt.Sprintf(format, args...)})
	}

	kids := ov.children(movies)
	names := make([]string, 0, len(kids))
	for k := range kids {
		names = append(names, k)
	}
	sort.Strings(names)

	numbers := map[int][]string{}
	for _, f := range names {
		path := filepath.Join(movies, f)
		switch {
		case ov.isDir(path):
			flag(path, "STRAY-SUBDIR", "%s has a subdirectory %s", movies, f)
			continue
		case !library.IsVideo(f):
			flag(path, "NON-VIDEO", "%s is not a video", f)
			continue
		}

		var num, title string
		if lone {
			mm := loneFilm.FindStringSubmatch(f)
			if mm == nil {
				flag(path, "SHAPE", "%s is not <Title (YYYY)> - S99EXX[ - partN].ext", f)
				continue
			}
			title, num = mm[1], mm[2]
		} else {
			mm := seriesFilm.FindStringSubmatch(f)
			if mm == nil {
				flag(path, "SHAPE", "%s is not %s - S99EXX - <Title (YYYY)>[ - partN].ext", f, name)
				continue
			}
			if mm[1] != name {
				flag(path, "PREFIX", "%s starts with %q, not the series folder name %q", f, mm[1], name)
			}
			title, num = mm[3], mm[2]
		}
		if !filmYear.MatchString(title) {
			flag(path, "NOYEAR", "%s has no (YYYY) in its title", f)
		}
		n, _ := strconv.Atoi(num)
		numbers[n] = append(numbers[n], f)
	}

	nums := make([]int, 0, len(numbers))
	for n, files := range numbers {
		nums = append(nums, n)
		if len(files) > 1 && !allParts(files) {
			flag(movies, "DUPNUM", "S99E%02d is held by %d files that are not all partN: %s", n, len(files), strings.Join(files, "; "))
		}
	}
	sort.Ints(nums)
	if len(nums) > 0 {
		var missing []string
		for n := 1; n <= nums[len(nums)-1]; n++ {
			if _, ok := numbers[n]; !ok {
				missing = append(missing, fmt.Sprintf("S99E%02d", n))
			}
		}
		if len(missing) > 0 {
			flag(movies, "GAP", "missing %s", strings.Join(missing, ", "))
		}
	}
	return findings
}

func allParts(files []string) bool {
	for _, f := range files {
		if !partNamed.MatchString(f) {
			return false
		}
	}
	return true
}

// isLoneFilmDir is SKILL.md's non-series exception: every video outside
// Extras/ lives under Movies/.
func isLoneFilmDir(dir string, ov *overlay) bool {
	movies := filepath.Join(dir, "Movies") + "/"
	extras := filepath.Join(dir, "Extras") + "/"
	for p := range ov.filesUnder(dir) {
		if library.IsVideo(p) && !strings.HasPrefix(p, movies) && !strings.HasPrefix(p, extras) {
			return false
		}
	}
	return true
}
