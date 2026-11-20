package audit

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/reclyptor/mediaops/internal/cli"
	"github.com/reclyptor/mediaops/internal/inventory"
	"github.com/reclyptor/mediaops/internal/library"
	"github.com/reclyptor/mediaops/internal/manifest"
	"github.com/reclyptor/mediaops/kit/ffprobe"
)

// PostcheckReport is step 9 of a manifest run: whether the tree is what the
// manifest said it would be.
type PostcheckReport struct {
	Manifest string         `json:"manifest"`
	SHA256   string         `json:"sha256"`
	Ops      int            `json:"ops"`
	Done     int            `json:"done"`
	Findings []Finding      `json:"findings"`
	Notes    []Finding      `json:"notes"`
	Series   []SeriesResult `json:"series"`
	Clean    bool           `json:"clean"`
}

var seasonDir = regexp.MustCompile(`^Season \d+$`)

// lineage follows a path through the manifest's own moves. An op names a path
// as it stood when the op ran, and a later op may move it, so what the tree
// holds now is the path carried forward, and what the --before inventory holds
// is the path carried back.
type lineage []manifest.Op

func rebase(p, from, to string) (string, bool) {
	switch {
	case p == from:
		return to, true
	case strings.HasPrefix(p, from+string(filepath.Separator)):
		return to + p[len(from):], true
	}
	return p, false
}

// final is where p, as op index from-1 left it, ends up once ops[from:] ran.
// A remux sends its source to the archive and leaves its destination alone.
func (l lineage) final(p string, from int) string {
	for _, op := range l[from:] {
		switch {
		case op.Kind == manifest.KindRemux:
			p, _ = rebase(p, op.Src, op.ArchiveDst)
		case op.IsFileMove() || op.IsDirMove():
			p, _ = rebase(p, op.Src, op.Dst)
		}
	}
	return p
}

// origin is where p, as op index upTo saw it, stood before ops[:upTo] ran.
func (l lineage) origin(p string, upTo int) string {
	for i := upTo - 1; i >= 0; i-- {
		op := l[i]
		if op.IsFileMove() || op.IsDirMove() {
			p, _ = rebase(p, op.Dst, op.Src)
		}
	}
	return p
}

// subject is the file or directory an op leaves behind for the postcheck to
// examine, carried forward to where it is now; "" for an op with none.
func (l lineage) subject(i int) string {
	op := l[i]
	switch {
	case op.IsFileMove() || op.IsDirMove():
		return l.final(op.Dst, i+1)
	case op.Kind == manifest.KindTitle || op.Kind == manifest.KindFlags || op.IsLang():
		return l.final(op.Path, i+1)
	}
	return ""
}

func isRegular(p string) bool {
	info, err := os.Lstat(p)
	return err == nil && info.Mode().IsRegular()
}

func isDir(p string) bool {
	info, err := os.Lstat(p)
	return err == nil && info.IsDir()
}

func gone(p string) bool {
	_, err := os.Lstat(p)
	return os.IsNotExist(err)
}

func isMKV(p string) bool { return strings.EqualFold(filepath.Ext(p), ".mkv") }

