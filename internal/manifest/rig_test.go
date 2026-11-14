package manifest

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/reclyptor/mediaops/kit/paths"
)

// rig is a media tree, an archive and a plan directory in a temp dir. Each
// share (nas, nas2) has a device of its own, distinct from "/", so a move
// across them takes the cross-device path and ARCHIVE_ROOT passes its guard.
type rig struct {
	t       *testing.T
	media   string
	archive string
	plans   string
	cfg     Config
}

func newRig(t *testing.T) *rig {
	t.Helper()
	base := t.TempDir()
	r := &rig{
		t:       t,
		media:   filepath.Join(base, "media"),
		archive: filepath.Join(base, "media", "nas2", "archive"),
		plans:   filepath.Join(base, "plans"),
	}
	for _, d := range []string{r.media, r.archive, r.plans} {
		if err := os.MkdirAll(d, 0o770); err != nil {
			t.Fatal(err)
		}
	}
	r.cfg = Config{
		Paths:   paths.Config{MediaRoot: r.media, Archive: r.archive, StatDev: r.dev},
		LockDir: filepath.Join(base, "locks"),
		Version: "test",
	}
	if err := os.MkdirAll(r.cfg.LockDir, 0o770); err != nil {
		t.Fatal(err)
	}
	return r
}

func (r *rig) dev(path string) (uint64, error) {
	switch p := filepath.Clean(path); {
	case within(filepath.Join(r.media, "nas2"), p):
		return 11, nil
	case within(filepath.Join(r.media, "nas"), p):
		return 10, nil
	}
	return 1, nil
}

// path joins rel onto MEDIA_ROOT.
func (r *rig) path(rel string) string { return filepath.Join(r.media, rel) }

// put writes a file under MEDIA_ROOT and returns its path.
func (r *rig) put(rel, content string) string {
	r.t.Helper()
	return putFile(r.t, r.path(rel), content)
}

func (r *rig) mkdir(rel string) string {
	r.t.Helper()
	p := r.path(rel)
	if err := os.MkdirAll(p, 0o770); err != nil {
		r.t.Fatal(err)
	}
	return p
}

func putFile(t *testing.T, path, content string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o770); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o660); err != nil {
		t.Fatal(err)
	}
	return path
}

// manifestJSON writes a manifest file in the plan dir and loads it.
func (r *rig) manifestJSON(name, body string) (*Manifest, []Finding, string) {
	r.t.Helper()
	path := putFile(r.t, filepath.Join(r.plans, name), body)
	m, findings, err := Load(path)
	if err != nil {
		r.t.Fatalf("Load: %v", err)
	}
	return m, findings, path
}

// manifestOps marshals ops into a manifest file and loads it.
func (r *rig) manifestOps(ops ...Op) (*Manifest, []Finding, string) {
	r.t.Helper()
	if ops == nil {
		ops = []Op{}
	}
	body, err := json.Marshal(map[string]any{"ops": ops})
	if err != nil {
		r.t.Fatal(err)
	}
	return r.manifestJSON("m.json", string(body))
}

// check loads ops and runs a check with no journal.
func (r *rig) check(opts CheckOptions, ops ...Op) CheckReport {
	r.t.Helper()
	m, findings, _ := r.manifestOps(ops...)
	rep, err := r.cfg.Check(m, findings, nil, opts)
	if err != nil {
		r.t.Fatalf("Check: %v", err)
	}
	return rep
}

func i64(v int64) *int64 { return &v }

func intp(v int) *int { return &v }

// problems is the findings' problem texts, for readable assertions.
func problems(rep CheckReport) []string {
	out := make([]string, len(rep.Findings))
	for i, f := range rep.Findings {
		out[i] = f.Problem
	}
	return out
}

// hasProblem reports whether some finding on op (0 = any) says substr.
func hasProblem(rep CheckReport, op int, substr string) bool {
	for _, f := range rep.Findings {
		if (op == 0 || (f.Op != nil && *f.Op == op)) && strings.Contains(f.Problem, substr) {
			return true
		}
	}
	return false
}

// snapshot records every path under dir with its mode, size and mtime, to
// prove a read-only operation changed nothing.
func snapshot(t *testing.T, dir string) map[string]string {
	t.Helper()
	snap := map[string]string{}
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		snap[p] = fmt.Sprintf("%v %d %d", info.Mode(), info.Size(), info.ModTime().UnixNano())
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return snap
}

// fixtureSeries finds `<series> - S99E` films, so a fixture's series folders
// have an episode outside Movies/ and are audited as series, not as the
// non-series exception.
var fixtureSeries = regexp.MustCompile(`^(.+?)/Movies/([^/]+) - S99E\d\d`)

// buildFixture copies testdata/<name>.json into a temp plan dir with
// /mnt/media replaced by a temp MEDIA_ROOT, and creates every source the
// manifest needs that no earlier op produces: a file for each file op and
// mkvpropedit target, a directory with two files for each directory op, an
// empty directory for each rmdir_if_empty target, and one episode under
// Season 01 for every series folder whose Movies/ the manifest fills, which
// keeps --films from taking it for a non-series folder. A fresh fixture
// therefore checks clean. content gives a file's bytes from its path; nil
// writes a small placeholder. It returns the Config and the manifest path.
func buildFixture(t *testing.T, name string, content func(path string) []byte) (Config, string) {
	t.Helper()
	return buildFixtureWith(t, name, content, nil)
}

