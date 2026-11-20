package manifest

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reclyptor/mediaops/internal/cli"
)

func TestCheckCleanSimpleManifest(t *testing.T) {
	r := newRig(t)
	src := r.put("nas/_pending/x/a.mkv", "12345")
	rep := r.check(CheckOptions{},
		Op{Kind: KindRename, Src: src, Dst: r.path("nas/anime/S/a.mkv"), Size: i64(5), Title: "T"},
		Op{Kind: KindFlags, Path: r.path("nas/anime/S/a.mkv"), AudioDefault: 1},
		Op{Kind: KindRmdirIfEmpty, Path: r.path("nas/_pending/x")},
	)
	if !rep.Clean || len(rep.Findings) != 0 {
		t.Fatalf("%+v", rep.Findings)
	}
	if rep.Ops != 3 || rep.Pending != 3 || rep.Done != 0 || rep.Bytes != 5 || rep.Films {
		t.Fatalf("%+v", rep)
	}
	if rep.Kinds["rename"] != 1 || rep.Kinds["flags"] != 1 || rep.Kinds["rmdir_if_empty"] != 1 {
		t.Fatalf("kinds %v", rep.Kinds)
	}
	if len(rep.Sources) != 1 || rep.Sources[0] != (Source{Dir: r.path("nas/_pending/x"), Files: 1}) {
		t.Fatalf("sources %+v", rep.Sources)
	}
	if len(rep.Notes) != 0 {
		t.Fatalf("the rmdir of a directory the rename empties is no note: %+v", rep.Notes)
	}
}

func TestCheckReportPrintsEmptyListsNotNull(t *testing.T) {
	r := newRig(t)
	rep := r.check(CheckOptions{})
	b, err := json.Marshal(rep)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"in_doubt", "sources", "notes", "findings"} {
		if !strings.Contains(string(b), `"`+key+`":[]`) {
			t.Errorf("%s is not []: %s", key, b)
		}
	}
	if !strings.Contains(string(b), `"clean":true`) || !strings.Contains(string(b), `"films":false`) {
		t.Errorf("%s", b)
	}
}

func TestCheckFindingKinds(t *testing.T) {
	r := newRig(t)
	existing := r.put("nas/a/exists.mkv", "xx")
	other := r.put("nas/a/other.mkv", "yyyy")
	r.mkdir("nas/a/dir")
	r.mkdir("nas/a/dir.mkv")
	file := r.put("nas/a/afile", "z")
	putFile(t, r.archive+"/e", "a")
	putFile(t, r.archive+"/x", "a")

	cases := []struct {
		name string
		op   Op
		want string
	}{
		{"rename src missing", Op{Kind: KindRename, Src: r.path("nas/a/none.mkv"), Dst: r.path("nas/a/new.mkv")}, "src is not a file at this point"},
		{"rename src is a directory", Op{Kind: KindRename, Src: r.path("nas/a/dir"), Dst: r.path("nas/a/new.mkv")}, "src is not a file"},
		{"rename size", Op{Kind: KindRename, Src: other, Dst: r.path("nas/a/new.mkv"), Size: i64(9)}, "size 4 is not the recorded 9"},
		{"rename dst exists", Op{Kind: KindRename, Src: other, Dst: existing}, "dst already exists"},
		{"rename dst is a dir", Op{Kind: KindRename, Src: other, Dst: r.path("nas/a/dir")}, "dst already exists"},
		{"dst ancestor is a file", Op{Kind: KindRename, Src: other, Dst: file + "/x/y.mkv"}, "ancestor is not a directory"},
		{"archive_file dst exists", Op{Kind: KindArchiveFile, Src: other, Dst: r.archive + "/e"}, "dst already exists"},
		{"dir src missing", Op{Kind: KindMoveDir, Src: r.path("nas/a/none"), Dst: r.path("nas/a/new")}, "src is not a directory"},
		{"dir src is a file", Op{Kind: KindRenameDir, Src: file, Dst: r.path("nas/a/new")}, "src is not a directory"},
		{"dir dst exists", Op{Kind: KindMoveDir, Src: r.path("nas/a/dir"), Dst: r.path("nas/a")}, "dst already exists"},
		{"dir dst inside src", Op{Kind: KindMoveDir, Src: r.path("nas/a/dir"), Dst: r.path("nas/a/dir/in/side")}, "inside its own src"},
		{"title target missing", Op{Kind: KindTitle, Path: r.path("nas/a/none.mkv"), Title: "T"}, "target is not a file"},
		{"flags target is a dir", Op{Kind: KindFlags, Path: r.path("nas/a/dir.mkv"), AudioDefault: 1}, "target is not a file"},
		{"video_lang target missing", Op{Kind: KindVideoLang, Path: r.path("nas/a/none.mkv"), Langs: map[int]string{0: "jpn"}}, "target is not a file"},
		{"audio_lang target missing", Op{Kind: KindAudioLang, Path: r.path("nas/a/none.mkv"), Langs: map[int]string{1: "jpn"}}, "target is not a file"},
		{"remux archive_dst exists", Op{Kind: KindRemux, Src: other, Dst: r.path("nas/a/r.mkv"), ArchiveDst: r.archive + "/x", Title: "T"}, "archive_dst already exists"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rep := r.check(CheckOptions{}, c.op)
			if !hasProblem(rep, 1, c.want) {
				t.Fatalf("want %q in %v", c.want, problems(rep))
			}
			if rep.Clean {
				t.Fatal("clean")
			}
		})
	}
}

