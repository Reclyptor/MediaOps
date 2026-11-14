package audit

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"

	"github.com/reclyptor/mediaops/internal/library"
	"github.com/reclyptor/mediaops/kit/ffprobe"
	"github.com/reclyptor/mediaops/kit/proc"
)

// DefaultMinMinutes is how long a loose file must run to count as a film.
const DefaultMinMinutes = 60.0

// FilmsReport is the films audit over a set of category roots.
type FilmsReport struct {
	Roots    []string       `json:"roots"`
	Findings []Finding      `json:"findings"`
	Counts   map[string]int `json:"counts"`
	Clean    bool           `json:"clean"`
}

var filmCodes = []string{"YEAR-IN-DIR", "BARE-FILM", "UNREADABLE", "NO-S99", "S99-MISPLACED"}

// AuditFilms finds film entries that break the library's Movies/ convention,
//
//	<Series>/Movies/<Series> - S99E01 - <Film Title> (Year).mkv
//
// with the year in the film's name, never the series directory's. A loose file
// in a series root is told from a misfiled episode by duration: 24 minutes is
// an episode, 100 is a film. Duration is probed only for those candidates, so
// the audit stays cheap on a library of hundreds of series, and a duration that
// cannot be read is UNREADABLE rather than 0 minutes, which would pass.
func AuditFilms(ctx context.Context, r proc.Runner, roots []string, minMinutes float64) (FilmsReport, error) {
	rep := FilmsReport{Roots: roots, Findings: []Finding{}, Counts: map[string]int{}}
	for _, root := range roots {
		series, err := subdirs(root)
		if err != nil {
			return FilmsReport{}, err
		}
		for _, s := range series {
			f, err := auditFilmSeries(ctx, r, filepath.Join(root, s.Name()), minMinutes)
			if err != nil {
				return FilmsReport{}, err
			}
			rep.Findings = append(rep.Findings, f...)
		}
	}
	rank := func(code string) int { return slices.Index(filmCodes, code) }
	sort.SliceStable(rep.Findings, func(i, j int) bool { return rank(rep.Findings[i].Code) < rank(rep.Findings[j].Code) })
	for _, f := range rep.Findings {
		rep.Counts[f.Code]++
	}
	rep.Clean = len(rep.Findings) == 0
	return rep, nil
}

func auditFilmSeries(ctx context.Context, r proc.Runner, dir string, minMinutes float64) ([]Finding, error) {
	var out []Finding
	name := filepath.Base(dir)
	if yearInDir.MatchString(name) {
		out = append(out, Finding{Code: "YEAR-IN-DIR", Path: dir, Detail: "the series directory carries (YYYY); the year belongs in the film's filename"})
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	movies := filepath.Join(dir, "Movies")
	hasMovies := slices.ContainsFunc(entries, func(e os.DirEntry) bool { return e.Name() == "Movies" && e.IsDir() })
	if !hasMovies {
		for _, e := range entries {
			if !e.Type().IsRegular() || !library.IsVideo(e.Name()) {
				continue
			}
			p := filepath.Join(dir, e.Name())
			secs, err := ffprobe.Duration(ctx, r, p)
			switch {
			case ctx.Err() != nil:
				return nil, ctx.Err()
			case err != nil:
				out = append(out, Finding{Code: "UNREADABLE", Path: p, Detail: err.Error()})
			case secs/60 >= minMinutes:
				out = append(out, Finding{Code: "BARE-FILM", Path: p,
					Detail: fmt.Sprintf("%.0f min, a film-length file loose in the series root with no Movies/", secs/60)})
			}
		}
		return out, nil
	}

	err = filepath.WalkDir(movies, func(p string, d fs.DirEntry, err error) error {
		switch {
		case err != nil:
			return err
		case d.IsDir() || !library.IsVideo(d.Name()):
			return nil
		case s99Canon.MatchString(d.Name()):
			return nil
		case s99Any.MatchString(d.Name()):
			out = append(out, Finding{Code: "S99-MISPLACED", Path: p, Detail: "S99EXX is not in the canonical ' - S99EXX - ' position"})
		default:
			out = append(out, Finding{Code: "NO-S99", Path: p, Detail: "no S99EXX number; an extra misfiled into Movies/ belongs under Extras/"})
		}
		return nil
	})
	return out, err
}