// buildFixtureRel is buildFixture with the file bytes chosen by the path
// relative to MEDIA_ROOT, so two trees built in different temp dirs hold
// identical bytes wherever the same file starts.
func buildFixtureRel(t *testing.T, name string, contentRel func(rel string) []byte) (Config, string) {
	t.Helper()
	return buildFixtureWith(t, name, nil, contentRel)
}

func buildFixtureWith(t *testing.T, name string, content func(path string) []byte, contentRel func(rel string) []byte) (Config, string) {
	t.Helper()
	r := newRig(t)
	raw, err := os.ReadFile(filepath.Join("testdata", name+".json"))
	if err != nil {
		t.Fatal(err)
	}
	body := strings.ReplaceAll(string(raw), "/mnt/media", r.media)
	path := putFile(t, filepath.Join(r.plans, name+".json"), body)
	m, findings, err := Load(path)
	if err != nil || len(findings) > 0 {
		t.Fatalf("fixture %s does not load clean: %v %v", name, err, findings)
	}

	b := fixtureBuilder{t: t, r: r, ops: m.Ops, content: content, contentRel: contentRel}
	b.seedSeries()
	b.satisfy()
	return r.cfg, path
}

type fixtureBuilder struct {
	t       *testing.T
	r       *rig
	ops     []Op
	content func(string) []byte
	// contentRel, when set, wins over content and gets the path under MEDIA_ROOT.
	contentRel func(string) []byte
}

func (b fixtureBuilder) writeFile(p string) {
	b.t.Helper()
	if exists(p) {
		b.t.Fatalf("fixture needs %s, which already exists but is not visible to the replay", p)
	}
	data := []byte("placeholder " + filepath.Base(p))
	switch {
	case b.contentRel != nil:
		rel, err := filepath.Rel(b.r.media, p)
		if err != nil {
			b.t.Fatal(err)
		}
		data = b.contentRel(rel)
	case b.content != nil:
		data = b.content(p)
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o770); err != nil {
		b.t.Fatal(err)
	}
	if err := os.WriteFile(p, data, 0o660); err != nil {
		b.t.Fatal(err)
	}
}

func (b fixtureBuilder) writeDir(p string, files bool) {
	b.t.Helper()
	if exists(p) {
		b.t.Fatalf("fixture needs the directory %s, which already exists but is not visible to the replay", p)
	}
	if err := os.MkdirAll(p, 0o770); err != nil {
		b.t.Fatal(err)
	}
	if files {
		b.writeFile(filepath.Join(p, "one.mkv"))
		b.writeFile(filepath.Join(p, "two.mkv"))
	}
}

// origin is where p has to be created before the run for it to be at p after
// the directory moves ahead of op `upto`.
func (b fixtureBuilder) origin(p string, upto int) string {
	for j := upto - 1; j >= 0; j-- {
		if op := b.ops[j]; op.IsDirMove() && within(op.Dst, p) {
			p = filepath.Join(op.Src, strings.TrimPrefix(p, op.Dst))
		}
	}
	return p
}

func (b fixtureBuilder) seedSeries() {
	seen := map[string]bool{}
	for _, op := range b.ops {
		for _, p := range []string{op.Dst, op.Path} {
			rel, err := filepath.Rel(b.r.media, p)
			if p == "" || err != nil {
				continue
			}
			// <share>/<category>/<series>/Movies/<series> - S99E..
			parts := strings.SplitN(rel, "/", 3)
			if len(parts) < 3 {
				continue
			}
			mm := fixtureSeries.FindStringSubmatch(parts[2])
			if mm == nil || filepath.Base(mm[1]) != mm[2] || seen[mm[1]] {
				continue
			}
			seen[mm[1]] = true
			dir := filepath.Join(b.r.media, parts[0], parts[1], mm[1])
			ep := b.origin(filepath.Join(dir, "Season 01", mm[2]+" - S01E01.mkv"), len(b.ops))
			if !exists(ep) {
				b.writeFile(ep)
			}
		}
	}
}

// satisfy replays the ops on an overlay and, whenever one needs something the
// tree lacks, creates it at its origin and starts over.
func (b fixtureBuilder) satisfy() {
	for round := 0; round < 100000; round++ {
		if !b.replayOnce() {
			return
		}
	}
	b.t.Fatal("fixture did not settle")
}

// replayOnce returns true when it had to create something.
func (b fixtureBuilder) replayOnce() bool {
	ov := newOverlay()
	for i, op := range b.ops {
		switch {
		case op.IsFileMove():
			if !ov.isFile(op.Src) {
				b.writeFile(b.origin(op.Src, i))
				return true
			}
			ov.moveFile(op.Src, op.Dst)
			if op.Kind == KindRemux {
				ov.added[op.ArchiveDst] = entry{size: ov.size(op.Dst)}
			}
		case op.IsDirMove():
			if !ov.isDir(op.Src) {
				b.writeDir(b.origin(op.Src, i), true)
				return true
			}
			ov.moveDir(op.Src, op.Dst, false)
		case op.Kind == KindRmdirIfEmpty:
			if !ov.isDir(op.Path) {
				if p := b.origin(op.Path, i); !exists(p) {
					b.writeDir(p, false)
					return true
				}
				continue
			}
			if len(ov.children(op.Path)) == 0 {
				ov.removeDir(op.Path)
			}
		default:
			if !ov.isFile(op.Path) {
				b.writeFile(b.origin(op.Path, i))
				return true
			}
		}
	}
	return false
}
