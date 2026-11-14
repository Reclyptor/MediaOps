package audit

import (
	"context"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"
)

// seriesFixture is a series directory "Show" under anime with its episodes put.
func seriesFixture(t *testing.T) (*fixture, string) {
	fx := newFixture(t)
	return fx, fx.path("SHARE/anime/Show")
}

func (fx *fixture) audit(dir string) SeriesResult {
	fx.t.Helper()
	res, err := AuditSeries(context.Background(), fx.ff, dir, 4)
	if err != nil {
		fx.t.Fatal(err)
	}
	return res
}

func TestSeriesClean(t *testing.T) {
	fx, dir := seriesFixture(t)
	fx.put("SHARE/anime/Show/Season 01/Show - S01E01 - A.mkv", goodMedia("Show"))
	fx.put("SHARE/anime/Show/Season 01/Show - S01E02 - B.mkv", goodMedia("Show"))
	res := fx.audit(dir)
	if !res.Clean || len(res.Findings) != 0 || res.Files != 2 || res.Name != "Show" {
		t.Fatalf("%+v", res)
	}
}

func TestSeriesEmpty(t *testing.T) {
	fx, dir := seriesFixture(t)
	fx.plain("SHARE/anime/Show/cover.jpg")
	res := fx.audit(dir)
	if got := codes(res.Findings); !slices.Equal(got, []string{"EMPTY"}) || res.Clean {
		t.Fatalf("findings = %v", got)
	}
}

func TestSeriesLooseOgmIsLooseAndLegacyNotEmpty(t *testing.T) {
	fx, dir := seriesFixture(t)
	fx.plain("SHARE/anime/Show/Show - S01E01 - A.ogm")
	fx.plain("SHARE/anime/Show/Show - S01E02 - B.ogm")
	res := fx.audit(dir)
	if got := codes(res.Findings); !slices.Equal(got, []string{"LEGACY", "LOOSE"}) {
		t.Fatalf("findings = %v", got)
	}
	if d := find(t, res.Findings, "LEGACY").Detail; !strings.Contains(d, "2 non-Matroska files (ogm)") {
		t.Errorf("LEGACY detail = %q", d)
	}
	if d := find(t, res.Findings, "LOOSE").Detail; !strings.Contains(d, "2 video files in the series root") {
		t.Errorf("LOOSE detail = %q", d)
	}
}

func TestSeriesLegacyInSeasonIsOnlyLegacy(t *testing.T) {
	fx, dir := seriesFixture(t)
	fx.put("SHARE/anime/Show/Season 01/Show - S01E01 - A.mkv", goodMedia("Show"))
	fx.plain("SHARE/anime/Show/Season 01/Show - S01E02 - B.avi")
	res := fx.audit(dir)
	if got := codes(res.Findings); !slices.Equal(got, []string{"LEGACY"}) {
		t.Fatalf("a correctly named avi is only a container defect: %v", got)
	}
}

func TestSeriesUnreadable(t *testing.T) {
	fx, dir := seriesFixture(t)
	fx.put("SHARE/anime/Show/Season 01/Show - S01E01 - A.mkv", goodMedia("Show"))
	bad := goodMedia("Show")
	bad.Fail = true
	p := fx.put("SHARE/anime/Show/Season 01/Show - S01E02 - B.mkv", bad)
	res := fx.audit(dir)
	if got := codes(res.Findings); !slices.Equal(got, []string{"UNREADABLE"}) {
		t.Fatalf("findings = %v", got)
	}
	if f := res.Findings[0]; f.Path != p {
		t.Errorf("path = %q", f.Path)
	}
}

