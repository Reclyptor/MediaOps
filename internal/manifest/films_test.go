package manifest

import (
	"strings"
	"testing"
)

// filmRig makes a series folder (an episode outside Movies/) holding the named
// films, and returns a title op on the first so the audit reaches it.
func filmRig(t *testing.T, series bool, films ...string) (*rig, Op) {
	t.Helper()
	r := newRig(t)
	if series {
		r.put("nas/anime/S/Season 01/S - S01E01.mkv", "e")
	}
	var first string
	for _, f := range films {
		p := r.mkdirFile("nas/anime/S/Movies/" + f)
		if first == "" {
			first = p
		}
	}
	return r, Op{Kind: KindTitle, Path: first, Title: "T"}
}

func (r *rig) mkdirFile(rel string) string {
	if strings.HasSuffix(rel, "/") {
		return r.mkdir(rel)
	}
	return r.put(rel, "x")
}

func filmFindings(rep CheckReport) []string {
	var out []string
	for _, f := range rep.Findings {
		if f.Op != nil {
			out = append(out, "OP:"+f.Problem)
		} else {
			out = append(out, f.Problem)
		}
	}
	return out
}

func wantTags(t *testing.T, rep CheckReport, tags ...string) {
	t.Helper()
	got := map[string]int{}
	for _, f := range rep.Findings {
		tag, _, _ := strings.Cut(f.Problem, ":")
		got[tag]++
	}
	for _, tag := range tags {
		if got[tag] == 0 {
			t.Errorf("no %s finding in %v", tag, filmFindings(rep))
		}
		delete(got, tag)
	}
	for tag := range got {
		t.Errorf("unexpected %s finding in %v", tag, filmFindings(rep))
	}
}

func TestFilmsSeriesShapeClean(t *testing.T) {
	r, op := filmRig(t, true,
		"S - S99E01 - First Film (2001).mkv",
		"S - S99E02 - Second Film (2003) - part1.mkv",
		"S - S99E02 - Second Film (2003) - part2.mkv",
		"S - S99E03 - Third (2005).ogm")
	rep := r.check(CheckOptions{Films: true}, op)
	if !rep.Clean || !rep.Films {
		t.Fatalf("%v", filmFindings(rep))
	}
}

func TestFilmsNonSeriesShapeClean(t *testing.T) {
	r, op := filmRig(t, false,
		"First Film (2001) - S99E01.mkv",
		"Second Film (2003) - S99E02 - part1.mkv",
		"Second Film (2003) - S99E02 - part2.mkv")
	r.put("nas/anime/S/Extras/Movies/First Film (2001)/making of.mkv", "x")
	rep := r.check(CheckOptions{Films: true}, op)
	if !rep.Clean {
		t.Fatalf("%v", filmFindings(rep))
	}
}

func TestFilmsSeriesShapeInNonSeriesFolderIsShape(t *testing.T) {
	r, op := filmRig(t, false, "S - S99E01 - First Film (2001).mkv")
	wantTags(t, r.check(CheckOptions{Films: true}, op), "SHAPE")
}

func TestFilmsNonSeriesShapeInSeriesFolderIsShape(t *testing.T) {
	r, op := filmRig(t, true, "First Film (2001) - S99E01.mkv")
	wantTags(t, r.check(CheckOptions{Films: true}, op), "SHAPE")
}

func TestFilmsEachDefect(t *testing.T) {
	cases := []struct {
		name   string
		series bool
		films  []string
		tags   []string
	}{
		{"prefix", true, []string{"Other - S99E01 - Film (2001).mkv"}, []string{"PREFIX"}},
		{"noyear series", true, []string{"S - S99E01 - Film.mkv"}, []string{"NOYEAR"}},
		{"noyear lone", false, []string{"Film - S99E01.mkv"}, []string{"NOYEAR"}},
		{"gap", true, []string{"S - S99E01 - A (2001).mkv", "S - S99E03 - C (2003).mkv"}, []string{"GAP"}},
		{"gap from the start", true, []string{"S - S99E02 - B (2002).mkv"}, []string{"GAP"}},
		{"dupnum", true, []string{"S - S99E01 - A (2001).mkv", "S - S99E01 - B (2002).mkv"}, []string{"DUPNUM"}},
		{"dupnum with one part only", true, []string{"S - S99E01 - A (2001).mkv", "S - S99E01 - A (2001) - part2.mkv"}, []string{"DUPNUM"}},
		{"parts are not a dupnum", true, []string{"S - S99E01 - A (2001) - part1.mkv", "S - S99E01 - A (2001) - part2.mkv"}, nil},
		{"stray subdir", true, []string{"S - S99E01 - A (2001).mkv", "Extras/"}, []string{"STRAY-SUBDIR"}},
		{"non-video", true, []string{"S - S99E01 - A (2001).mkv", "poster.jpg"}, []string{"NON-VIDEO"}},
		{"shape", true, []string{"S - S99E01 - A (2001).mkv", "random.mkv"}, []string{"SHAPE"}},
		{"episode number is two digits", true, []string{"S - S99E1 - A (2001).mkv"}, []string{"SHAPE"}},
		{"several at once", true, []string{"Other - S99E01 - A.mkv", "S - S99E04 - B (2004).mkv", "n.txt", "dir/"},
			[]string{"PREFIX", "NOYEAR", "GAP", "NON-VIDEO", "STRAY-SUBDIR"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r, _ := filmRig(t, c.series)
			var first string
			for _, f := range c.films {
				p := r.mkdirFile("nas/anime/S/Movies/" + f)
				if first == "" {
					first = p
				}
			}
			op := Op{Kind: KindFlags, Path: first, AudioDefault: 1}
			if strings.HasSuffix(first, "/") || !strings.HasSuffix(first, ".mkv") {
				op = Op{Kind: KindRmdirIfEmpty, Path: r.path("nas/anime/S/Movies")}
			}
			wantTags(t, r.check(CheckOptions{Films: true}, op), c.tags...)
		})
	}
}