func TestCheckJudgesAnOpOnTheStateEarlierOpsLeave(t *testing.T) {
	r := newRig(t)
	a := r.put("nas/p/a.mkv", "aaa")
	b := r.path("nas/p/b.mkv")
	c := r.path("nas/q/c.mkv")

	rep := r.check(CheckOptions{},
		Op{Kind: KindRename, Src: a, Dst: b},
		Op{Kind: KindTitle, Path: b, Title: "T"},
		Op{Kind: KindRename, Src: b, Dst: c, Size: i64(3)},
		Op{Kind: KindFlags, Path: c, AudioDefault: 1},
	)
	if !rep.Clean {
		t.Fatalf("a chain through the overlay: %v", problems(rep))
	}

	rep = r.check(CheckOptions{},
		Op{Kind: KindRename, Src: a, Dst: b},
		Op{Kind: KindTitle, Path: a, Title: "T"},
		Op{Kind: KindRename, Src: a, Dst: c},
	)
	if !hasProblem(rep, 2, "not a file at this point") || !hasProblem(rep, 3, "src is not a file at this point") || len(rep.Findings) != 2 {
		t.Fatalf("%v", problems(rep))
	}

	// A destination an earlier op used and vacated is still claimed: two ops never share one.
	r.put("nas/p/z.mkv", "z")
	rep = r.check(CheckOptions{},
		Op{Kind: KindRename, Src: a, Dst: b},
		Op{Kind: KindRename, Src: b, Dst: c},
		Op{Kind: KindRename, Src: r.path("nas/p/z.mkv"), Dst: b},
	)
	if len(rep.Findings) != 1 || !hasProblem(rep, 3, "also claimed by op 1") {
		t.Fatalf("%v", problems(rep))
	}
}

func TestCheckDstClaimedTwice(t *testing.T) {
	r := newRig(t)
	a := r.put("nas/p/a.mkv", "a")
	b := r.put("nas/p/b.mkv", "b")
	dst := r.path("nas/q/x.mkv")
	rep := r.check(CheckOptions{},
		Op{Kind: KindRename, Src: a, Dst: dst},
		Op{Kind: KindRename, Src: b, Dst: dst},
	)
	if !hasProblem(rep, 2, "also claimed by op 1") {
		t.Fatalf("%v", problems(rep))
	}
	// The claim holds even when the first op's source is missing.
	rep = r.check(CheckOptions{},
		Op{Kind: KindRename, Src: r.path("nas/p/none.mkv"), Dst: dst},
		Op{Kind: KindRename, Src: b, Dst: dst},
	)
	if !hasProblem(rep, 2, "also claimed by op 1") || !hasProblem(rep, 1, "src is not a file") {
		t.Fatalf("%v", problems(rep))
	}
	rep = r.check(CheckOptions{}, Op{Kind: KindRemux, Src: a, Dst: r.archive + "/r.mkv", ArchiveDst: r.archive + "/r.mkv", Title: "T"})
	if !hasProblem(rep, 1, "same path") {
		t.Fatalf("%v", problems(rep))
	}
}

