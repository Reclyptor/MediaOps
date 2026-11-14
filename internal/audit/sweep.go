package audit

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/reclyptor/mediaops/internal/library"
	"github.com/reclyptor/mediaops/internal/manifest"
	"github.com/reclyptor/mediaops/kit/ffprobe"
	"github.com/reclyptor/mediaops/kit/proc"
)

// SweepReport is the manifest sweep: what the library holds that the plan
// leaves unhandled.
type SweepReport struct {
	Manifest string    `json:"manifest"`
	Roots    []string  `json:"roots"`
	Findings []Finding `json:"findings"`
	Notes    []string  `json:"notes"`
	Clean    bool      `json:"clean"`
}

// sweepNote says where the pass that is not here went.
const sweepNote = "the post-state shape, prefix, year, number and gap pass is `mkv-manifest check --films`; it is not repeated here"

// plan is a manifest as the sweep reads it: which paths its ops move and which
// directories they remove.
type plan struct {
	srcs    map[string]bool // files moved by a file op
	dirSrcs []string        // directories moved by a directory op
	renamed map[string]bool // directories renamed or moved, for YEAR-IN-DIR
	rmdirs  map[string]bool // rmdir_if_empty targets
	dsts    []string        // where file and directory ops put things
}

func newPlan(m *manifest.Manifest) plan {
	p := plan{srcs: map[string]bool{}, renamed: map[string]bool{}, rmdirs: map[string]bool{}}
	for _, op := range m.Ops {
		switch {
		case op.IsFileMove():
			p.srcs[op.Src] = true
			p.dsts = append(p.dsts, op.Dst)
		case op.IsDirMove():
			p.dirSrcs = append(p.dirSrcs, op.Src)
			p.renamed[op.Src] = true
			p.dsts = append(p.dsts, op.Dst)
		case op.Kind == manifest.KindRmdirIfEmpty:
			p.rmdirs[op.Path] = true
		}
	}
	return p
}

// moves reports whether the plan relocates path, itself or through a directory
// op above it.
func (p plan) moves(path string) bool {
	return p.srcs[path] || slices.ContainsFunc(p.dirSrcs, func(d string) bool { return strings.HasPrefix(path, d+string(filepath.Separator)) })
}

// refills reports whether the plan puts something under dir.
func (p plan) refills(dir string) bool {
	return slices.ContainsFunc(p.dsts, func(d string) bool { return strings.HasPrefix(d, dir+string(filepath.Separator)) })
}

// removes reports whether the plan accounts for dir going away: an
// rmdir_if_empty on it, or a directory op moving it or something above it.
func (p plan) removes(dir string) bool {
	return p.rmdirs[dir] || p.renamed[dir] || p.moves(dir)
}

// SweepManifest ports film-plan-sweep.py onto an mkv-manifest manifest. Its
// passes are coverage (every finding of the films audit is handled by the plan
// or explained), STRAY-SUBDIR, the container titles of films the plan does not
// touch, and RESIDUE. Schema findings from loading the manifest are reported
// as SCHEMA findings and the sweep runs on whatever was readable.
func SweepManifest(ctx context.Context, r proc.Runner, m *manifest.Manifest, schema []manifest.Finding, roots []string, minMinutes float64) (SweepReport, error) {
	rep := SweepReport{Manifest: m.Path, Roots: roots, Findings: []Finding{}, Notes: []string{sweepNote}}
	for _, f := range schema {
		rep.Findings = append(rep.Findings, schemaFinding(f))
	}

	pl := newPlan(m)
	var titles []film
	for _, root := range roots {
		series, err := subdirs(root)
		if err != nil {
			return SweepReport{}, err
		}
		for _, s := range series {
			dir := filepath.Join(root, s.Name())
			found, films, err := sweepSeries(ctx, r, pl, dir, minMinutes)
			if err != nil {
				return SweepReport{}, err
			}
			rep.Findings = append(rep.Findings, found...)
			titles = append(titles, films...)
		}
	}

	titleFindings, err := sweepTitles(ctx, r, titles)
	if err != nil {
		return SweepReport{}, err
	}
	rep.Findings = append(rep.Findings, titleFindings...)

	residue, err := sweepResidue(pl)
	if err != nil {
		return SweepReport{}, err
	}
	rep.Findings = append(rep.Findings, residue...)
	rep.Clean = len(rep.Findings) == 0
	return rep, nil
}

func schemaFinding(f manifest.Finding) Finding {
	out := Finding{Code: "SCHEMA", Path: f.Path, Detail: f.Problem, Op: f.Op}
	if f.Kind != "" {
		out.Detail = f.Kind + ": " + f.Problem
	}
	return out
}