func TestSeriesTitle(t *testing.T) {
	t.Run("colon and slash titles pass against their filename spelling", func(t *testing.T) {
		fx := newFixture(t)
		fx.put("a/Re - Cutie Honey/Season 01/Re - Cutie Honey - S01E01 - X.mkv", goodMedia("Re: Cutie Honey"))
		fx.put("a/Fate Apocrypha/Season 01/Fate Apocrypha - S01E01 - X.mkv", goodMedia("Fate/Apocrypha"))
		for _, s := range []string{"Re - Cutie Honey", "Fate Apocrypha"} {
			if res := fx.audit(fx.path("a/" + s)); !res.Clean {
				t.Errorf("%s: %v", s, res.Findings)
			}
		}
	})
	t.Run("a wrong or missing title is TITLE in content scope", func(t *testing.T) {
		fx, dir := seriesFixture(t)
		fx.put("SHARE/anime/Show/Season 01/Show - S01E01 - A.mkv", goodMedia("Other"))
		fx.put("SHARE/anime/Show/Season 01/Show - S01E02 - B.mkv", goodMedia(""))
		res := fx.audit(dir)
		if count(res.Findings, "TITLE") != 2 || res.Findings[0].Scope != "content" {
			t.Fatalf("%v", res.Findings)
		}
	})
	t.Run("extras are their own scope", func(t *testing.T) {
		fx, dir := seriesFixture(t)
		fx.put("SHARE/anime/Show/Extras/Season 01/Disc 01/Menu.mkv", goodMedia("Menu"))
		res := fx.audit(dir)
		f := find(t, res.Findings, "TITLE")
		if f.Scope != "extras" || len(res.Findings) != 1 {
			t.Fatalf("%v", res.Findings)
		}
	})
	t.Run("films under Movies are not held to the series title", func(t *testing.T) {
		fx, dir := seriesFixture(t)
		fx.put("SHARE/anime/Show/Movies/Show - S99E01 - The Film (2010).mkv", goodMedia("The Film (2010)"))
		if res := fx.audit(dir); !res.Clean {
			t.Fatalf("%v", res.Findings)
		}
	})
}

func TestSeriesAudioAndSubs(t *testing.T) {
	cases := []struct {
		name string
		edit func(m *fakeMedia)
		want []string
	}{
		{"two default audio", func(m *fakeMedia) { m.Streams[2].Default = true }, []string{"AUDIO-default-count"}},
		{"no default audio", func(m *fakeMedia) { m.Streams[1].Default = false }, []string{"AUDIO-default-count"}},
		{"english default beside a japanese track", func(m *fakeMedia) {
			m.Streams[1].Default, m.Streams[2].Default = false, true
		}, []string{"AUDIO-not-jpn"}},
		{"english only, default, is fine", func(m *fakeMedia) {
			m.Streams = []fakeStream{m.Streams[0], m.Streams[2], m.Streams[3]}
			m.Streams[1].Default = true
		}, nil},
		{"no audio at all is not an audio finding", func(m *fakeMedia) {
			m.Streams = []fakeStream{m.Streams[0], m.Streams[3]}
		}, nil},
		{"two default subtitles", func(m *fakeMedia) { m.Streams[4].Default = true }, []string{"SUBS-default-count"}},
		{"no default subtitle", func(m *fakeMedia) { m.Streams[3].Default = false }, []string{"SUBS-default-count"}},
		{"no subtitles at all is not a subtitle finding", func(m *fakeMedia) {
			m.Streams = m.Streams[:3]
		}, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fx, dir := seriesFixture(t)
			m := goodMedia("Show")
			c.edit(&m)
			fx.put("SHARE/anime/Show/Season 01/Show - S01E01 - A.mkv", m)
			if got := codes(fx.audit(dir).Findings); !slices.Equal(got, c.want) {
				t.Fatalf("findings = %v, want %v", got, c.want)
			}
		})
	}
}

func TestSeriesAudioScopeSplit(t *testing.T) {
	fx, dir := seriesFixture(t)
	m := goodMedia("Show")
	m.Streams[2].Default = true
	fx.put("SHARE/anime/Show/Season 01/Show - S01E01 - A.mkv", m)
	fx.put("SHARE/anime/Show/Extras/Season 01/Disc 01/Bonus.mkv", m)
	res := fx.audit(dir)
	var scopes []string
	for _, f := range res.Findings {
		if f.Code == "AUDIO-default-count" {
			scopes = append(scopes, f.Scope)
		}
	}
	slices.Sort(scopes)
	if !slices.Equal(scopes, []string{"content", "extras"}) {
		t.Fatalf("scopes = %v", scopes)
	}
}

