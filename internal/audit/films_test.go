package audit

import (
	"context"
	"slices"
	"strings"
	"syscall"
	"testing"
)

func (fx *fixture) films(roots ...string) FilmsReport {
	fx.t.Helper()
	rep, err := AuditFilms(context.Background(), fx.ff, roots, DefaultMinMinutes)
	if err != nil {
		fx.t.Fatal(err)
	}
	return rep
}

func minutes(m float64) fakeMedia {
	media := goodMedia("x")
	media.Duration = m * 60
	return media
}

func TestFilmsYearInDir(t *testing.T) {
	fx := newFixture(t)
	fx.mkdir("a/Pancreas (2018)")
	fx.mkdir("a/Plain")
	fx.mkdir("a/Year 2018 inside")
	rep := fx.films(fx.path("a"))
	if got := codes(rep.Findings); !slices.Equal(got, []string{"YEAR-IN-DIR"}) || !strings.HasSuffix(rep.Findings[0].Path, "Pancreas (2018)") {
		t.Fatalf("%v", rep.Findings)
	}
}

func TestFilmsBareFilm(t *testing.T) {
	fx := newFixture(t)
	fx.put("a/Film Series/Film.mkv", minutes(100))
	fx.put("a/Episodes/Ep1.mkv", minutes(24))
	fx.put("a/Boundary/Exact.mkv", minutes(60))
	fx.put("a/Has Movies/Long.mkv", minutes(120))
	fx.mkdir("a/Has Movies/Movies")
	rep := fx.films(fx.path("a"))
	if got := codes(rep.Findings); !slices.Equal(got, []string{"BARE-FILM", "BARE-FILM"}) {
		t.Fatalf("%v", rep.Findings)
	}
	paths := []string{rep.Findings[0].Path, rep.Findings[1].Path}
	if !strings.HasSuffix(paths[0], "Exact.mkv") || !strings.HasSuffix(paths[1], "Film.mkv") {
		t.Fatalf("paths = %v", paths)
	}
	if !strings.Contains(rep.Findings[1].Detail, "100 min") {
		t.Errorf("detail = %q", rep.Findings[1].Detail)
	}
}

func TestFilmsMinMinutesFlag(t *testing.T) {
	fx := newFixture(t)
	fx.put("SHARE/anime/S/Film.mkv", minutes(40))
	if code, _, _ := run(t, fx.deps(), "films", fx.path("SHARE/anime")); code != 0 {
		t.Fatalf("40 minutes is an episode at the default: code %d", code)
	}
	code, out, _ := run(t, fx.deps(), "films", fx.path("SHARE/anime"), "--min-minutes", "30")
	if code != 1 || !strings.Contains(out, "BARE-FILM") {
		t.Fatalf("code %d: %s", code, out)
	}
}

func TestFilmsUnreadableDurationIsNotZeroMinutes(t *testing.T) {
	fx := newFixture(t)
	bad := minutes(100)
	bad.Fail = true
	p := fx.put("a/Series/Film.mkv", bad)
	zero := minutes(0)
	zero.Duration = 0
	fx.put("a/Series2/Zero.mkv", zero)
	rep := fx.films(fx.path("a"))
	if got := codes(rep.Findings); !slices.Equal(got, []string{"UNREADABLE", "UNREADABLE"}) {
		t.Fatalf("%v", rep.Findings)
	}
	if rep.Findings[0].Path != p {
		t.Errorf("path = %q", rep.Findings[0].Path)
	}
	if rep.Clean || rep.Counts["UNREADABLE"] != 2 {
		t.Errorf("%+v", rep)
	}
}

func TestFilmsMoviesNaming(t *testing.T) {
	fx := newFixture(t)
	fx.plain("a/Series/Movies/Series - S99E01 - Film (2010).mkv")
	fx.plain("a/Series/Movies/Series - S99E02 - Other (2011) - part1.mkv")
	fx.plain("a/Series/Movies/Menu.mkv")
	fx.plain("a/Series/Movies/Film (2012) - S99E03.mkv")
	fx.plain("a/Series/Movies/Sub/Series - S99E04 - Deep (2013).mkv")
	fx.plain("a/Series/Movies/Sub/Voice Comic.mkv")
	fx.plain("a/Series/Movies/notes.txt")
	rep := fx.films(fx.path("a"))
	got := map[string]string{}
	for _, f := range rep.Findings {
		got[filepathBase(f.Path)] = f.Code
	}
	want := map[string]string{"Menu.mkv": "NO-S99", "Voice Comic.mkv": "NO-S99", "Film (2012) - S99E03.mkv": "S99-MISPLACED"}
	if len(got) != len(want) {
		t.Fatalf("got %v", got)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s: %q, want %q", k, got[k], v)
		}
	}
	if rep.Counts["NO-S99"] != 2 || rep.Counts["S99-MISPLACED"] != 1 {
		t.Errorf("counts = %v", rep.Counts)
	}
}