func TestCheckDirMovesCarryTheirFiles(t *testing.T) {
	r := newRig(t)
	r.put("nas/old/s1/e1.mkv", "1")
	r.put("nas/old/s1/e2.mkv", "22")
	r.mkdir("nas/old/empty")
	rep := r.check(CheckOptions{},
		Op{Kind: KindMoveDir, Src: r.path("nas/old"), Dst: r.path("nas/new")},
		Op{Kind: KindRename, Src: r.path("nas/new/s1/e1.mkv"), Dst: r.path("nas/new/s1/first.mkv")},
		Op{Kind: KindFlags, Path: r.path("nas/new/s1/e2.mkv"), AudioDefault: 1},
		Op{Kind: KindTitle, Path: r.path("nas/old/s1/e2.mkv"), Title: "gone"},
	)
	if len(rep.Findings) != 1 || !hasProblem(rep, 4, "not a file at this point") {
		t.Fatalf("%v", problems(rep))
	}
	want := []Source{{Dir: r.path("nas/new/s1"), Files: 1}, {Dir: r.path("nas/old"), Files: 2}}
	if rep.Bytes != 3+1 || len(rep.Sources) != 2 || rep.Sources[0] != want[0] || rep.Sources[1] != want[1] {
		t.Fatalf("bytes %d sources %+v", rep.Bytes, rep.Sources)
	}
}

func TestCheckRmdirNotes(t *testing.T) {
	r := newRig(t)
	r.put("nas/p/keep/x.mkv", "x")
	r.mkdir("nas/p/empty")
	r.put("nas/p/moves/a.mkv", "a")
	rep := r.check(CheckOptions{},
		Op{Kind: KindRmdirIfEmpty, Path: r.path("nas/p/absent")},
		Op{Kind: KindRmdirIfEmpty, Path: r.path("nas/p/keep")},
		Op{Kind: KindRmdirIfEmpty, Path: r.path("nas/p/empty")},
		Op{Kind: KindRename, Src: r.path("nas/p/moves/a.mkv"), Dst: r.path("nas/q/a.mkv")},
		Op{Kind: KindRmdirIfEmpty, Path: r.path("nas/p/moves")},
		Op{Kind: KindRmdirIfEmpty, Path: r.path("nas/p/moves")},
	)
	if !rep.Clean {
		t.Fatalf("rmdir problems are notes, not findings: %v", problems(rep))
	}
	if len(rep.Notes) != 3 {
		t.Fatalf("%+v", rep.Notes)
	}
	if rep.Notes[0].Op != 1 || !strings.Contains(rep.Notes[0].Note, "already absent") {
		t.Errorf("%+v", rep.Notes[0])
	}
	if rep.Notes[1].Op != 2 || !strings.Contains(rep.Notes[1].Note, "x.mkv") {
		t.Errorf("%+v", rep.Notes[1])
	}
	if rep.Notes[2].Op != 6 || !strings.Contains(rep.Notes[2].Note, "already absent") {
		t.Errorf("removed once, absent the second time: %+v", rep.Notes[2])
	}
}

