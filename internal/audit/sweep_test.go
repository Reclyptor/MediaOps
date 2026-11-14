package audit

import (
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

type op = map[string]any

// writeManifest writes a manifest of ops beside the media tree and returns its path.
func (fx *fixture) writeManifest(name string, ops ...op) string {
	fx.t.Helper()
	if ops == nil {
		ops = []op{}
	}
	body, err := json.Marshal(map[string]any{"ops": ops})
	if err != nil {
		fx.t.Fatal(err)
	}
	p := fx.path("plans/" + name)
	fx.mkdir("plans")
	writeFile(fx.t, p, string(body))
	return p
}

func rename(src, dst string) op { return op{"kind": "rename", "src": src, "dst": dst} }

func (fx *fixture) sweep(manifest string, args ...string) (int, SweepReport) {
	fx.t.Helper()
	code, out, errOut := run(fx.t, fx.deps(), append([]string{"sweep", manifest}, args...)...)
	var rep SweepReport
	if code <= 1 {
		if err := json.Unmarshal([]byte(out), &rep); err != nil {
			fx.t.Fatalf("code %d, %v: %s", code, err, out)
		}
	} else {
		fx.t.Logf("stderr: %s", errOut)
	}
	return code, rep
}

func TestSweepCoverage(t *testing.T) {
	fx := newFixture(t)
	anime := fx.path("SHARE/anime")
	yearDir := fx.mkdir("SHARE/anime/Old (2010)")
	renamedYear := fx.mkdir("SHARE/anime/Fixed (2011)")
	bare := fx.put("SHARE/anime/Bare/Film.mkv", minutes(100))
	covered := fx.put("SHARE/anime/Bare2/Film.mkv", minutes(100))
	short := fx.put("SHARE/anime/Eps/Ep.mkv", minutes(24))
	misnamed := fx.plain("SHARE/anime/Has/Movies/Menu.mkv")
	movedMis := fx.plain("SHARE/anime/Has/Movies/Moved.mkv")
	fine := fx.plain("SHARE/anime/Has/Movies/Has - S99E01 - Film (2010).mkv")
	_ = fine
	m := fx.writeManifest("m.json",
		op{"kind": "rename_dir", "src": renamedYear, "dst": filepath.Join(anime, "Fixed")},
		rename(covered, filepath.Join(anime, "Bare2/Movies/Bare2 - S99E01 - Film (2010).mkv")),
		rename(movedMis, filepath.Join(anime, "Has/Movies/Has - S99E02 - Moved (2010).mkv")),
	)
	code, rep := fx.sweep(m, anime)
	if code != 1 {
		t.Fatalf("code %d", code)
	}
	got := map[string]string{}
	for _, f := range rep.Findings {
		if strings.HasPrefix(f.Code, "UNCOVERED") {
			got[f.Path] = f.Code
		}
	}
	want := map[string]string{yearDir: "UNCOVERED-YEAR-IN-DIR", bare: "UNCOVERED-BARE-FILM", misnamed: "UNCOVERED-S99"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for p, c := range want {
		if got[p] != c {
			t.Errorf("%s: %q, want %q", p, got[p], c)
		}
	}
	if _, ok := got[short]; ok {
		t.Error("a 24 minute file is an episode, not a bare film")
	}
	if !slices.Contains(rep.Notes, sweepNote) || !strings.Contains(sweepNote, "mkv-manifest check --films") {
		t.Errorf("notes = %v", rep.Notes)
	}
}

func TestSweepCoveredByADirectoryOp(t *testing.T) {
	fx := newFixture(t)
	anime := fx.path("SHARE/anime")
	dir := fx.mkdir("SHARE/anime/Show")
	fx.plain("SHARE/anime/Show/Movies/Menu.mkv")
	m := fx.writeManifest("m.json", op{"kind": "move_dir", "src": dir, "dst": fx.path("SHARE/anime/Elsewhere")})
	if code, rep := fx.sweep(m, anime); code != 0 {
		t.Fatalf("code %d: %v", code, rep.Findings)
	}
}

func TestSweepStraySubdir(t *testing.T) {
	fx := newFixture(t)
	anime := fx.path("SHARE/anime")
	keep := fx.plain("SHARE/anime/Show/Movies/Leftovers/extra.mkv")
	gone := fx.plain("SHARE/anime/Show/Movies/Emptied/Show - S99E01 - Film (2010).mkv")
	fx.plain("SHARE/anime/Show/Movies/Show - S99E02 - Other (2011).mkv")
	m := fx.writeManifest("m.json",
		rename(gone, fx.path("SHARE/anime/Show/Movies/Show - S99E01 - Film (2010).mkv")),
		op{"kind": "rmdir_if_empty", "path": filepath.Dir(gone)},
	)
	_, rep := fx.sweep(m, anime)
	stray := 0
	for _, f := range rep.Findings {
		if f.Code == "STRAY-SUBDIR" {
			stray++
			if f.Path != filepath.Dir(keep) || !strings.Contains(f.Detail, "1 files remain") {
				t.Errorf("stray = %+v", f)
			}
		}
	}
	if stray != 1 {
		t.Fatalf("stray = %d: %v", stray, rep.Findings)
	}
}

func TestSweepTitles(t *testing.T) {
	cases := []struct {
		name  string
		file  string // under the series dir
		title string
		want  string // finding code, "" for none
	}{
		{"matches", "Movies/Show - S99E01 - Film (2010).mkv", "Film (2010)", ""},
		{"a part number is not part of the title", "Movies/Show - S99E01 - Film (2010) - part2.mkv", "Film (2010)", ""},
		{"a colon is spelled as a filename", "Movies/Show - S99E01 - Re - Film (2010).mkv", "Re: Film (2010)", ""},
		{"empty", "Movies/Show - S99E01 - Film (2010).mkv", "", "TITLE-EMPTY"},
		{"differs", "Movies/Show - S99E01 - Film (2010).mkv", "Another (2010)", "TITLE-DIFFERS"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fx := newFixture(t)
			media := goodMedia(c.title)
			p := fx.put("SHARE/anime/Show/"+c.file, media)
			fx.put("SHARE/anime/Show/Season 01/Show - S01E01 - A.mkv", goodMedia("Show"))
			m := fx.writeManifest("m.json", rename(fx.path("elsewhere/x.mkv"), fx.path("elsewhere/y.mkv")))
			_, rep := fx.sweep(m, fx.path("SHARE/anime"))
			if c.want == "" {
				if len(rep.Findings) != 0 {
					t.Fatalf("%v", rep.Findings)
				}
				return
			}
			f := find(t, rep.Findings, c.want)
			if f.Path != p {
				t.Errorf("path = %q", f.Path)
			}
		})
	}
}

func TestSweepTitlesLoneFilmShape(t *testing.T) {
	fx := newFixture(t)
	p := fx.put("SHARE/anime/Lone/Movies/Film (2010) - S99E01 - part1.mkv", goodMedia("Other (2010)"))
	m := fx.writeManifest("m.json", rename(fx.path("e/x.mkv"), fx.path("e/y.mkv")))
	_, rep := fx.sweep(m, fx.path("SHARE/anime"))
	if f := find(t, rep.Findings, "TITLE-DIFFERS"); f.Path != p || !strings.Contains(f.Detail, `"Film (2010)"`) {
		t.Fatalf("%+v", f)
	}
}

func TestSweepSkipsFilmsThePlanTouches(t *testing.T) {
	fx := newFixture(t)
	p := fx.put("SHARE/anime/Show/Movies/Show - S99E01 - Film (2010).mkv", goodMedia("Wrong"))
	m := fx.writeManifest("m.json", rename(p, fx.path("SHARE/anime/Show/Movies/Show - S99E02 - Film (2010).mkv")))
	_, rep := fx.sweep(m, fx.path("SHARE/anime"))
	for _, f := range rep.Findings {
		if strings.HasPrefix(f.Code, "TITLE") {
			t.Fatalf("a film the plan moves is not read: %+v", f)
		}
	}
	if fx.ff.wasAsked(p) {
		t.Error("probed a film the plan touches")
	}
}

func TestSweepResidue(t *testing.T) {
	fx := newFixture(t)
	anime := fx.path("SHARE/anime")
	a := fx.plain("SHARE/anime/A/Old/one.mkv")
	b := fx.plain("SHARE/anime/B/Old/one.mkv")
	c := fx.plain("SHARE/anime/C/Old/one.mkv")
	fx.plain("SHARE/anime/C/Old/two.mkv")
	d := fx.plain("SHARE/anime/D/Movies/one.mkv")
	dst := func(s string) string {
		return fx.path("SHARE/anime/" + s + "/Movies/" + s + " - S99E01 - F (2010).mkv")
	}
	m := fx.writeManifest("m.json",
		rename(a, dst("A")),
		rename(b, dst("B")),
		rename(c, dst("C")),
		rename(d, dst("D")),
		op{"kind": "rmdir_if_empty", "path": filepath.Dir(b)},
	)
	_, rep := fx.sweep(m, anime)
	var residue []string
	for _, f := range rep.Findings {
		if f.Code == "RESIDUE" {
			residue = append(residue, f.Path)
		}
	}
	// A's Old/ is emptied and never removed; B's is removed by the plan; C's
	// still holds a file; D's Movies/ is refilled by the plan's own rename.
	if want := []string{filepath.Dir(a)}; !slices.Equal(residue, want) {
		t.Fatalf("residue = %v, want %v", residue, want)
	}
}

func TestSweepResidueCoveredByDirectoryMove(t *testing.T) {
	fx := newFixture(t)
	dir := fx.mkdir("SHARE/anime/Show/Old")
	f := fx.plain("SHARE/anime/Show/Old/Show - S99E01 - F (2010).mkv")
	m := fx.writeManifest("m.json",
		rename(f, fx.path("SHARE/anime/Show/Movies/Show - S99E01 - F (2010).mkv")),
		op{"kind": "rename_dir", "src": dir, "dst": fx.path("SHARE/anime/Show/Older")},
	)
	_, rep := fx.sweep(m, fx.path("SHARE/anime"))
	if count(rep.Findings, "RESIDUE") != 0 {
		t.Fatalf("%v", rep.Findings)
	}
}

func TestSweepReportsSchemaFindingsAndGoesOn(t *testing.T) {
	fx := newFixture(t)
	fx.mkdir("SHARE/anime/Old (2010)")
	m := fx.writeManifest("m.json", op{"kind": "rename", "src": "relative.mkv", "dst": fx.path("x.mkv"), "bogus": 1})
	code, rep := fx.sweep(m, fx.path("SHARE/anime"))
	if code != 1 || count(rep.Findings, "SCHEMA") < 2 || count(rep.Findings, "UNCOVERED-YEAR-IN-DIR") != 1 {
		t.Fatalf("code %d: %v", code, rep.Findings)
	}
	if f := find(t, rep.Findings, "SCHEMA"); f.Op == nil || *f.Op != 1 {
		t.Errorf("op = %v", f.Op)
	}
}

func TestSweepUsage(t *testing.T) {
	fx := newFixture(t)
	if code, _, _ := run(t, fx.deps(), "sweep"); code != 2 {
		t.Errorf("no manifest: code %d", code)
	}
	if code, _, _ := run(t, fx.deps(), "sweep", fx.path("missing.json"), fx.path("x")); code != 2 {
		t.Errorf("missing manifest: code %d", code)
	}
	m := fx.writeManifest("m.json")
	if code, _, errOut := run(t, fx.deps(), "sweep", m, fx.path("no-root")); code != 2 || !strings.Contains(errOut, "no-root") {
		t.Errorf("missing root: code %d: %s", code, errOut)
	}
}