func TestSeriesShapeAndPrefix(t *testing.T) {
	fx, dir := seriesFixture(t)
	fx.put("SHARE/anime/Show/Season 01/Show - S01E01 - A.mkv", goodMedia("Show"))
	fx.put("SHARE/anime/Show/Season 01/Show 01.mkv", goodMedia("Show"))
	fx.put("SHARE/anime/Show/Season 01/Other - S01E02 - B.mkv", goodMedia("Show"))
	res := fx.audit(dir)
	if got := codes(res.Findings); !slices.Equal(got, []string{"PREFIX", "SHAPE"}) {
		t.Fatalf("findings = %v", got)
	}
}

func TestSeriesAcceptsFractionalAndOVA(t *testing.T) {
	fx, dir := seriesFixture(t)
	for _, n := range []string{
		"Show - S01E01 - A.mkv", "Show - S01E02 - B.mkv", "Show - S01E03 - C.mkv", "Show - S01E04 - D.mkv",
		"Show - S01E05 - E.mkv", "Show - S01E05.5 - Recap.mkv", "Show - S01E06 - F (OVA).mkv",
	} {
		fx.put("SHARE/anime/Show/Season 01/"+n, goodMedia("Show"))
	}
	if res := fx.audit(dir); !res.Clean {
		t.Fatalf("%v", res.Findings)
	}
}

func TestSeriesNumbering(t *testing.T) {
	put := func(fx *fixture, season string, eps ...string) {
		for _, e := range eps {
			fx.put("SHARE/anime/Show/"+season+"/Show - S01E"+e+" - T.mkv", goodMedia("Show"))
		}
	}
	t.Run("missing", func(t *testing.T) {
		fx, dir := seriesFixture(t)
		put(fx, "Season 01", "01", "02", "04")
		res := fx.audit(dir)
		if got := codes(res.Findings); !slices.Equal(got, []string{"NUM"}) || !strings.Contains(res.Findings[0].Detail, "missing 3 of 1..4") {
			t.Fatalf("%v", res.Findings)
		}
	})
	t.Run("a long gap is cut to eight numbers", func(t *testing.T) {
		fx, dir := seriesFixture(t)
		put(fx, "Season 01", "01", "12")
		d := fx.audit(dir).Findings[0].Detail
		if !strings.Contains(d, "missing 2,3,4,5,6,7,8,9 of 1..12") {
			t.Fatalf("%q", d)
		}
	})
	t.Run("duplicate", func(t *testing.T) {
		fx, dir := seriesFixture(t)
		put(fx, "Season 01", "01", "02")
		fx.put("SHARE/anime/Show/Season 01/Show - S01E02 - Again.mkv", goodMedia("Show"))
		res := fx.audit(dir)
		if got := codes(res.Findings); !slices.Equal(got, []string{"NUM"}) || !strings.Contains(res.Findings[0].Detail, "duplicate") {
			t.Fatalf("%v", res.Findings)
		}
	})
	t.Run("contiguous passes", func(t *testing.T) {
		fx, dir := seriesFixture(t)
		put(fx, "Season 01", "01", "02", "03")
		if res := fx.audit(dir); !res.Clean {
			t.Fatalf("%v", res.Findings)
		}
	})
	t.Run("only Season dirs are numbered", func(t *testing.T) {
		fx, dir := seriesFixture(t)
		put(fx, "Specials/Season 01", "01", "09")
		put(fx, "Other", "01", "09")
		if res := fx.audit(dir); count(res.Findings, "NUM") != 0 {
			t.Fatalf("%v", res.Findings)
		}
	})
	t.Run("each Season is numbered on its own", func(t *testing.T) {
		fx, dir := seriesFixture(t)
		put(fx, "Season 01", "01", "02")
		put(fx, "Season 02", "01", "03")
		res := fx.audit(dir)
		if len(res.Findings) != 1 || !strings.HasPrefix(res.Findings[0].Detail, "Season 02 missing 2") {
			t.Fatalf("%v", res.Findings)
		}
	})
}