func TestCheckRootContainment(t *testing.T) {
	r := newRig(t)
	outside := filepath.Join(filepath.Dir(r.media), "elsewhere")
	putFile(t, outside+"/a.mkv", "a")
	inside := r.put("nas/p/a.mkv", "a")
	staging := r.cfg.Paths.StagingRoot
	cases := []struct {
		name string
		op   Op
		want string
	}{
		{"src outside MEDIA_ROOT", Op{Kind: KindRename, Src: outside + "/a.mkv", Dst: r.path("nas/p/b.mkv")}, "src is outside MEDIA_ROOT"},
		{"dst outside MEDIA_ROOT", Op{Kind: KindRename, Src: inside, Dst: outside + "/b.mkv"}, "dst is outside MEDIA_ROOT"},
		{"dst is MEDIA_ROOT", Op{Kind: KindRenameDir, Src: r.path("nas/p"), Dst: r.media}, "dst is outside MEDIA_ROOT"},
		{"sibling prefix is not inside", Op{Kind: KindRename, Src: inside, Dst: r.media + "2/b.mkv"}, "dst is outside MEDIA_ROOT"},
		{"archive dst outside ARCHIVE_ROOT", Op{Kind: KindArchiveFile, Src: inside, Dst: r.path("nas/p/b.mkv")}, "dst is outside ARCHIVE_ROOT"},
		{"archive dir dst is ARCHIVE_ROOT", Op{Kind: KindArchiveDir, Src: r.path("nas/p"), Dst: r.archive}, "dst is outside ARCHIVE_ROOT"},
		{"remux archive_dst outside", Op{Kind: KindRemux, Src: inside, Dst: r.path("nas/p/b.mkv"), ArchiveDst: r.path("nas/p/b.mp4"), Title: "T"}, "archive_dst is outside ARCHIVE_ROOT"},
		{"target outside", Op{Kind: KindTitle, Path: outside + "/a.mkv", Title: "T"}, "path is outside MEDIA_ROOT"},
		{"rmdir MEDIA_ROOT", Op{Kind: KindRmdirIfEmpty, Path: r.media}, "outside MEDIA_ROOT"},
		{"rmdir a share", Op{Kind: KindRmdirIfEmpty, Path: r.path("nas")}, "never removes a share"},
		{"rmdir staging", Op{Kind: KindRmdirIfEmpty, Path: staging}, "never removes the staging tree"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rep := r.check(CheckOptions{}, c.op)
			if !hasProblem(rep, 1, c.want) {
				t.Fatalf("want %q in %v", c.want, problems(rep))
			}
		})
	}
	if rep := r.check(CheckOptions{}, Op{Kind: KindRmdirIfEmpty, Path: r.path("nas/p")}); !rep.Clean {
		t.Fatalf("a directory below the share is fine: %v", problems(rep))
	}
}

func TestCheckArchiveRootGuard(t *testing.T) {
	r := newRig(t)
	src := r.put("nas/p/a.mkv", "a")
	m, findings, _ := r.manifestOps(Op{Kind: KindArchiveFile, Src: src, Dst: r.archive + "/a.mkv"})

	for name, mutate := range map[string]func(*Config){
		"unset":   func(c *Config) { c.Paths.Archive = "" },
		"missing": func(c *Config) { c.Paths.Archive = r.path("nas2/nope") },
		"on root": func(c *Config) { c.Paths.StatDev = func(string) (uint64, error) { return 1, nil } },
	} {
		cfg := r.cfg
		mutate(&cfg)
		if _, err := cfg.Check(m, findings, nil, CheckOptions{}); cli.Code(err) != cli.ExitRefused {
			t.Errorf("%s: err = %v", name, err)
		}
	}

	// Without archive ops the guard is not consulted.
	cfg := r.cfg
	cfg.Paths.Archive = ""
	m, findings, _ = r.manifestOps(Op{Kind: KindRename, Src: src, Dst: r.path("nas/p/b.mkv")})
	if rep, err := cfg.Check(m, findings, nil, CheckOptions{}); err != nil || !rep.Clean {
		t.Fatalf("%v %v", err, rep.Findings)
	}
}

// An unset STAGING_ROOT must stop the check, not leave the staging-tree guard
// comparing against an empty path that matches nothing.
func TestCheckRefusesWithoutStagingRoot(t *testing.T) {
	r := newRig(t)
	src := r.put("nas/p/a.mkv", "a")
	cfg := r.cfg
	cfg.Paths.StagingRoot = ""

	for name, op := range map[string]Op{
		"rmdir":  {Kind: KindRmdirIfEmpty, Path: r.path("nas/p")},
		"rename": {Kind: KindRename, Src: src, Dst: r.path("nas/p/b.mkv")},
	} {
		m, findings, _ := r.manifestOps(op)
		rep, err := cfg.Check(m, findings, nil, CheckOptions{})
		if cli.Code(err) != cli.ExitRefused || !strings.Contains(err.Error(), "STAGING_ROOT") {
			t.Errorf("%s: err = %v, want a STAGING_ROOT refusal", name, err)
		}
		if len(rep.Findings) != 0 {
			t.Errorf("%s: a refused check reported findings: %v", name, problems(rep))
		}
	}
}

