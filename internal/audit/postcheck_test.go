package audit

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"

	"github.com/reclyptor/mediaops/internal/inventory"
	"github.com/reclyptor/mediaops/internal/manifest"
)

// pcRun is a finished two-episode manifest run: two renames out of staging
// (with title and size), flags on the first episode, a language and a title op
// on the second, and a rmdir of the rip directory.
type pcRun struct {
	fx         *fixture
	src1, src2 string
	dst1, dst2 string
	m1, m2     fakeMedia
	ops        []op
	done       []int
	before     []inventory.Record
}

func newRun(t *testing.T) *pcRun {
	fx := newFixture(t)
	r := &pcRun{fx: fx, m1: goodMedia("Show"), m2: goodMedia("Show"), done: []int{1, 2, 3, 4, 5, 6}}
	r.src1 = fx.path("SHARE/_pending/rip/t00.mkv")
	r.src2 = fx.path("SHARE/_pending/rip/t01.mkv")
	r.dst1 = fx.path("SHARE/anime/Show/Season 01/Show - S01E01 - A.mkv")
	r.dst2 = fx.path("SHARE/anime/Show/Season 01/Show - S01E02 - B.mkv")
	return r
}

// build puts the destinations and writes the manifest, journal and inventory,
// from whatever the test left in r.
func (r *pcRun) build() (manifestPath, beforePath string) {
	t, fx := r.fx.t, r.fx
	if r.ops == nil {
		r.ops = []op{
			{"kind": "rename", "src": r.src1, "dst": r.dst1, "title": "Show", "size": 10},
			{"kind": "rename", "src": r.src2, "dst": r.dst2, "title": "Show", "size": 10},
			{"kind": "flags", "path": r.dst1, "audio_default": 1, "sub_default": 3},
			{"kind": "audio_lang", "path": r.dst2, "langs": map[string]string{"1": "jpn"}},
			{"kind": "title", "path": r.dst2, "title": "Show"},
			{"kind": "rmdir_if_empty", "path": filepath.Dir(r.src1)},
		}
	}
	if r.before == nil {
		r.before = []inventory.Record{
			{Path: r.src1, Streams: inventoryStreams(r.m1)},
			{Path: r.src2, Streams: inventoryStreams(r.m2)},
		}
	}
	if r.dst1 != "" && r.m1.Streams != nil {
		fx.put(strings.TrimPrefix(r.dst1, fx.root+"/"), r.m1)
	}
	if r.dst2 != "" && r.m2.Streams != nil {
		fx.put(strings.TrimPrefix(r.dst2, fx.root+"/"), r.m2)
	}

	manifestPath = fx.writeManifest("run.json", r.ops...)
	m, _, err := manifest.Load(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	lines := []string{fmt.Sprintf(`{"event":"start","manifest_sha256":%q,"ops":%d}`, m.SHA256, len(r.ops))}
	for _, n := range r.done {
		lines = append(lines, fmt.Sprintf(`{"event":"done","op":%d}`, n))
	}
	writeFile(t, manifest.JournalPath(manifestPath), strings.Join(lines, "\n")+"\n")

	body, err := json.Marshal(inventory.Inventory{Files: r.before})
	if err != nil {
		t.Fatal(err)
	}
	beforePath = fx.path("plans/before.json")
	writeFile(t, beforePath, string(body))
	return manifestPath, beforePath
}

func (r *pcRun) check(extra ...string) (int, PostcheckReport) {
	t := r.fx.t
	t.Helper()
	m, before := r.build()
	code, out, errOut := run(t, r.fx.deps(), append([]string{"postcheck", m, "--before", before}, extra...)...)
	var rep PostcheckReport
	if code <= 1 {
		if err := json.Unmarshal([]byte(out), &rep); err != nil {
			t.Fatalf("code %d: %v: %s", code, err, out)
		}
	} else {
		t.Logf("stderr: %s", errOut)
	}
	return code, rep
}

func (r *pcRun) wantOnly(code string) PostcheckReport {
	r.fx.t.Helper()
	exit, rep := r.check()
	if exit != 1 {
		r.fx.t.Fatalf("exit %d, want 1: %v", exit, rep.Findings)
	}
	if got := codes(rep.Findings); !slices.Equal(got, []string{code}) {
		r.fx.t.Fatalf("findings = %v, want only %s: %v", got, code, rep.Findings)
	}
	return rep
}

func TestPostcheckCleanRun(t *testing.T) {
	r := newRun(t)
	code, rep := r.check()
	if code != 0 || !rep.Clean || len(rep.Findings) != 0 {
		t.Fatalf("code %d: %v", code, rep.Findings)
	}
	if rep.Ops != 6 || rep.Done != 6 || len(rep.Series) != 1 || rep.Series[0].Name != "Show" || !rep.Series[0].Clean {
		t.Fatalf("%+v", rep)
	}
	if rep.SHA256 == "" {
		t.Error("no sha256")
	}
}

func TestPostcheckOpNotDone(t *testing.T) {
	r := newRun(t)
	r.done = []int{1, 2, 3, 4, 6}
	rep := r.wantOnly("NOT-DONE")
	if f := rep.Findings[0]; f.Op == nil || *f.Op != 5 {
		t.Fatalf("%+v", f)
	}
}

func TestPostcheckSourceRemains(t *testing.T) {
	r := newRun(t)
	r.fx.plain("SHARE/_pending/rip/t00.mkv")
	rep := r.wantOnly("SRC-REMAINS")
	if rep.Findings[0].Path != r.src1 {
		t.Fatalf("%+v", rep.Findings[0])
	}
}

func TestPostcheckDestinationMissing(t *testing.T) {
	r := newRun(t)
	r.m2.Streams = nil // not put
	_, rep := r.check()
	for _, want := range []string{"DST-MISSING", "TARGET-MISSING"} {
		if count(rep.Findings, want) == 0 {
			t.Errorf("no %s in %v", want, codes(rep.Findings))
		}
	}
}

func TestPostcheckDestinationNotARegularFile(t *testing.T) {
	r := newRun(t)
	r.m1.Streams = nil
	r.fx.mkdir("SHARE/anime/Show/Season 01/Show - S01E01 - A.mkv")
	_, rep := r.check()
	if f := find(t, rep.Findings, "DST-MISSING"); f.Path != r.dst1 {
		t.Fatalf("%+v", f)
	}
}

func TestPostcheckSizeChangeIsANote(t *testing.T) {
	r := newRun(t)
	r.ops = []op{
		{"kind": "rename", "src": r.src1, "dst": r.dst1, "size": 9},
		{"kind": "rename", "src": r.src2, "dst": r.dst2, "size": 10},
	}
	r.done = []int{1, 2}
	code, rep := r.check()
	if code != 0 || !rep.Clean || len(rep.Notes) != 1 {
		t.Fatalf("code %d, findings %v, notes %v", code, rep.Findings, rep.Notes)
	}
	if n := rep.Notes[0]; n.Code != "SIZE-CHANGED" || n.Path != r.dst1 || n.Op == nil || *n.Op != 1 {
		t.Fatalf("%+v", n)
	}
}

func TestPostcheckTitle(t *testing.T) {
	t.Run("rename title", func(t *testing.T) {
		r := newRun(t)
		r.m1.Title = "Wrong"
		_, rep := r.check()
		if count(rep.Findings, "TITLE-MISMATCH") != 1 {
			t.Fatalf("%v", rep.Findings)
		}
	})
	t.Run("title op", func(t *testing.T) {
		r := newRun(t)
		r.ops = []op{
			{"kind": "rename", "src": r.src1, "dst": r.dst1},
			{"kind": "rename", "src": r.src2, "dst": r.dst2},
			{"kind": "title", "path": r.dst2, "title": "Elsewhere"},
		}
		r.done = []int{1, 2, 3}
		_, rep := r.check()
		f := find(t, rep.Findings, "TITLE-MISMATCH")
		if f.Path != r.dst2 || f.Op == nil || *f.Op != 3 {
			t.Fatalf("%+v", f)
		}
	})
}

func TestPostcheckFlagsReadBack(t *testing.T) {
	t.Run("wrong subtitle default", func(t *testing.T) {
		r := newRun(t)
		r.m1.Streams[3].Default, r.m1.Streams[4].Default = false, true
		_, rep := r.check()
		f := find(t, rep.Findings, "FLAGS-MISMATCH")
		if count(rep.Findings, "FLAGS-MISMATCH") != 2 || !strings.Contains(f.Detail, "subtitle stream 3 is not default") {
			t.Fatalf("%v", rep.Findings)
		}
		if count(rep.Findings, "TRACKS-CHANGED") != 0 {
			t.Errorf("default flags are not layout: %v", rep.Findings)
		}
	})
	t.Run("a second default audio", func(t *testing.T) {
		r := newRun(t)
		r.m1.Streams[2].Default = true
		_, rep := r.check()
		if !strings.Contains(find(t, rep.Findings, "FLAGS-MISMATCH").Detail, "audio stream 2 is default, and should not be") {
			t.Fatalf("%v", rep.Findings)
		}
	})
	t.Run("a null sub_default wants none", func(t *testing.T) {
		r := newRun(t)
		r.ops = []op{
			{"kind": "rename", "src": r.src1, "dst": r.dst1},
			{"kind": "rename", "src": r.src2, "dst": r.dst2},
			{"kind": "flags", "path": r.dst1, "audio_default": 1, "sub_default": nil},
		}
		r.done = []int{1, 2, 3}
		_, rep := r.check()
		if !strings.Contains(find(t, rep.Findings, "FLAGS-MISMATCH").Detail, "subtitle stream 3 is default") {
			t.Fatalf("%v", rep.Findings)
		}
	})
	t.Run("an index of the wrong type", func(t *testing.T) {
		r := newRun(t)
		r.ops = []op{
			{"kind": "rename", "src": r.src1, "dst": r.dst1},
			{"kind": "rename", "src": r.src2, "dst": r.dst2},
			{"kind": "flags", "path": r.dst1, "audio_default": 3, "sub_default": 3},
		}
		r.done = []int{1, 2, 3}
		_, rep := r.check()
		if !strings.Contains(find(t, rep.Findings, "FLAGS-MISMATCH").Detail, "stream 3 is subtitle, not audio") {
			t.Fatalf("%v", rep.Findings)
		}
	})
}

func TestPostcheckAudioLangReadBack(t *testing.T) {
	t.Run("reads back", func(t *testing.T) {
		r := newRun(t)
		r.before = []inventory.Record{
			{Path: r.src1, Streams: inventoryStreams(r.m1)},
			{Path: r.src2, Streams: func() []inventory.Stream {
				s := inventoryStreams(r.m2)
				und := "und"
				s[1].Lang = &und // the op sets jpn
				return s
			}()},
		}
		if code, rep := r.check(); code != 0 {
			t.Fatalf("%v", rep.Findings)
		}
	})
	t.Run("does not", func(t *testing.T) {
		r := newRun(t)
		r.m2.Streams[1].Lang = "eng"
		_, rep := r.check()
		f := find(t, rep.Findings, "LANG-MISMATCH")
		if f.Path != r.dst2 || !strings.Contains(f.Detail, `"eng", the manifest says "jpn"`) {
			t.Fatalf("%+v", f)
		}
	})
}

func TestPostcheckIntegrity(t *testing.T) {
	t.Run("warnings", func(t *testing.T) {
		r := newRun(t)
		r.m1.Warn = []string{"[matroska] Unknown-sized element"}
		rep := r.wantOnly("INTEGRITY")
		if rep.Findings[0].Path != r.dst1 || !strings.Contains(rep.Findings[0].Detail, "Unknown-sized") {
			t.Fatalf("%+v", rep.Findings[0])
		}
	})
	t.Run("unreadable", func(t *testing.T) {
		r := newRun(t)
		r.m1.Fail = true
		_, rep := r.check()
		if count(rep.Findings, "UNREADABLE") == 0 {
			t.Fatalf("%v", codes(rep.Findings))
		}
	})
	t.Run("only mkv files", func(t *testing.T) {
		r := newRun(t)
		cover := r.fx.plain("SHARE/anime/Show/Extras/cover.jpg")
		r.ops = []op{{"kind": "rename", "src": r.fx.path("SHARE/_pending/rip/cover.jpg"), "dst": cover}}
		r.done = []int{1}
		r.m1.Streams, r.m2.Streams = nil, nil
		_, rep := r.check()
		if n := count(rep.Findings, "INTEGRITY") + count(rep.Findings, "UNREADABLE") + count(rep.Findings, "TITLE-MISMATCH"); n != 0 {
			t.Fatalf("a renamed jpg is not probed: %v", rep.Findings)
		}
		if r.fx.ff.wasAsked(cover) {
			t.Error("probed a jpg")
		}
	})
}

func TestPostcheckBeforeRecord(t *testing.T) {
	t.Run("missing record is a finding", func(t *testing.T) {
		r := newRun(t)
		r.before = []inventory.Record{{Path: r.src1, Streams: inventoryStreams(r.m1)}}
		rep := r.wantOnly("NO-BEFORE")
		if rep.Findings[0].Path != r.src2 {
			t.Fatalf("%+v", rep.Findings[0])
		}
	})
	t.Run("an error record is as good as none", func(t *testing.T) {
		r := newRun(t)
		r.before = []inventory.Record{
			{Path: r.src1, Streams: inventoryStreams(r.m1)},
			{Path: r.src2, Error: "ffprobe exited 1"},
		}
		r.wantOnly("NO-BEFORE")
	})
	t.Run("changed tracks", func(t *testing.T) {
		r := newRun(t)
		r.before = []inventory.Record{
			{Path: r.src1, Streams: inventoryStreams(r.m1)},
			{Path: r.src2, Streams: inventoryStreams(r.m2)},
		}
		r.m1.Streams[2].Codec = "eac3"
		_, rep := r.check()
		if !strings.Contains(find(t, rep.Findings, "TRACKS-CHANGED").Detail, "track 2 codec was ac3, is eac3") {
			t.Fatalf("%v", rep.Findings)
		}
	})
	t.Run("a different count", func(t *testing.T) {
		r := newRun(t)
		r.before = []inventory.Record{
			{Path: r.src1, Streams: inventoryStreams(r.m1)},
			{Path: r.src2, Streams: inventoryStreams(r.m2)},
		}
		r.m1.Streams = r.m1.Streams[:4]
		_, rep := r.check()
		if !strings.Contains(find(t, rep.Findings, "TRACKS-CHANGED").Detail, "5 tracks before, 4 now") {
			t.Fatalf("%v", rep.Findings)
		}
	})
	t.Run("default flags and sizes are not layout", func(t *testing.T) {
		r := newRun(t)
		r.ops = []op{
			{"kind": "rename", "src": r.src1, "dst": r.dst1},
			{"kind": "rename", "src": r.src2, "dst": r.dst2},
		}
		r.done = []int{1, 2}
		r.m1.Streams[4].Default = true
		before := r.m1.clone()
		before.Streams = slices.Clone(before.Streams)
		before.Streams[4].Default = false
		r.before = []inventory.Record{
			{Path: r.src1, Streams: inventoryStreams(before)},
			{Path: r.src2, Streams: inventoryStreams(r.m2)},
		}
		_, rep := r.check()
		if count(rep.Findings, "TRACKS-CHANGED") != 0 {
			t.Fatalf("%v", rep.Findings)
		}
	})
}

func TestPostcheckOneSignaturePerSeasonDir(t *testing.T) {
	r := newRun(t)
	r.m2.Streams[1].Channels = 6
	r.before = nil
	r.before = []inventory.Record{
		{Path: r.src1, Streams: inventoryStreams(r.m1)},
		{Path: r.src2, Streams: inventoryStreams(r.m2)},
	}
	rep := r.wantOnly("SEASON-SIGNATURE")
	f := rep.Findings[0]
	if filepath.Base(f.Path) != "Season 01" || !strings.Contains(f.Detail, "2 distinct track signatures") {
		t.Fatalf("%+v", f)
	}
}

func TestPostcheckSignatureIgnoresDirsThatAreNotSeasons(t *testing.T) {
	r := newRun(t)
	r.dst1 = r.fx.path("SHARE/anime/Show/Specials/Show - S00E01 - A.mkv")
	r.dst2 = r.fx.path("SHARE/anime/Show/Specials/Show - S00E02 - B.mkv")
	r.m2.Streams[1].Channels = 6
	r.before = []inventory.Record{
		{Path: r.src1, Streams: inventoryStreams(r.m1)},
		{Path: r.src2, Streams: inventoryStreams(r.m2)},
	}
	_, rep := r.check()
	if count(rep.Findings, "SEASON-SIGNATURE") != 0 {
		t.Fatalf("%v", rep.Findings)
	}
}

func TestPostcheckDirectoryOps(t *testing.T) {
	setup := func(t *testing.T, src, dst bool) (*fixture, string, string) {
		fx := newFixture(t)
		srcDir := fx.path("SHARE/anime/Old Name")
		dstDir := fx.path("SHARE/anime/New Name")
		if src {
			fx.plain("SHARE/anime/Old Name/leftover.mkv")
		}
		if dst {
			fx.plain("SHARE/anime/New Name/Season 01/New Name - S01E01 - A.mkv")
		}
		return fx, srcDir, dstDir
	}
	check := func(fx *fixture, src, dst string) PostcheckReport {
		r := &pcRun{fx: fx, done: []int{1}, before: []inventory.Record{{Path: "/nowhere"}}}
		r.ops = []op{{"kind": "rename_dir", "src": src, "dst": dst}}
		_, rep := r.check()
		return rep
	}
	fx, src, dst := setup(t, false, true)
	if rep := check(fx, src, dst); count(rep.Findings, "DST-MISSING")+count(rep.Findings, "SRC-REMAINS") != 0 {
		t.Fatalf("a finished directory move: %v", rep.Findings)
	}
	fx, src, dst = setup(t, true, true)
	if rep := check(fx, src, dst); count(rep.Findings, "SRC-REMAINS") != 1 {
		t.Fatalf("%v", rep.Findings)
	}
	fx, src, dst = setup(t, false, false)
	if rep := check(fx, src, dst); count(rep.Findings, "DST-MISSING") != 1 {
		t.Fatalf("%v", rep.Findings)
	}
}

func TestPostcheckRemux(t *testing.T) {
	build := func(archived bool) (*pcRun, PostcheckReport) {
		r := newRun(t)
		archive := r.fx.path("archive/Show/t00.mkv")
		if archived {
			r.fx.plain("archive/Show/t00.mkv")
		}
		r.ops = []op{{"kind": "remux", "src": r.src1, "dst": r.dst1, "archive_dst": archive, "title": "Show"}}
		r.done = []int{1}
		r.m2.Streams = nil
		r.before = []inventory.Record{{Path: r.src1, Streams: inventoryStreams(r.m1)}}
		_, rep := r.check()
		return r, rep
	}
	if _, rep := build(true); !rep.Clean {
		t.Fatalf("%v", rep.Findings)
	}
	if _, rep := build(false); count(rep.Findings, "ARCHIVE-MISSING") != 1 {
		t.Fatalf("%v", rep.Findings)
	}
}

func TestPostcheckFollowsLaterMoves(t *testing.T) {
	t.Run("a title op on a path a later rename moved", func(t *testing.T) {
		r := newRun(t)
		moved := r.fx.path("SHARE/anime/Show/Season 01/Show - S01E01 - Renamed.mkv")
		r.ops = []op{
			{"kind": "rename", "src": r.src1, "dst": r.dst1},
			{"kind": "title", "path": r.dst1, "title": "Show"},
			{"kind": "rename", "src": r.dst1, "dst": moved},
		}
		r.done = []int{1, 2, 3}
		r.dst1 = moved // the file ends up here
		r.m2.Streams = nil
		r.before = []inventory.Record{{Path: r.src1, Streams: inventoryStreams(r.m1)}}
		_, rep := r.check()
		if !rep.Clean {
			t.Fatalf("%v", rep.Findings)
		}
	})
	t.Run("a rename of a staged file that an earlier op produced", func(t *testing.T) {
		r := newRun(t)
		mid := r.fx.path("SHARE/_pending/rip2/t00.mkv")
		r.ops = []op{
			{"kind": "rename", "src": r.src1, "dst": mid},
			{"kind": "rename", "src": mid, "dst": r.dst1},
		}
		r.done = []int{1, 2}
		r.m2.Streams = nil
		r.before = []inventory.Record{{Path: r.src1, Streams: inventoryStreams(r.m1)}}
		_, rep := r.check()
		if !rep.Clean {
			t.Fatalf("the record of the original source answers for the second hop: %v", rep.Findings)
		}
	})
}

func TestLineage(t *testing.T) {
	l := lineage{
		{Kind: manifest.KindRenameDir, Src: "/a/old", Dst: "/a/new"},
		{Kind: manifest.KindRename, Src: "/a/new/x.mkv", Dst: "/a/new/y.mkv"},
		{Kind: manifest.KindRemux, Src: "/a/new/z.mkv", Dst: "/a/new/z2.mkv", ArchiveDst: "/arch/z.mkv"},
	}
	if got := l.final("/a/old/x.mkv", 0); got != "/a/new/y.mkv" {
		t.Errorf("final = %q", got)
	}
	if got := l.final("/a/new/z.mkv", 1); got != "/arch/z.mkv" {
		t.Errorf("a remux source goes to the archive: %q", got)
	}
	if got := l.final("/a/new/z2.mkv", 1); got != "/a/new/z2.mkv" {
		t.Errorf("a remux destination stays: %q", got)
	}
	if got := l.origin("/a/new/y.mkv", 2); got != "/a/old/x.mkv" {
		t.Errorf("origin = %q", got)
	}
	if got := l.final("/elsewhere/x.mkv", 0); got != "/elsewhere/x.mkv" {
		t.Errorf("final of an unrelated path = %q", got)
	}
}

func TestPostcheckLeavesStagingOutOfTheSeriesAudit(t *testing.T) {
	r := newRun(t)
	staged := r.fx.path("SHARE/_pending/Show/Season 01/Show - S01E01 - A.mkv")
	r.ops = []op{{"kind": "rename", "src": r.src1, "dst": staged}}
	r.done = []int{1}
	r.dst1, r.dst2 = staged, ""
	r.m2.Streams = nil
	r.before = []inventory.Record{{Path: r.src1, Streams: inventoryStreams(r.m1)}}
	_, rep := r.check()
	if len(rep.Series) != 0 || !rep.Clean {
		t.Fatalf("series = %v, findings = %v", rep.Series, rep.Findings)
	}
}

func TestPostcheckSeriesFindingsAreCoded(t *testing.T) {
	r := newRun(t)
	r.m1.Title = "Show" // dst1 is right
	r.fx.plain("SHARE/anime/Show/Show - S01E09 - Loose.ogm")
	_, rep := r.check()
	for _, want := range []string{"SERIES:LOOSE", "SERIES:LEGACY"} {
		if count(rep.Findings, want) != 1 {
			t.Errorf("no %s in %v", want, codes(rep.Findings))
		}
	}
	if f := find(t, rep.Findings, "SERIES:LOOSE"); !strings.HasSuffix(f.Path, "/Show") {
		t.Errorf("%+v", f)
	}
}

func TestPostcheckNeverOpensAFIFO(t *testing.T) {
	r := newRun(t)
	fifo := r.fx.path("SHARE/anime/Show/Season 01/Show - S01E03 - C.mkv")
	r.fx.mkdir("SHARE/anime/Show/Season 01")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	r.ops = []op{
		{"kind": "rename", "src": r.src1, "dst": r.dst1},
		{"kind": "rename", "src": r.src2, "dst": r.dst2},
		{"kind": "rename", "src": r.fx.path("SHARE/_pending/rip/t02.mkv"), "dst": fifo},
	}
	r.done = []int{1, 2, 3}
	r.before = []inventory.Record{
		{Path: r.src1, Streams: inventoryStreams(r.m1)},
		{Path: r.src2, Streams: inventoryStreams(r.m2)},
	}
	_, rep := r.check()
	if r.fx.ff.wasAsked(fifo) {
		t.Fatal("a FIFO was probed")
	}
	if count(rep.Findings, "DST-MISSING") != 1 {
		t.Fatalf("a FIFO is not a regular destination: %v", rep.Findings)
	}
}

func TestPostcheckSchemaFindings(t *testing.T) {
	r := newRun(t)
	r.ops = []op{
		{"kind": "rename", "src": r.src1, "dst": r.dst1},
		{"kind": "title", "path": r.dst1, "title": "Show", "bogus": true},
	}
	r.done = []int{1, 2}
	r.m2.Streams = nil
	r.before = []inventory.Record{{Path: r.src1, Streams: inventoryStreams(r.m1)}}
	_, rep := r.check()
	if count(rep.Findings, "SCHEMA") != 1 {
		t.Fatalf("%v", rep.Findings)
	}
}

func TestPostcheckCommandErrors(t *testing.T) {
	r := newRun(t)
	mpath, before := r.build()
	d := r.fx.deps()

	if code, out, errOut := run(t, d, "postcheck", mpath); code != 2 || out != "" || !strings.Contains(errOut, "--before") {
		t.Errorf("no --before: code %d: %s", code, errOut)
	}
	if code, _, _ := run(t, d, "postcheck", mpath, "--before", r.fx.path("missing.json")); code != 2 {
		t.Errorf("missing inventory: code %d", code)
	}
	notInv := r.fx.path("plans/not-inv.json")
	writeFile(t, notInv, `{"nope": 1}`)
	if code, _, _ := run(t, d, "postcheck", mpath, "--before", notInv); code != 2 {
		t.Errorf("not an inventory: code %d", code)
	}
	if code, _, _ := run(t, d, "postcheck", r.fx.path("missing.json"), "--before", before); code != 2 {
		t.Errorf("missing manifest: code %d", code)
	}
	if code, _, _ := run(t, d, "postcheck", mpath, "--before", before, "--workers", "0"); code != 2 {
		t.Errorf("workers: code %d", code)
	}
	if code, _, _ := run(t, d, "postcheck"); code != 2 {
		t.Errorf("no args: code %d", code)
	}

	t.Run("a journal for another manifest is exit 3", func(t *testing.T) {
		writeFile(t, manifest.JournalPath(mpath), `{"event":"start","manifest_sha256":"deadbeef","ops":6}`+"\n")
		code, out, errOut := run(t, d, "postcheck", mpath, "--before", before)
		if code != 3 || out != "" || !strings.Contains(errOut, "journal") {
			t.Fatalf("code %d, stdout %q, stderr %q", code, out, errOut)
		}
	})
	t.Run("a Python-era journal is exit 3", func(t *testing.T) {
		r := newRun(t)
		mpath, before := r.build()
		if err := os.WriteFile(strings.TrimSuffix(mpath, ".json")+".journal", []byte("3\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if code, _, _ := run(t, r.fx.deps(), "postcheck", mpath, "--before", before); code != 3 {
			t.Fatalf("code %d", code)
		}
	})
	t.Run("a manifest that never ran has no journal and nothing is done", func(t *testing.T) {
		r := newRun(t)
		mpath, before := r.build()
		if err := os.Remove(manifest.JournalPath(mpath)); err != nil {
			t.Fatal(err)
		}
		code, out, _ := run(t, r.fx.deps(), "postcheck", mpath, "--before", before)
		if code != 1 || strings.Count(out, "NOT-DONE") != 6 {
			t.Fatalf("code %d: %s", code, out)
		}
	})
}