func TestSeriesExtrasKeepTheirOriginalNames(t *testing.T) {
	fx, dir := seriesFixture(t)
	fx.put("SHARE/anime/Show/Extras/Season 01/Disc 01/Creditless OP.mkv", goodMedia("Show"))
	if res := fx.audit(dir); !res.Clean {
		t.Fatalf("%v", res.Findings)
	}
}

func TestSeriesLayout(t *testing.T) {
	cases := []struct {
		name  string
		files []string // under Show/
		want  []string
		match string
	}{
		{"video in a non-Season extras dir", []string{"Extras/Bonus/Clip.mkv"}, []string{"LAYOUT"}, "Extras/Bonus holds video"},
		{"Extras/Extras is sanctioned", []string{"Extras/Extras/Clip.mkv"}, nil, ""},
		{"Extras/Movies is sanctioned", []string{"Extras/Movies/Film/Clip.mkv"}, nil, ""},
		{"Extras/Season NN/Disc NN is sanctioned", []string{"Extras/Season 01/Disc 01/Clip.mkv"}, nil, ""},
		{"a non-video extras dir is not a video-layout defect", []string{"Extras/Soundtrack/ost.flac", "Extras/Scans/p1.jpg"}, nil, ""},
		{"loose video in Extras", []string{"Extras/Clip.mkv"}, []string{"LAYOUT"}, "Extras/ has loose files"},
		{"loose video in Specials", []string{"Specials/Clip.mkv"}, []string{"LAYOUT", "SHAPE"}, "Specials/ has loose files"},
		{"Specials/Season NN is allowed", []string{"Specials/Season 00/Show - S00E01 - Sp.mkv"}, nil, ""},
		{"Specials/Movies is allowed", []string{"Specials/Movies/Show - S99E01 - F (2010).mkv"}, nil, ""},
		{"another Specials dir is not", []string{"Specials/Junk/Show - S00E01 - Sp.mkv"}, []string{"LAYOUT"}, "Specials/Junk"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fx, dir := seriesFixture(t)
			fx.put("SHARE/anime/Show/Season 01/Show - S01E01 - A.mkv", goodMedia("Show"))
			for _, f := range c.files {
				if strings.HasSuffix(f, ".mkv") {
					fx.put("SHARE/anime/Show/"+f, goodMedia("Show"))
				} else {
					fx.plain("SHARE/anime/Show/" + f)
				}
			}
			res := fx.audit(dir)
			if got := codes(res.Findings); !slices.Equal(got, c.want) {
				t.Fatalf("findings = %v (%v), want %v", got, res.Findings, c.want)
			}
			if c.match != "" && !strings.Contains(find(t, res.Findings, "LAYOUT").Detail, c.match) {
				t.Errorf("detail = %q, want it to contain %q", find(t, res.Findings, "LAYOUT").Detail, c.match)
			}
		})
	}
}

func TestSeriesNeverOpensAFIFO(t *testing.T) {
	fx, dir := seriesFixture(t)
	fx.put("SHARE/anime/Show/Season 01/Show - S01E01 - A.mkv", goodMedia("Show"))
	fifo := fx.path("SHARE/anime/Show/Season 01/Show - S01E02 - B.mkv")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	res := fx.audit(dir)
	if fx.ff.wasAsked(fifo) {
		t.Fatal("a FIFO was probed")
	}
	if !slices.Equal(res.Skipped, []string{fifo}) || res.Files != 1 {
		t.Fatalf("skipped = %v, files = %d", res.Skipped, res.Files)
	}
	if !res.Clean {
		t.Fatalf("the FIFO is not an episode, so nothing is missing: %v", res.Findings)
	}
}