func TestCheckDestinationNameRules(t *testing.T) {
	r := newRig(t)
	long := strings.Repeat("x", 252) + ".mkv"
	srcs := make([]string, 7)
	for i := range srcs {
		srcs[i] = r.put("nas/p/"+string(rune('a'+i))+".mkv", "a")
	}
	m := &Manifest{Path: r.plans + "/m.json", Ops: []Op{
		{N: 1, Kind: KindRename, Src: srcs[1], Dst: r.path("nas/q/" + long)},
		{N: 2, Kind: KindRename, Src: srcs[2], Dst: r.path("nas/q/bad\xffname.mkv")},
		{N: 3, Kind: KindRename, Src: srcs[3], Dst: r.path("nas/q/tab\there.mkv")},
		{N: 4, Kind: KindRename, Src: srcs[4], Dst: r.path("nas/q/a.mkv.partial")},
		{N: 5, Kind: KindRename, Src: srcs[5], Dst: r.path("nas/q/" + strings.Repeat("é", 127) + ".mkv")},
		{N: 6, Kind: KindRename, Src: srcs[6], Dst: r.path("nas/q/ok - [fine] ‘x’ ？.mkv")},
	}}
	rep, err := r.cfg.Check(m, nil, nil, CheckOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for op, want := range map[int]string{1: "256 bytes", 2: "not valid UTF-8", 3: "control character", 4: ".partial", 5: "bytes"} {
		if !hasProblem(rep, op, "destination name") || !hasProblem(rep, op, want) {
			t.Errorf("op %d: %v", op, problems(rep))
		}
	}
	for _, f := range rep.Findings {
		if *f.Op == 6 {
			t.Errorf("a plain name was rejected: %+v", f)
		}
	}
}

func TestCheckUnwritableAncestor(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can write anywhere")
	}
	r := newRig(t)
	src := r.put("nas/p/a.mkv", "a")
	ro := r.mkdir("nas/ro")
	if err := os.Chmod(ro, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(ro, 0o700) })
	rep := r.check(CheckOptions{}, Op{Kind: KindRename, Src: src, Dst: ro + "/deeper/x.mkv"})
	if !hasProblem(rep, 1, "ancestor is not writable") {
		t.Fatalf("%v", problems(rep))
	}
}