// sweepSeries is the coverage and stray-subdirectory passes for one series
// directory. It also returns the Movies/ films the plan leaves where they are,
// whose titles get read afterwards.
func sweepSeries(ctx context.Context, r proc.Runner, pl plan, dir string, minMinutes float64) (found []Finding, untouched []film, err error) {
	name := filepath.Base(dir)
	if yearInDir.MatchString(name) && !pl.renamed[dir] {
		found = append(found, Finding{Code: "UNCOVERED-YEAR-IN-DIR", Path: dir, Detail: "the series directory carries (YYYY) and the plan does not rename it"})
	}

	movies := filepath.Join(dir, "Movies")
	if info, err := os.Lstat(movies); err != nil || !info.IsDir() {
		bare, err := sweepBare(ctx, r, pl, dir, minMinutes)
		return append(found, bare...), nil, err
	}

	lone, err := isLoneFilmDir(dir)
	if err != nil {
		return nil, nil, err
	}
	err = filepath.WalkDir(movies, func(p string, d fs.DirEntry, err error) error {
		switch {
		case err != nil:
			return err
		case d.IsDir():
			if filepath.Dir(p) == movies {
				left, err := leftBehind(pl, p)
				if err != nil {
					return err
				}
				if left > 0 {
					found = append(found, Finding{Code: "STRAY-SUBDIR", Path: p,
						Detail: fmt.Sprintf("%d files remain in it that the plan does not move", left)})
				}
			}
			return nil
		case !library.IsVideo(d.Name()):
			return nil
		case !d.Type().IsRegular() || pl.moves(p):
			return nil
		}
		if !s99Canon.MatchString(d.Name()) {
			found = append(found, Finding{Code: "UNCOVERED-S99", Path: p, Detail: "a Movies/ file with no canonical S99EXX name that the plan does not move"})
		}
		if title, ok := manifest.FilmTitle(d.Name(), lone); ok {
			untouched = append(untouched, film{path: p, title: title})
		}
		return nil
	})
	return found, untouched, err
}

// sweepBare is UNCOVERED-BARE-FILM: a film-length file loose in a series root
// with no Movies/ that the plan does not move.
func sweepBare(ctx context.Context, r proc.Runner, pl plan, dir string, minMinutes float64) ([]Finding, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []Finding
	for _, e := range entries {
		p := filepath.Join(dir, e.Name())
		if !e.Type().IsRegular() || !library.IsVideo(e.Name()) || pl.moves(p) {
			continue
		}
		secs, err := ffprobe.Duration(ctx, r, p)
		switch {
		case ctx.Err() != nil:
			return nil, ctx.Err()
		case err != nil:
			out = append(out, Finding{Code: "UNREADABLE", Path: p, Detail: err.Error()})
		case secs/60 >= minMinutes:
			out = append(out, Finding{Code: "UNCOVERED-BARE-FILM", Path: p,
				Detail: fmt.Sprintf("%.0f min loose in the series root with no Movies/, and the plan does not move it", secs/60)})
		}
	}
	return out, nil
}

// leftBehind counts the files under dir the plan does not move.
func leftBehind(pl plan, dir string) (int, error) {
	n := 0
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && !pl.moves(p) {
			n++
		}
		return err
	})
	return n, err
}

// isLoneFilmDir is SKILL.md's non-series exception: every video outside
// Extras/ lives under Movies/.
func isLoneFilmDir(dir string) (bool, error) {
	tree, err := walkVideos(dir)
	if err != nil {
		return false, err
	}
	movies := filepath.Join(dir, "Movies") + "/"
	extras := filepath.Join(dir, "Extras") + "/"
	return !slices.ContainsFunc(tree.files, func(p string) bool {
		return !strings.HasPrefix(p, movies) && !strings.HasPrefix(p, extras)
	}), nil
}

// film is a Movies/ file the plan leaves alone and the title its name carries.
type film struct {
	path, title string
}

// sweepTitles checks the container title of each film against the title its
// filename carries.
func sweepTitles(ctx context.Context, r proc.Runner, films []film) ([]Finding, error) {
	results := make([]probed, len(films))
	if err := forEach(ctx, DefaultWorkers, len(films), func(i int) {
		results[i].info, results[i].err = ffprobe.Probe(ctx, r, films[i].path)
	}); err != nil {
		return nil, err
	}

	var out []Finding
	for i, f := range films {
		switch title := results[i].info.Format.Title; {
		case results[i].err != nil:
			out = append(out, Finding{Code: "UNREADABLE", Path: f.path, Detail: results[i].err.Error()})
		case title == "":
			out = append(out, Finding{Code: "TITLE-EMPTY", Path: f.path, Detail: fmt.Sprintf("no container title, want %q", f.title)})
		case !titleMatches(title, f.title):
			out = append(out, Finding{Code: "TITLE-DIFFERS", Path: f.path, Detail: fmt.Sprintf("container title %q, the filename says %q", title, f.title)})
		}
	}
	return out, nil
}

// sweepResidue is RESIDUE: a directory the plan's file ops empty that nothing
// in the plan removes or refills.
func sweepResidue(pl plan) ([]Finding, error) {
	dirs := map[string]bool{}
	for src := range pl.srcs {
		dirs[filepath.Dir(src)] = true
	}
	sorted := make([]string, 0, len(dirs))
	for d := range dirs {
		sorted = append(sorted, d)
	}
	sort.Strings(sorted)

	var out []Finding
	for _, d := range sorted {
		info, err := os.Lstat(d)
		if err != nil || !info.IsDir() {
			continue
		}
		left, err := leftBehind(pl, d)
		if err != nil {
			return nil, err
		}
		if left == 0 && !pl.removes(d) && !pl.refills(d) {
			out = append(out, Finding{Code: "RESIDUE", Path: d, Detail: "the plan empties this directory and neither removes it, moves it nor puts anything in it"})
		}
	}
	return out, nil
}