func TestSeriesWorkersBoundTheProbes(t *testing.T) {
	fx, dir := seriesFixture(t)
	fx.ff.delay = 5 * time.Millisecond
	for i := 1; i <= 12; i++ {
		fx.put("SHARE/anime/Show/Season 01/Show - S01E"+twoDigits(i)+" - T.mkv", goodMedia("Show"))
	}
	if _, err := AuditSeries(context.Background(), fx.ff, dir, 2); err != nil {
		t.Fatal(err)
	}
	if peak := fx.ff.peak.Load(); peak > 2 || peak < 1 {
		t.Fatalf("peak concurrency = %d, want 1..2", peak)
	}
}

func twoDigits(n int) string {
	return string(rune('0'+n/10)) + string(rune('0'+n%10))
}

func TestSeriesCommand(t *testing.T) {
	fx := newFixture(t)
	fx.put("SHARE/anime/Good/Season 01/Good - S01E01 - A.mkv", goodMedia("Good"))
	fx.plain("SHARE/anime/Bad/cover.jpg")
	good, bad := fx.path("SHARE/anime/Good"), fx.path("SHARE/anime/Bad")

	t.Run("clean exits 0", func(t *testing.T) {
		code, out, _ := run(t, fx.deps(), "series", good)
		if code != 0 || !strings.Contains(out, `"clean": 1`) || !strings.Contains(out, `"total": 1`) {
			t.Fatalf("code %d: %s", code, out)
		}
	})
	t.Run("a finding exits 1", func(t *testing.T) {
		code, out, _ := run(t, fx.deps(), "series", good, bad, "--workers", "2")
		if code != 1 || !strings.Contains(out, `"EMPTY"`) || !strings.Contains(out, `"total": 2`) {
			t.Fatalf("code %d: %s", code, out)
		}
	})
	t.Run("a missing directory is exit 2 naming it", func(t *testing.T) {
		missing := fx.path("SHARE/anime/Typo")
		code, out, errOut := run(t, fx.deps(), "series", good, missing)
		if code != 2 || out != "" || !strings.Contains(errOut, missing) {
			t.Fatalf("code %d, stdout %q, stderr %q", code, out, errOut)
		}
	})
	t.Run("a file is not a directory", func(t *testing.T) {
		code, _, errOut := run(t, fx.deps(), "series", fx.path("SHARE/anime/Bad/cover.jpg"))
		if code != 2 || !strings.Contains(errOut, "not a directory") {
			t.Fatalf("code %d: %s", code, errOut)
		}
	})
	t.Run("usage", func(t *testing.T) {
		for _, args := range [][]string{{"series"}, {"series", good, "--workers", "0"}, {"series", "--bogus", good}} {
			if code, _, _ := run(t, fx.deps(), args...); code != 2 {
				t.Errorf("%v: code %d", args, code)
			}
		}
	})
	t.Run("--list ignores blank lines and resolves relative lines against the current directory", func(t *testing.T) {
		list := fx.plain("list.txt")
		body := "\n  " + shareName + "/anime/Good  \n\n" + bad + "\n"
		writeFile(t, list, body)
		t.Chdir(fx.root)
		code, out, _ := run(t, fx.deps(), "series", "--list", list)
		if code != 1 || !strings.Contains(out, `"total": 2`) || !strings.Contains(out, fx.path("SHARE/anime/Good")) {
			t.Fatalf("code %d: %s", code, out)
		}
	})
	t.Run("--list with a missing directory is exit 2", func(t *testing.T) {
		list := fx.plain("list2.txt")
		writeFile(t, list, "nowhere\n")
		code, _, errOut := run(t, fx.deps(), "series", "--list", list)
		if code != 2 || !strings.Contains(errOut, "nowhere") {
			t.Fatalf("code %d: %s", code, errOut)
		}
	})
	t.Run("an unreadable list is exit 2", func(t *testing.T) {
		if code, _, _ := run(t, fx.deps(), "series", "--list", fx.path("no-such-list")); code != 2 {
			t.Fatalf("code %d", code)
		}
	})
}