func TestCheckSchemaFindingsAreMergedAndSkipReplay(t *testing.T) {
	r := newRig(t)
	good := r.put("nas/p/a.mkv", "a")
	m, findings, _ := r.manifestJSON("m.json", `{"ops":[
	  {"kind":"rename","src":"`+good+`","dst":"`+r.path("nas/p/b.mkv")+`","oops":1},
	  {"kind":"rename","src":"`+good+`","dst":"`+r.path("nas/p/c.mkv")+`"}],"zzz":1}`)
	rep, err := r.cfg.Check(m, findings, nil, CheckOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Findings[0].Op != nil || !strings.Contains(rep.Findings[0].Problem, "zzz") {
		t.Fatalf("whole-manifest findings come first: %+v", rep.Findings)
	}
	if !hasProblem(rep, 1, `unknown field "oops"`) {
		t.Fatalf("%v", problems(rep))
	}
	// Op 1 is not replayed, so the source is still free for op 2.
	if len(rep.Findings) != 2 || rep.Pending != 2 {
		t.Fatalf("%v", problems(rep))
	}
}

func TestCheckLeavesTheTreeUntouched(t *testing.T) {
	for _, name := range []string{"penguindrum", "films", "outlanders"} {
		t.Run(name, func(t *testing.T) {
			cfg, path := buildFixture(t, name, nil)
			before := snapshot(t, filepath.Dir(cfg.Paths.MediaRoot))
			m, findings, err := Load(path)
			if err != nil {
				t.Fatal(err)
			}
			j, err := ReadJournal(m)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := cfg.Check(m, findings, j, CheckOptions{Films: true}); err != nil {
				t.Fatal(err)
			}
			after := snapshot(t, filepath.Dir(cfg.Paths.MediaRoot))
			if len(before) != len(after) {
				t.Fatalf("entries %d -> %d", len(before), len(after))
			}
			for p, v := range before {
				if after[p] != v {
					t.Errorf("%s: %q -> %q", p, v, after[p])
				}
			}
		})
	}
}

func TestCheckFixtureReportsNotes(t *testing.T) {
	cfg, path := buildFixture(t, "outlanders", nil)
	m, findings, _ := Load(path)
	rep, err := cfg.Check(m, findings, nil, CheckOptions{})
	if err != nil || !rep.Clean {
		t.Fatalf("%v %v", err, rep.Findings)
	}
	if rep.Bytes == 0 || len(rep.Sources) != 1 || rep.Sources[0].Files != 2 {
		t.Fatalf("%+v", rep)
	}
}

func TestCheckBrokenFixtureReportsEveryPlantedProblem(t *testing.T) {
	cfg, path := buildFixture(t, "penguindrum", nil)
	m, findings, _ := Load(path)

	// op 1: source gone; op 2: destination occupied; op 4 claims op 3's destination;
	// op 5: wrong recorded size; op 6: destination under a regular file;
	// op 7: destination name over the limit; op 8: dst in ARCHIVE_ROOT-less outside tree.
	if err := os.Remove(m.Ops[0].Src); err != nil {
		t.Fatal(err)
	}
	putFile(t, m.Ops[1].Dst, "squatter")
	m.Ops[3].Dst = m.Ops[2].Dst
	m.Ops[4].Size = i64(999999)
	putFile(t, filepath.Dir(m.Ops[5].Dst)+"/../blocker", "f")
	m.Ops[5].Dst = filepath.Join(filepath.Dir(m.Ops[5].Dst), "..", "blocker", "x.mkv")
	m.Ops[5].Dst = filepath.Clean(m.Ops[5].Dst)
	m.Ops[6].Dst = filepath.Join(filepath.Dir(m.Ops[6].Dst), strings.Repeat("n", 300)+".mkv")
	m.Ops[7].Dst = "/elsewhere/x.mkv"

	rep, err := cfg.Check(m, findings, nil, CheckOptions{})
	if err != nil {
		t.Fatal(err)
	}
	want := map[int]string{
		1: "src is not a file at this point",
		2: "dst already exists",
		4: "also claimed by op 3",
		5: "is not the recorded 999999",
		6: "ancestor is not a directory",
		7: "255",
		8: "outside MEDIA_ROOT",
	}
	for op, sub := range want {
		if !hasProblem(rep, op, sub) {
			t.Errorf("op %d: missing %q in %v", op, sub, problems(rep))
		}
	}
	if rep.Clean {
		t.Fatal("clean")
	}
}

func TestCheckSettledAndConflictAreSkippedOrReported(t *testing.T) {
	r := newRig(t)
	a := r.put("nas/p/a.mkv", "aaa")
	b := r.put("nas/p/b.mkv", "bbb")
	ops := []Op{
		{Kind: KindRename, Src: r.path("nas/p/gone.mkv"), Dst: r.put("nas/q/done.mkv", "xyz"), Size: i64(3)},
		{Kind: KindRename, Src: a, Dst: r.put("nas/q/both.mkv", "aaa")},
		{Kind: KindFlags, Path: r.path("nas/q/done.mkv"), AudioDefault: 1},
		{Kind: KindRename, Src: b, Dst: r.path("nas/q/redo.mkv")},
	}
	m, findings, mpath := r.manifestOps(ops...)
	j := writeJournal(t, r.cfg, m, mpath, func(w *JournalWriter) {
		w.Begin(m.Ops[0], BeginState{Size: i64(3)})
		w.Begin(m.Ops[1], BeginState{Size: i64(3)})
		w.Begin(m.Ops[3], BeginState{Size: i64(3)})
	})
	rep, err := r.cfg.Check(m, findings, j, CheckOptions{})
	if err != nil {
		t.Fatal(err)
	}
	got := map[int]Outcome{}
	for _, d := range rep.InDoubt {
		got[d.Op] = d.Outcome
	}
	if got[1] != Settled || got[2] != Conflict || got[4] != Redo || len(rep.InDoubt) != 3 {
		t.Fatalf("%+v", rep.InDoubt)
	}
	if len(rep.Findings) != 1 || !hasProblem(rep, 2, "interrupted earlier") || !strings.Contains(rep.Findings[0].Problem, a) {
		t.Fatalf("only the conflict is a finding, naming both paths: %+v", rep.Findings)
	}
	// op 3 sees the settled op's destination as a file even though op 1's src is gone.
}

func TestCheckDoneOpsAreSkipped(t *testing.T) {
	r := newRig(t)
	a := r.put("nas/p/a.mkv", "a")
	dst := r.put("nas/q/a.mkv", "a")
	m, findings, mpath := r.manifestOps(
		Op{Kind: KindRename, Src: r.path("nas/p/missing.mkv"), Dst: dst},
		Op{Kind: KindTitle, Path: dst, Title: "T"},
		Op{Kind: KindRename, Src: a, Dst: r.path("nas/q/b.mkv")},
	)
	j := writeJournal(t, r.cfg, m, mpath, func(w *JournalWriter) {
		w.Begin(m.Ops[0], BeginState{})
		w.Done(1, 1, "")
	})
	rep, err := r.cfg.Check(m, findings, j, CheckOptions{})
	if err != nil || !rep.Clean {
		t.Fatalf("%v %v", err, problems(rep))
	}
	if rep.Done != 1 || rep.Pending != 2 || rep.Ops != 3 || rep.Kinds["rename"] != 2 || len(rep.InDoubt) != 0 {
		t.Fatalf("%+v", rep)
	}
}

func TestCheckResumedDirMoveAndRemuxAreModelled(t *testing.T) {
	r := newRig(t)
	// A directory move interrupted after one file: both sides hold part.
	r.put("nas/src/s/a.mkv", "a")
	r.put("nas/dst/s/b.mkv", "bb")
	// A remux whose output is in place and archive not yet made.
	rsrc := r.put("nas/r/x.mp4", "src")
	rdst := r.put("nas/r/x.mkv", "out")
	arch := r.archive + "/x.orig.mkv"
	m, findings, mpath := r.manifestOps(
		Op{Kind: KindMoveDir, Src: r.path("nas/src"), Dst: r.path("nas/dst")},
		Op{Kind: KindFlags, Path: r.path("nas/dst/s/a.mkv"), AudioDefault: 1},
		Op{Kind: KindRemux, Src: rsrc, Dst: rdst, ArchiveDst: arch, Title: "T"},
		Op{Kind: KindFlags, Path: arch, AudioDefault: 1},
	)
	j := writeJournal(t, r.cfg, m, mpath, func(w *JournalWriter) {
		w.Begin(m.Ops[0], BeginState{Tree: map[string]int64{"s/a.mkv": 1, "s/b.mkv": 2}})
		w.Begin(m.Ops[2], BeginState{Size: i64(3)})
	})
	if _, doubt := j.InDoubt(1); !doubt {
		t.Fatal("op 1 is not in doubt")
	}
	rep, err := r.cfg.Check(m, findings, j, CheckOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range rep.InDoubt {
		if d.Outcome != Resume {
			t.Errorf("%+v", d)
		}
	}
	if len(rep.InDoubt) != 2 {
		t.Fatalf("%+v", rep.InDoubt)
	}
	if !rep.Clean {
		t.Fatalf("%v", problems(rep))
	}
}

// writeJournal writes a journal for m through the real writer and reads it back.
func writeJournal(t *testing.T, cfg Config, m *Manifest, _ string, f func(w *JournalWriter)) *Journal {
	t.Helper()
	w, err := OpenJournal(m, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Start(); err != nil {
		t.Fatal(err)
	}
	f(w)
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	j, err := ReadJournal(m)
	if err != nil {
		t.Fatal(err)
	}
	return j
}

func TestCheckDirMoveRefusesATreeHoldingALink(t *testing.T) {
	r := newRig(t)
	r.put("nas/old/s1/e1.mkv", "1")
	op := Op{Kind: KindMoveDir, Src: r.path("nas/old"), Dst: r.path("nas/new")}
	if rep := r.check(CheckOptions{}, op); len(rep.Findings) != 0 {
		t.Fatalf("clean tree: %v", problems(rep))
	}
	if err := os.Symlink("e1.mkv", r.path("nas/old/s1/link.mkv")); err != nil {
		t.Fatal(err)
	}
	rep := r.check(CheckOptions{}, op)
	if len(rep.Findings) != 1 || !hasProblem(rep, 1, "symlink") || !strings.Contains(problems(rep)[0], "link.mkv") {
		t.Fatalf("%v", problems(rep))
	}
}