func TestFilmsGroupsByCode(t *testing.T) {
	fx := newFixture(t)
	fx.plain("a/Aaa/Movies/Menu.mkv")
	fx.mkdir("a/Zzz (2010)")
	rep := fx.films(fx.path("a"))
	if got := []string{rep.Findings[0].Code, rep.Findings[1].Code}; !slices.Equal(got, []string{"YEAR-IN-DIR", "NO-S99"}) {
		t.Fatalf("%v", got)
	}
}

func TestFilmsRoots(t *testing.T) {
	fx := newFixture(t)
	fx.mkdir("a/A (2010)")
	fx.mkdir("b/B (2011)")
	code, out, _ := run(t, fx.deps(), "films", fx.path("a"), fx.path("b"))
	if code != 1 || strings.Count(out, "YEAR-IN-DIR") < 2 {
		t.Fatalf("code %d: %s", code, out)
	}
	code, out, errOut := run(t, fx.deps(), "films", fx.path("nowhere"))
	if code != 2 || out != "" || !strings.Contains(errOut, "nowhere") {
		t.Fatalf("code %d, %q, %q", code, out, errOut)
	}
}

// The audits name no library folders of their own, so a call without a ROOT is
// a usage error that prints the usage line, not a walk of some default tree.
func TestAuditWithoutRootIsUsage(t *testing.T) {
	fx := newFixture(t)
	m := fx.plain("m.json")
	cases := []struct {
		name  string
		args  []string
		usage string
	}{
		{"films", []string{"films"}, usageFilms},
		{"films with only a flag", []string{"films", "--min-minutes", "30"}, usageFilms},
		{"blindspot", []string{"blindspot"}, usageBlindspot},
		{"sweep", []string{"sweep", m}, usageSweep},
		{"sweep with only a flag", []string{"sweep", m, "--min-minutes", "30"}, usageSweep},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, out, errOut := run(t, fx.deps(), tc.args...)
			if code != 2 || out != "" || !strings.Contains(errOut, tc.usage) {
				t.Fatalf("code %d, stdout %q, stderr %q; want exit 2 and %q", code, out, errOut, tc.usage)
			}
		})
	}
}

func TestFilmsNeverOpensAFIFO(t *testing.T) {
	fx := newFixture(t)
	fx.mkdir("a/Series")
	fifo := fx.path("a/Series/Film.mkv")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	if rep := fx.films(fx.path("a")); !rep.Clean || fx.ff.wasAsked(fifo) {
		t.Fatalf("%v, asked=%v", rep.Findings, fx.ff.wasAsked(fifo))
	}
}

func TestBlindspot(t *testing.T) {
	fx := newFixture(t)
	fx.plain("a/Clean/Season 01/e1.mkv")
	fx.plain("a/Berserk/b1.ogm")
	fx.plain("a/Berserk/b2.ogm")
	fx.plain("a/Berserk/b3.AVI")
	fx.plain("a/Mixed/Season 01/e1.mkv")
	fx.plain("a/Mixed/Season 01/e2.avi")
	fx.plain("a/LooseMkv/e1.mkv")
	fx.plain("a/Assets/cover.jpg")
	fx.plain("a/stray-file.mkv")
	rep, err := AuditBlindspot([]string{fx.path("a")})
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]BlindRow{}
	for _, r := range rep.Findings {
		byName[r.Series] = r
	}
	if len(byName) != 3 || rep.Clean {
		t.Fatalf("%+v", rep.Findings)
	}
	if b := byName["Berserk"]; b.MKV != 0 || b.Other != 3 || b.Loose != 3 || b.Extensions["ogm"] != 2 || b.Extensions["avi"] != 1 {
		t.Errorf("Berserk = %+v", b)
	}
	if m := byName["Mixed"]; m.MKV != 1 || m.Other != 1 || m.Loose != 0 || m.Extensions["avi"] != 1 {
		t.Errorf("Mixed = %+v", m)
	}
	if l := byName["LooseMkv"]; l.MKV != 1 || l.Other != 0 || l.Loose != 1 || l.Code != "BLINDSPOT" {
		t.Errorf("LooseMkv = %+v", l)
	}
}

func TestBlindspotCommand(t *testing.T) {
	fx := newFixture(t)
	fx.plain("SHARE/anime/Clean/Season 01/e1.mkv")
	fx.plain("SHARE/ecchi/Clean/Season 01/e1.mkv")
	roots := []string{fx.path("SHARE/anime"), fx.path("SHARE/ecchi")}
	if code, out, _ := run(t, fx.deps(), append([]string{"blindspot"}, roots...)...); code != 0 || !strings.Contains(out, `"clean": true`) {
		t.Fatalf("code %d: %s", code, out)
	}
	fx.plain("SHARE/ecchi/Old/e1.ogv")
	code, out, _ := run(t, fx.deps(), append([]string{"blindspot"}, roots...)...)
	if code != 1 || !strings.Contains(out, `"ogv": 1`) {
		t.Fatalf("an .ogv is a video: code %d: %s", code, out)
	}
	if code, _, _ := run(t, fx.deps(), "blindspot", fx.path("missing")); code != 2 {
		t.Fatalf("code %d", code)
	}
}

func filepathBase(p string) string { return p[strings.LastIndex(p, "/")+1:] }