func TestFilmsFindingsAreWholeManifestAndNamed(t *testing.T) {
	r, op := filmRig(t, true, "Other - S99E01 - A (2001).mkv")
	rep := r.check(CheckOptions{Films: true}, op)
	f := rep.Findings[0]
	if f.Op != nil || !strings.HasPrefix(f.Problem, "PREFIX:") || !strings.HasSuffix(f.Path, "Movies/Other - S99E01 - A (2001).mkv") {
		t.Fatalf("%+v", f)
	}
}

func TestFilmsOnlyAuditsTouchedSeries(t *testing.T) {
	r, op := filmRig(t, true, "S - S99E01 - A (2001).mkv")
	r.put("nas/anime/Untouched/Season 01/Untouched - S01E01.mkv", "e")
	r.put("nas/anime/Untouched/Movies/garbage.txt", "x")
	rep := r.check(CheckOptions{Films: true}, op)
	if !rep.Clean {
		t.Fatalf("%v", filmFindings(rep))
	}
	rep = r.check(CheckOptions{Films: true}, op, Op{Kind: KindRmdirIfEmpty, Path: r.path("nas/anime/Untouched/Movies")})
	wantTags(t, rep, "NON-VIDEO")
	rep = r.check(CheckOptions{}, op, Op{Kind: KindRmdirIfEmpty, Path: r.path("nas/anime/Untouched/Movies")})
	if !rep.Clean || rep.Films {
		t.Fatalf("without --films nothing is audited: %v", filmFindings(rep))
	}
}

func TestFilmsReadsThroughTheOverlay(t *testing.T) {
	r := newRig(t)
	r.put("nas/anime/S/Season 01/S - S01E01.mkv", "e")
	src := r.put("nas/_pending/anime/S/raw film.mkv", "x")
	bad := r.put("nas/anime/S/Movies/wrong.mkv", "x")

	// The pending source lands in Movies/ under a good name; the bad file leaves.
	rep := r.check(CheckOptions{Films: true},
		Op{Kind: KindRename, Src: src, Dst: r.path("nas/anime/S/Movies/S - S99E01 - Film (2001).mkv")},
		Op{Kind: KindRename, Src: bad, Dst: r.path("nas/_pending/anime/S/wrong.mkv")},
	)
	if !rep.Clean {
		t.Fatalf("%v", filmFindings(rep))
	}

	// The same landing under a bad name is caught before anything moves.
	r2 := newRig(t)
	r2.put("nas/anime/S/Season 01/S - S01E01.mkv", "e")
	src2 := r2.put("nas/_pending/anime/S/raw film.mkv", "x")
	wantTags(t, r2.check(CheckOptions{Films: true},
		Op{Kind: KindRename, Src: src2, Dst: r2.path("nas/anime/S/Movies/raw film.mkv")}), "SHAPE")
}

func TestFilmsStagingMoviesDirIsNotASeries(t *testing.T) {
	r := newRig(t)
	src := r.put("nas/_pending/anime/S/Movies/whatever.mkv", "x")
	rep := r.check(CheckOptions{Films: true}, Op{Kind: KindRename, Src: src, Dst: r.path("nas/_pending/anime/S/Movies/other.mkv")})
	if !rep.Clean {
		t.Fatalf("%v", filmFindings(rep))
	}
}

func TestFilmsMovingAWholeSeriesDirAudits(t *testing.T) {
	r, _ := filmRig(t, true, "S - S99E01 - A (2001).mkv", "junk.mkv")
	rep := r.check(CheckOptions{Films: true}, Op{Kind: KindMoveDir, Src: r.path("nas/anime/S"), Dst: r.path("nas/ecchi/S")})
	wantTags(t, rep, "SHAPE")
	if !strings.Contains(rep.Findings[0].Path, "nas/ecchi/S/Movies/junk.mkv") {
		t.Fatalf("%+v", rep.Findings[0])
	}
}