// Postcheck reads the tree a manifest run left behind and reports every way it
// differs from the manifest: ops the journal does not call done, sources that
// remain, destinations that are missing, titles, default flags and languages
// that do not read back, containers that do not parse cleanly, tracks that
// changed against the --before inventory, a Season directory whose episodes
// disagree on their tracks, and the series audit over every series directory a
// destination lies in. Schema findings from loading the manifest are SCHEMA
// findings and the check runs on whatever was readable.
func (d Deps) Postcheck(ctx context.Context, m *manifest.Manifest, schema []manifest.Finding, j *manifest.Journal, before inventory.Inventory, workers int) (PostcheckReport, error) {
	rep := PostcheckReport{Manifest: m.Path, SHA256: m.SHA256, Ops: len(m.Ops), Findings: []Finding{}, Notes: []Finding{}, Series: []SeriesResult{}}
	for _, f := range schema {
		rep.Findings = append(rep.Findings, schemaFinding(f))
	}
	l := lineage(m.Ops)
	beforeBy := before.ByPath()

	// Which files need ffprobe: video destinations, and every file a title,
	// flags, audio_lang or video_lang op addressed. Integrity runs on mkv ones only.
	var probePaths, integrityPaths []string
	langs := map[string]map[int]string{}
	for i, op := range m.Ops {
		if !j.Done(op.N) {
			continue
		}
		rep.Done++
		s := l.subject(i)
		switch {
		case op.IsFileMove():
			if isRegular(s) && isMKV(s) {
				integrityPaths = append(integrityPaths, s)
			}
			if isRegular(s) && library.IsVideo(s) {
				probePaths = append(probePaths, s)
			}
		case op.Kind == manifest.KindTitle || op.Kind == manifest.KindFlags || op.IsLang():
			if isRegular(s) {
				probePaths = append(probePaths, s)
			}
			if op.IsLang() {
				if langs[s] == nil {
					langs[s] = map[int]string{}
				}
				for idx, lang := range op.Langs {
					langs[s][idx] = lang
				}
			}
		}
	}
	probePaths, integrityPaths = uniqueSorted(probePaths), uniqueSorted(integrityPaths)

	probes := make(map[string]probed, len(probePaths))
	results := make([]probed, len(probePaths))
	if err := forEach(ctx, workers, len(probePaths), func(i int) {
		results[i].info, results[i].err = ffprobe.Probe(ctx, d.Runner, probePaths[i])
	}); err != nil {
		return PostcheckReport{}, err
	}
	for i, p := range probePaths {
		probes[p] = results[i]
	}
	type integrity struct {
		warnings []string
		err      error
	}
	integ := make([]integrity, len(integrityPaths))
	if err := forEach(ctx, workers, len(integrityPaths), func(i int) {
		_, integ[i].warnings, integ[i].err = ffprobe.Integrity(ctx, d.Runner, integrityPaths[i])
	}); err != nil {
		return PostcheckReport{}, err
	}
	integrityOf := make(map[string]integrity, len(integrityPaths))
	for i, p := range integrityPaths {
		integrityOf[p] = integ[i]
	}

	add := func(n int, code, path, detail string) {
		rep.Findings = append(rep.Findings, opFinding(n, code, path, detail))
	}
	// readable returns the probe of s, reporting once why it cannot be had.
	reported := map[string]bool{}
	readable := func(n int, s string) (ffprobe.Info, bool) {
		pr, ok := probes[s]
		switch {
		case !ok:
			return ffprobe.Info{}, false // not there: the destination check says so
		case pr.err != nil:
			if !reported[s] {
				reported[s] = true
				add(n, "UNREADABLE", s, pr.err.Error())
			}
			return ffprobe.Info{}, false
		}
		return pr.info, true
	}

	// target is the probe of the file a title, flags, audio_lang or video_lang op edited.
	target := func(op manifest.Op, s string) (ffprobe.Info, bool) {
		if !isRegular(s) {
			add(op.N, "TARGET-MISSING", s, fmt.Sprintf("the file a %s op edited is not a regular file", op.Kind))
			return ffprobe.Info{}, false
		}
		return readable(op.N, s)
	}

	seasons := map[string][]seasonFile{}
	var subjects []string
	for i, op := range m.Ops {
		if !j.Done(op.N) {
			add(op.N, "NOT-DONE", firstOf(op.Dst, op.Path, op.Src), fmt.Sprintf("%s is not journaled done", op.Kind))
			continue
		}
		s := l.subject(i)
		if s != "" {
			subjects = append(subjects, s)
		}
		switch {
		case op.IsFileMove():
			if !gone(op.Src) {
				add(op.N, "SRC-REMAINS", op.Src, "the source still exists after a move that is journaled done")
			}
			if !isRegular(s) {
				add(op.N, "DST-MISSING", s, "the destination is not a regular file")
				continue
			}
			if op.Kind == manifest.KindRemux && !isRegular(l.final(op.ArchiveDst, i+1)) {
				add(op.N, "ARCHIVE-MISSING", op.ArchiveDst, "the archived original is not a regular file")
			}
			if op.Kind != manifest.KindRemux && op.Size != nil {
				if info, err := os.Lstat(s); err == nil && info.Size() != *op.Size {
					rep.Notes = append(rep.Notes, opFinding(op.N, "SIZE-CHANGED", s,
						fmt.Sprintf("%d bytes now, the manifest said %d; an in-place title edit may grow a file", info.Size(), *op.Size)))
				}
			}
			if ig, ok := integrityOf[s]; ok {
				switch {
				case ig.err != nil:
					add(op.N, "UNREADABLE", s, ig.err.Error())
				case len(ig.warnings) > 0:
					add(op.N, "INTEGRITY", s, strings.Join(ig.warnings, "; "))
				}
			}
			info, ok := readable(op.N, s)
			if !ok {
				continue
			}
			if op.Title != "" && info.Format.Title != op.Title {
				add(op.N, "TITLE-MISMATCH", s, fmt.Sprintf("container title %q, the manifest says %q", info.Format.Title, op.Title))
			}
			rec, have := beforeBy[l.origin(op.Src, i)]
			if !have {
				add(op.N, "NO-BEFORE", op.Src, "the --before inventory has no record of the source, so its tracks cannot be compared")
			} else {
				for _, diff := range compareTracks(rec.Streams, info.Streams, langs[s]) {
					add(op.N, "TRACKS-CHANGED", s, diff)
				}
			}
			if parent := filepath.Dir(s); seasonDir.MatchString(filepath.Base(parent)) && !slices.Contains(strings.Split(parent, string(filepath.Separator)), "Extras") {
				seasons[parent] = append(seasons[parent], seasonFile{s, signature(info.Streams)})
			}

		case op.IsDirMove():
			if !gone(op.Src) {
				add(op.N, "SRC-REMAINS", op.Src, "the source directory still exists after a move that is journaled done")
			}
			if !isDir(s) {
				add(op.N, "DST-MISSING", s, "the destination is not a directory")
			}

		case op.Kind == manifest.KindTitle:
			if info, ok := target(op, s); ok && info.Format.Title != op.Title {
				add(op.N, "TITLE-MISMATCH", s, fmt.Sprintf("container title %q, the manifest says %q", info.Format.Title, op.Title))
			}

		case op.Kind == manifest.KindFlags:
			if info, ok := target(op, s); ok {
				for _, problem := range checkFlags(info.Streams, op.AudioDefault, op.SubDefault) {
					add(op.N, "FLAGS-MISMATCH", s, problem)
				}
			}

		case op.IsLang():
			if info, ok := target(op, s); ok {
				for _, problem := range checkLangs(info.Streams, op.Langs, op.LangType()) {
					add(op.N, "LANG-MISMATCH", s, problem)
				}
			}
		}
	}

	rep.Findings = append(rep.Findings, seasonFindings(seasons)...)

	dirs, err := d.seriesDirs(subjects)
	if err != nil {
		return PostcheckReport{}, cli.Refused(err)
	}
	results2, err := AuditSeriesDirs(ctx, d.Runner, dirs, workers)
	if err != nil {
		return PostcheckReport{}, err
	}
	rep.Series = results2
	for _, sr := range results2 {
		for _, f := range sr.Findings {
			f.Code = "SERIES:" + f.Code
			rep.Findings = append(rep.Findings, f)
		}
	}
	rep.Clean = len(rep.Findings) == 0
	return rep, nil
}

func firstOf(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func uniqueSorted(in []string) []string {
	sort.Strings(in)
	return slices.Compact(in)
}
