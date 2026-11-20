package manifest

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"golang.org/x/sys/unix"

	"github.com/reclyptor/mediaops/internal/cli"
)

// allKinds builds a tree and a manifest with one op of every kind, in an order
// that leaves each op something to do.
func (e *xEnv) allKinds() (ops []Op, srcAvi string) {
	t := e.t
	renamed := writeMedia(t, e.path("nas/show/a.mkv"))
	archived := writeMedia(t, e.path("nas/show/b.mkv"))
	remuxSrc := writeMedia(t, e.path("nas/show/c.avi"))
	titled := writeMedia(t, e.path("nas/show/d.mkv"))
	flagged := writeMedia(t, e.path("nas/show/e.mkv"))
	langed := writeMedia(t, e.path("nas/show/f.mkv"))
	vlanged := writeMedia(t, e.path("nas/show/g.mkv"))
	for _, dir := range []string{"nas/dirs/one", "nas/dirs/two", "nas/dirs/three"} {
		writeMedia(t, e.path(dir+"/x.mkv"))
		writeMedia(t, e.path(dir+"/sub/y.mkv"))
	}
	e.mkdir("nas/empty")
	e.mkdir("nas/full")
	writeMedia(t, e.path("nas/full/keep.mkv"))

	ops = []Op{
		{Kind: KindRename, Src: renamed, Dst: e.path("nas/out/A (2001).mkv"), Title: "Show A", Size: sizeOf(t, renamed)},
		{Kind: KindArchiveFile, Src: archived, Dst: filepath.Join(e.archive, "old/b.mkv"), Size: sizeOf(t, archived)},
		{Kind: KindRemux, Src: remuxSrc, Dst: e.path("nas/out/C.mkv"), ArchiveDst: filepath.Join(e.archive, "old/c.avi"), Title: "Show C", Size: sizeOf(t, remuxSrc)},
		{Kind: KindTitle, Path: titled, Title: "Show D"},
		{Kind: KindFlags, Path: flagged, AudioDefault: 2, SubDefault: intp(4)},
		{Kind: KindAudioLang, Path: langed, Langs: map[int]string{1: "fre", 2: "ger"}},
		{Kind: KindVideoLang, Path: vlanged, Langs: map[int]string{0: "jpn"}},
		{Kind: KindRenameDir, Src: e.path("nas/dirs/one"), Dst: e.path("nas/moved/one")},
		{Kind: KindMoveDir, Src: e.path("nas/dirs/two"), Dst: e.path("nas2/moved/two")},
		{Kind: KindArchiveDir, Src: e.path("nas/dirs/three"), Dst: filepath.Join(e.archive, "dirs/three")},
		{Kind: KindRmdirIfEmpty, Path: e.path("nas/empty")},
		{Kind: KindRmdirIfEmpty, Path: e.path("nas/full")},
	}
	return ops, remuxSrc
}

func TestExecAppliesEveryKind(t *testing.T) {
	for _, cross := range []bool{false, true} {
		name := "rename"
		if cross {
			name = "copy across devices"
		}
		t.Run(name, func(t *testing.T) {
			e := newXEnv(t)
			if cross {
				e.crossDevice()
			}
			ops, _ := e.allKinds()
			path := e.write(ops...)
			rep, err := e.do(t.Context(), path, ExecOptions{Go: true})
			if err != nil {
				t.Fatalf("exec: %v\n%+v", err, rep)
			}

			want := []string{"done", "done", "done", "done", "done", "done", "done", "done", "done", "done", "done", "skipped"}
			if !eqStrings(statuses(rep), want) {
				t.Fatalf("statuses = %v, want %v\n%+v", statuses(rep), want, rep.Results)
			}
			if rep.DryRun || rep.Applied != 12 || rep.AlreadyDone != 0 || rep.Error != "" || rep.FailedOp != nil {
				t.Fatalf("report = %+v", rep)
			}
			if rep.Journal != JournalPath(path) || !rep.Check.Clean {
				t.Fatalf("journal %q, check %+v", rep.Journal, rep.Check)
			}
			if rep.Results[11].Detail != "not empty, left in place" {
				t.Errorf("skipped detail = %q", rep.Results[11].Detail)
			}

			// rename + title
			a := e.path("nas/out/A (2001).mkv")
			if media(t, a).Title != "Show A" || present(e.path("nas/show/a.mkv")) {
				t.Errorf("rename: title %q, source present %v", media(t, a).Title, present(e.path("nas/show/a.mkv")))
			}
			// archive_file lands under ARCHIVE_ROOT
			if !present(filepath.Join(e.archive, "old/b.mkv")) || present(e.path("nas/show/b.mkv")) {
				t.Error("archive_file did not move the file")
			}
			// remux: titled copy at dst, original archived, no partial left
			if media(t, e.path("nas/out/C.mkv")).Title != "Show C" || !present(filepath.Join(e.archive, "old/c.avi")) {
				t.Error("remux did not write the titled copy and archive the original")
			}
			if present(e.path("nas/out/C.mkv.partial")) || present(e.path("nas/show/c.avi")) {
				t.Error("remux left its partial or its source behind")
			}
			if got := media(t, e.path("nas/show/d.mkv")).Title; got != "Show D" {
				t.Errorf("title = %q", got)
			}
			f := media(t, e.path("nas/show/e.mkv"))
			if f.Streams[1].Default || !f.Streams[2].Default || f.Streams[3].Default || !f.Streams[4].Default {
				t.Errorf("flags = %+v", f.Streams)
			}
			l := media(t, e.path("nas/show/f.mkv"))
			if l.Streams[1].Lang != "fre" || l.Streams[2].Lang != "ger" || l.Streams[3].Lang != "eng" {
				t.Errorf("languages = %+v", l.Streams)
			}
			if got := media(t, e.path("nas/show/g.mkv")).Streams[0].Lang; got != "jpn" {
				t.Errorf("video language = %q", got)
			}
			for _, p := range []string{"nas/moved/one", "nas2/moved/two"} {
				if !present(e.path(p+"/x.mkv")) || !present(e.path(p+"/sub/y.mkv")) {
					t.Errorf("%s is missing its files", p)
				}
			}
			if !present(filepath.Join(e.archive, "dirs/three/sub/y.mkv")) {
				t.Error("archive_dir did not land under ARCHIVE_ROOT")
			}
			for _, p := range []string{"nas/dirs/one", "nas/dirs/two", "nas/dirs/three", "nas/empty"} {
				if present(e.path(p)) {
					t.Errorf("%s should be gone", p)
				}
			}
			if !present(e.path("nas/full/keep.mkv")) {
				t.Error("a non-empty rmdir target lost its file")
			}

			if got := journalEvents(t, path); got[0] != "start" || got[len(got)-1] != "finish" || len(got) != 2+2*12 {
				t.Errorf("journal = %v", got)
			}
			j, err := ReadJournal(mustLoad(t, path))
			if err != nil || j.DoneCount() != 12 {
				t.Errorf("journal done %d, %v", j.DoneCount(), err)
			}
			if rep.Bytes == 0 {
				t.Error("no bytes reported")
			}
		})
	}
}

func mustLoad(t *testing.T, path string) *Manifest {
	t.Helper()
	m, findings, err := Load(path)
	if err != nil || len(findings) > 0 {
		t.Fatalf("Load: %v %v", err, findings)
	}
	return m
}

func TestExecRerunAfterCompletionDoesNothing(t *testing.T) {
	e := newXEnv(t)
	ops, _ := e.allKinds()
	path := e.write(ops...)
	if _, err := e.do(t.Context(), path, ExecOptions{Go: true}); err != nil {
		t.Fatal(err)
	}
	calls := e.fake.total()
	rep, err := e.do(t.Context(), path, ExecOptions{Go: true})
	if err != nil {
		t.Fatalf("rerun: %v", err)
	}
	if rep.AlreadyDone != 12 || len(rep.Results) != 0 || rep.Applied != 0 || e.fake.total() != calls {
		t.Fatalf("rerun did work: %+v, %d new calls", rep, e.fake.total()-calls)
	}
}

func TestExecRemuxReportsWhatItVerified(t *testing.T) {
	e := newXEnv(t)
	src := writeMedia(t, e.path("nas/s/a.avi"), func(m *fakeMedia) {
		m.Streams = append(m.Streams, fakeStream{Type: "attachment", Codec: "ttf"}, fakeStream{Type: "data", Codec: "bin_data"})
		m.DecodeErrors = []string{"error one", "error two"}
	})
	rep, err := e.goRun(Op{Kind: KindRemux, Src: src, Dst: e.path("nas/s/a.mkv"), ArchiveDst: filepath.Join(e.archive, "a.avi"), Title: "T"})
	if err != nil {
		t.Fatal(err)
	}
	d := rep.Results[0].Detail
	if !strings.Contains(d, "2 non-media source stream(s) not compared") || !strings.Contains(d, "2 decode error line(s) were already in the source") {
		t.Fatalf("detail = %q", d)
	}
	if rep.Results[0].Bytes != int64(len(sampleMedia().bytes("payload of "+src))) {
		t.Errorf("bytes = %d", rep.Results[0].Bytes)
	}
}

func TestExecRemuxDecodesTheSourceOnlyWhenTheRemuxHasErrors(t *testing.T) {
	e := newXEnv(t)
	src := writeMedia(t, e.path("nas/s/a.avi"))
	if _, err := e.goRun(Op{Kind: KindRemux, Src: src, Dst: e.path("nas/s/a.mkv"), ArchiveDst: filepath.Join(e.archive, "a.avi"), Title: "T"}); err != nil {
		t.Fatal(err)
	}
	if n := e.fake.count("ffmpeg", ""); n != 1 {
		t.Fatalf("ffmpeg ran %d times on a clean remux, want 1 (the remux only)", n)
	}
}

// remuxCase is a remux whose output the verification must refuse.
type remuxCase struct {
	name string
	src  func(*fakeMedia)
	mod  func(*fakeMedia)
	want string
}

func TestExecRemuxMismatchFailsAndLeavesTheSource(t *testing.T) {
	cases := []remuxCase{
		{"fewer frames", nil, func(m *fakeMedia) { m.Frames-- }, "has 99 video frames, the source has 100"},
		{"more frames", nil, func(m *fakeMedia) { m.Frames++ }, "has 101 video frames"},
		{"no frames at all", func(m *fakeMedia) { m.Frames = 0 }, nil, "has 0 video frames, the source has 0"},
		{"no video in the output", nil, func(m *fakeMedia) { m.Streams = m.Streams[1:] }, "has no video stream"},
		{"shorter", nil, func(m *fakeMedia) { m.Duration -= 0.5 }, "s long, the source is"},
		{"longer", nil, func(m *fakeMedia) { m.Duration += 0.11 }, "s long, the source is"},
		{"codec changed", nil, func(m *fakeMedia) { m.Streams[1].Codec = "mp3" }, "has the streams"},
		{"channels changed", nil, func(m *fakeMedia) { m.Streams[1].Channels = 6 }, "has the streams"},
		{"size changed", nil, func(m *fakeMedia) { m.Streams[0].Width = 320 }, "has the streams"},
		{"stream dropped", nil, func(m *fakeMedia) { m.Streams = m.Streams[:4] }, "has the streams"},
		{"subtitle dropped", nil, func(m *fakeMedia) { m.Streams = append(m.Streams[:3], m.Streams[4:]...) }, "has the streams"},
		{"new decode errors", nil, func(m *fakeMedia) { m.DecodeErrors = []string{"corrupt macroblock"} }, "printed 1 error line(s), the source 0; first: corrupt macroblock"},
		{"more decode errors", func(m *fakeMedia) { m.DecodeErrors = []string{"a"} }, func(m *fakeMedia) { m.DecodeErrors = []string{"a", "b"} }, "printed 2 error line(s), the source 1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newXEnv(t)
			var srcMods []func(*fakeMedia)
			if tc.src != nil {
				srcMods = append(srcMods, tc.src)
			}
			src := writeMedia(t, e.path("nas/s/a.avi"), srcMods...)
			before := treeFiles(t, e.media)
			e.fake.remuxMod = tc.mod
			dst := e.path("nas/s/a.mkv")
			path := e.write(Op{Kind: KindRemux, Src: src, Dst: dst, ArchiveDst: filepath.Join(e.archive, "a.avi"), Title: "T"})
			rep, err := e.do(t.Context(), path, ExecOptions{Go: true})

			wantCode(t, err, cli.ExitIncomplete)
			if !strings.Contains(rep.Error, tc.want) || rep.FailedOp == nil || *rep.FailedOp != 1 || rep.Results[0].Status != StatusFailed {
				t.Fatalf("error %q, failed_op %v, results %+v; want %q", rep.Error, rep.FailedOp, rep.Results, tc.want)
			}
			if !strings.Contains(err.Error(), "op 1 (remux)") {
				t.Errorf("err = %v", err)
			}
			if after := treeFiles(t, e.media); !mapsEqual(before, after) {
				t.Errorf("the tree changed: %v -> %v", keys(before), keys(after))
			}
			if got := journalEvents(t, path); !eqStrings(got, []string{"start", "begin 1", "failed 1"}) {
				t.Errorf("journal = %v", got)
			}
		})
	}
}

func TestExecRemuxWithinDurationToleranceIsAccepted(t *testing.T) {
	e := newXEnv(t)
	src := writeMedia(t, e.path("nas/s/a.avi"))
	e.fake.remuxMod = func(m *fakeMedia) { m.Duration += 0.1 }
	if _, err := e.goRun(Op{Kind: KindRemux, Src: src, Dst: e.path("nas/s/a.mkv"), ArchiveDst: filepath.Join(e.archive, "a.avi"), Title: "T"}); err != nil {
		t.Fatal(err)
	}
}

func TestExecRemuxMuxerMayReorderAcrossTypes(t *testing.T) {
	e := newXEnv(t)
	src := writeMedia(t, e.path("nas/s/a.avi"), func(m *fakeMedia) {
		m.Streams = []fakeStream{m.Streams[1], m.Streams[0], m.Streams[3]}
	})
	if _, err := e.goRun(Op{Kind: KindRemux, Src: src, Dst: e.path("nas/s/a.mkv"), ArchiveDst: filepath.Join(e.archive, "a.avi"), Title: "T"}); err != nil {
		t.Fatal(err)
	}
}

func TestExecRemuxMkvmergeFailure(t *testing.T) {
	e := newXEnv(t)
	src := writeMedia(t, e.path("nas/s/a.avi"))
	e.fake.remuxFail = 2
	rep, err := e.goRun(Op{Kind: KindRemux, Src: src, Dst: e.path("nas/s/a.mkv"), ArchiveDst: filepath.Join(e.archive, "a.avi"), Title: "T"})
	wantCode(t, err, cli.ExitIncomplete)
	if !strings.Contains(rep.Error, "mkvmerge failed remuxing") || present(e.path("nas/s/a.mkv.partial")) || !present(src) {
		t.Fatalf("report %+v", rep)
	}
}

func TestExecRemuxFailureDiscardsPartialAndRerunSucceeds(t *testing.T) {
	e := newXEnv(t)
	src := writeMedia(t, e.path("nas/s/a.avi"))
	e.fake.remuxMod = func(m *fakeMedia) { m.Frames = 1 }
	path := e.write(Op{Kind: KindRemux, Src: src, Dst: e.path("nas/s/a.mkv"), ArchiveDst: filepath.Join(e.archive, "a.avi"), Title: "T"})
	if _, err := e.do(t.Context(), path, ExecOptions{Go: true}); err == nil {
		t.Fatal("a bad remux was accepted")
	}
	if present(e.path("nas/s/a.mkv.partial")) || present(e.path("nas/s/a.mkv")) {
		t.Fatal("the bad output was left in the library")
	}
	e.fake.remuxMod = nil
	rep, err := e.do(t.Context(), path, ExecOptions{Go: true})
	if err != nil || rep.Results[0].Status != StatusDone {
		t.Fatalf("rerun: %v %+v", err, rep)
	}
	if !present(e.path("nas/s/a.mkv")) || !present(filepath.Join(e.archive, "a.avi")) {
		t.Fatal("rerun did not finish the remux")
	}
}

func TestExecRemuxDiscardsALeftoverPartialFirst(t *testing.T) {
	e := newXEnv(t)
	src := writeMedia(t, e.path("nas/s/a.avi"))
	putFile(t, e.path("nas/s/a.mkv.partial"), "garbage from a killed remux")
	rep, err := e.goRun(Op{Kind: KindRemux, Src: src, Dst: e.path("nas/s/a.mkv"), ArchiveDst: filepath.Join(e.archive, "a.avi"), Title: "T"})
	if err != nil || rep.Results[0].Status != StatusDone {
		t.Fatalf("%v %+v", err, rep)
	}
	if present(e.path("nas/s/a.mkv.partial")) || media(t, e.path("nas/s/a.mkv")).Title != "T" {
		t.Fatal("the leftover partial was not replaced")
	}
}

func TestExecEditReadBackMismatchFails(t *testing.T) {
	cases := []struct {
		name string
		set  func(*fakeRunner)
		op   func(path string) Op
		want string
	}{
		{"title", func(f *fakeRunner) { f.ignoreTitle = true }, func(p string) Op { return Op{Kind: KindTitle, Path: p, Title: "New"} }, "reads back as"},
		{"flags", func(f *fakeRunner) { f.ignoreFlags = true }, func(p string) Op { return Op{Kind: KindFlags, Path: p, AudioDefault: 2, SubDefault: intp(4)} }, "default flags"},
		{"audio_lang", func(f *fakeRunner) { f.ignoreLang = true }, func(p string) Op { return Op{Kind: KindAudioLang, Path: p, Langs: map[int]string{1: "fre"}} }, "reads back as"},
		{"video_lang", func(f *fakeRunner) { f.ignoreLang = true }, func(p string) Op { return Op{Kind: KindVideoLang, Path: p, Langs: map[int]string{0: "jpn"}} }, "reads back as"},
		{"flags on a stream that is not audio", func(*fakeRunner) {}, func(p string) Op { return Op{Kind: KindFlags, Path: p, AudioDefault: 3} }, "not an audio stream"},
		{"audio_lang on a stream that is not audio", func(*fakeRunner) {}, func(p string) Op { return Op{Kind: KindAudioLang, Path: p, Langs: map[int]string{0: "fre"}} }, "not an audio stream"},
		{"video_lang on a stream that is not video", func(*fakeRunner) {}, func(p string) Op { return Op{Kind: KindVideoLang, Path: p, Langs: map[int]string{1: "jpn"}} }, "not a video stream"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newXEnv(t)
			tc.set(e.fake)
			p := writeMedia(t, e.path("nas/s/a.mkv"))
			rep, err := e.goRun(tc.op(p))
			wantCode(t, err, cli.ExitIncomplete)
			if !strings.Contains(rep.Error, tc.want) || rep.FailedOp == nil || *rep.FailedOp != 1 {
				t.Fatalf("error %q, failed_op %v; want %q", rep.Error, rep.FailedOp, tc.want)
			}
		})
	}
}

func TestExecRenameTitleReadBackMismatchFails(t *testing.T) {
	e := newXEnv(t)
	e.fake.ignoreTitle = true
	src := writeMedia(t, e.path("nas/s/a.mkv"))
	rep, err := e.goRun(Op{Kind: KindRename, Src: src, Dst: e.path("nas/s/b.mkv"), Title: "T"})
	wantCode(t, err, cli.ExitIncomplete)
	if !strings.Contains(rep.Error, "reads back as") {
		t.Fatalf("error %q", rep.Error)
	}
	if !present(e.path("nas/s/b.mkv")) {
		t.Fatal("the move itself should have happened; the journal holds it in doubt")
	}
}

// brokenMover is a Mover whose rename lies about what it did.
func (e *xEnv) lyingRename(after func(oldpath, newpath string)) {
	e.cfg.Mover.Rename = func(oldpath, newpath string) error {
		if err := os.Rename(oldpath, newpath); err != nil {
			return err
		}
		after(oldpath, newpath)
		return nil
	}
}

func TestExecMoveReadBackMismatchFails(t *testing.T) {
	t.Run("destination changes size", func(t *testing.T) {
		e := newXEnv(t)
		e.lyingRename(func(_, newpath string) {
			f, err := os.OpenFile(newpath, os.O_APPEND|os.O_WRONLY, 0)
			if err != nil {
				t.Fatal(err)
			}
			f.WriteString("extra")
			f.Close()
		})
		src := writeMedia(t, e.path("nas/s/a.mkv"))
		rep, err := e.goRun(Op{Kind: KindRename, Src: src, Dst: e.path("nas/s/b.mkv")})
		wantCode(t, err, cli.ExitIncomplete)
		if !strings.Contains(rep.Error, "bytes after the move") {
			t.Fatalf("error %q", rep.Error)
		}
	})
	t.Run("source survives", func(t *testing.T) {
		e := newXEnv(t)
		var src string
		e.cfg.Mover.Rename = func(oldpath, newpath string) error {
			data, _ := os.ReadFile(oldpath)
			return os.WriteFile(newpath, data, 0o660)
		}
		src = writeMedia(t, e.path("nas/s/a.mkv"))
		rep, err := e.goRun(Op{Kind: KindArchiveFile, Src: src, Dst: filepath.Join(e.archive, "a.mkv")})
		wantCode(t, err, cli.ExitIncomplete)
		if !strings.Contains(rep.Error, "still exists after the move") {
			t.Fatalf("error %q", rep.Error)
		}
	})
	t.Run("directory tree differs", func(t *testing.T) {
		e := newXEnv(t)
		e.lyingRename(func(_, newpath string) {
			if err := os.Remove(filepath.Join(newpath, "two.mkv")); err != nil {
				t.Fatal(err)
			}
		})
		writeMedia(t, e.path("nas/d/one.mkv"))
		writeMedia(t, e.path("nas/d/two.mkv"))
		rep, err := e.goRun(Op{Kind: KindRenameDir, Src: e.path("nas/d"), Dst: e.path("nas/e")})
		wantCode(t, err, cli.ExitIncomplete)
		if !strings.Contains(rep.Error, "does not hold the tree") || !strings.Contains(rep.Error, "two.mkv is missing") {
			t.Fatalf("error %q", rep.Error)
		}
	})
	t.Run("directory source survives", func(t *testing.T) {
		e := newXEnv(t)
		e.cfg.Mover.Rename = func(oldpath, newpath string) error {
			return os.MkdirAll(newpath, 0o770)
		}
		writeMedia(t, e.path("nas/d/one.mkv"))
		rep, err := e.goRun(Op{Kind: KindRenameDir, Src: e.path("nas/d"), Dst: e.path("nas/e")})
		wantCode(t, err, cli.ExitIncomplete)
		if !strings.Contains(rep.Error, "does not hold the tree") {
			t.Fatalf("error %q", rep.Error)
		}
	})
}

// sabotage changes the tree between the check and the op, which is how a
// precondition fails at exec time.
func TestExecRefusesWhenAPreconditionFailsAtExecTime(t *testing.T) {
	type tc struct {
		name  string
		build func(e *xEnv) (ops []Op, sabotage func())
		want  string
	}
	anchor := func(e *xEnv) Op {
		return Op{Kind: KindTitle, Path: writeMedia(t, e.path("nas/anchor.mkv")), Title: "Anchor"}
	}
	cases := []tc{
		{"rename source vanishes", func(e *xEnv) ([]Op, func()) {
			src := writeMedia(t, e.path("nas/a.mkv"))
			return []Op{anchor(e), {Kind: KindRename, Src: src, Dst: e.path("nas/b.mkv")}}, func() { os.Remove(src) }
		}, "is not a regular file"},
		{"rename source becomes a symlink", func(e *xEnv) ([]Op, func()) {
			src := writeMedia(t, e.path("nas/a.mkv"))
			other := writeMedia(t, e.path("nas/other.mkv"))
			return []Op{anchor(e), {Kind: KindRename, Src: src, Dst: e.path("nas/b.mkv")}}, func() { os.Remove(src); os.Symlink(other, src) }
		}, "is not a regular file"},
		{"rename source changes size", func(e *xEnv) ([]Op, func()) {
			src := writeMedia(t, e.path("nas/a.mkv"))
			size := sizeOf(t, src)
			return []Op{anchor(e), {Kind: KindRename, Src: src, Dst: e.path("nas/b.mkv"), Size: size}}, func() { os.WriteFile(src, []byte("short"), 0o660) }
		}, "not the recorded"},
		{"rename destination appears", func(e *xEnv) ([]Op, func()) {
			src := writeMedia(t, e.path("nas/a.mkv"))
			return []Op{anchor(e), {Kind: KindRename, Src: src, Dst: e.path("nas/b.mkv")}}, func() { putFile(t, e.path("nas/b.mkv"), "x") }
		}, "already exists"},
		{"archive_file destination appears", func(e *xEnv) ([]Op, func()) {
			src := writeMedia(t, e.path("nas/a.mkv"))
			dst := filepath.Join(e.archive, "a.mkv")
			return []Op{anchor(e), {Kind: KindArchiveFile, Src: src, Dst: dst}}, func() { putFile(t, dst, "x") }
		}, "already exists"},
		{"directory source becomes a file", func(e *xEnv) ([]Op, func()) {
			writeMedia(t, e.path("nas/d/x.mkv"))
			return []Op{anchor(e), {Kind: KindMoveDir, Src: e.path("nas/d"), Dst: e.path("nas/e")}}, func() {
				os.RemoveAll(e.path("nas/d"))
				putFile(t, e.path("nas/d"), "x")
			}
		}, "is not a directory"},
		{"directory destination appears", func(e *xEnv) ([]Op, func()) {
			writeMedia(t, e.path("nas/d/x.mkv"))
			return []Op{anchor(e), {Kind: KindArchiveDir, Src: e.path("nas/d"), Dst: filepath.Join(e.archive, "d")}}, func() { e.mkdir("nas2/archive/d") }
		}, "already exists"},
		{"directory holds a symlink", func(e *xEnv) ([]Op, func()) {
			writeMedia(t, e.path("nas/d/x.mkv"))
			return []Op{anchor(e), {Kind: KindRenameDir, Src: e.path("nas/d"), Dst: e.path("nas/e")}}, func() { os.Symlink("x.mkv", e.path("nas/d/link")) }
		}, "symlink"},
		{"remux source vanishes", func(e *xEnv) ([]Op, func()) {
			src := writeMedia(t, e.path("nas/a.avi"))
			return []Op{anchor(e), {Kind: KindRemux, Src: src, Dst: e.path("nas/a.mkv"), ArchiveDst: filepath.Join(e.archive, "a.avi"), Title: "T"}}, func() { os.Remove(src) }
		}, "is not a regular file"},
		{"remux destination appears", func(e *xEnv) ([]Op, func()) {
			src := writeMedia(t, e.path("nas/a.avi"))
			return []Op{anchor(e), {Kind: KindRemux, Src: src, Dst: e.path("nas/a.mkv"), ArchiveDst: filepath.Join(e.archive, "a.avi"), Title: "T"}}, func() { putFile(t, e.path("nas/a.mkv"), "x") }
		}, "already exists"},
		{"remux archive destination appears", func(e *xEnv) ([]Op, func()) {
			src := writeMedia(t, e.path("nas/a.avi"))
			arc := filepath.Join(e.archive, "a.avi")
			return []Op{anchor(e), {Kind: KindRemux, Src: src, Dst: e.path("nas/a.mkv"), ArchiveDst: arc, Title: "T"}}, func() { putFile(t, arc, "x") }
		}, "archive destination"},
		{"title target vanishes", func(e *xEnv) ([]Op, func()) {
			p := writeMedia(t, e.path("nas/a.mkv"))
			return []Op{anchor(e), {Kind: KindTitle, Path: p, Title: "T"}}, func() { os.Remove(p) }
		}, "is not a regular file"},
		{"flags target vanishes", func(e *xEnv) ([]Op, func()) {
			p := writeMedia(t, e.path("nas/a.mkv"))
			return []Op{anchor(e), {Kind: KindFlags, Path: p, AudioDefault: 1}}, func() { os.Remove(p) }
		}, "is not a regular file"},
		{"audio_lang target vanishes", func(e *xEnv) ([]Op, func()) {
			p := writeMedia(t, e.path("nas/a.mkv"))
			return []Op{anchor(e), {Kind: KindAudioLang, Path: p, Langs: map[int]string{1: "fre"}}}, func() { os.Remove(p) }
		}, "is not a regular file"},
		{"video_lang target vanishes", func(e *xEnv) ([]Op, func()) {
			p := writeMedia(t, e.path("nas/a.mkv"))
			return []Op{anchor(e), {Kind: KindVideoLang, Path: p, Langs: map[int]string{0: "jpn"}}}, func() { os.Remove(p) }
		}, "is not a regular file"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := newXEnv(t)
			ops, sabotage := c.build(e)
			armed := false
			e.fake.hook = func(argv []string) {
				if !armed && argv[0] == "mkvpropedit" {
					armed = true
					sabotage()
				}
			}
			path := e.write(ops...)
			rep, err := e.do(t.Context(), path, ExecOptions{Go: true})
			wantCode(t, err, cli.ExitIncomplete)
			if !strings.Contains(rep.Error, c.want) {
				t.Fatalf("error %q lacks %q", rep.Error, c.want)
			}
			if rep.FailedOp == nil || *rep.FailedOp != 2 || rep.Applied != 1 {
				t.Fatalf("failed_op %v, applied %d", rep.FailedOp, rep.Applied)
			}
			if got := journalEvents(t, path); !eqStrings(got, []string{"start", "begin 1", "done 1", "failed 2"}) {
				t.Errorf("a refused op must fail before it begins; journal = %v", got)
			}
			if rep.Results[0].Status != StatusDone || rep.Results[1].Status != StatusFailed {
				t.Errorf("statuses = %v", statuses(rep))
			}
		})
	}
}

func TestExecStopsAtTheFirstFailureAndLeavesLaterOpsPending(t *testing.T) {
	e := newXEnv(t)
	a := writeMedia(t, e.path("nas/a.mkv"))
	b := writeMedia(t, e.path("nas/b.mkv"))
	c := writeMedia(t, e.path("nas/c.mkv"))
	path := e.write(Op{Kind: KindTitle, Path: a, Title: "A"}, Op{Kind: KindTitle, Path: b, Title: "B"}, Op{Kind: KindTitle, Path: c, Title: "C"})
	e.fake.hook = func(argv []string) {
		if argv[0] == "mkvpropedit" && argv[1] == a {
			os.Remove(b)
		}
	}
	rep, err := e.do(t.Context(), path, ExecOptions{Go: true})
	wantCode(t, err, cli.ExitIncomplete)
	if !eqStrings(statuses(rep), []string{"done", "failed", "pending"}) {
		t.Fatalf("statuses = %v", statuses(rep))
	}
	if e.fake.count("mkvpropedit", c) != 0 || rep.Applied != 1 {
		t.Fatal("an op after the failure ran")
	}
}

func TestExecWithoutARunnerRefusesToApply(t *testing.T) {
	e := newXEnv(t)
	e.cfg.Runner = nil
	a := writeMedia(t, e.path("nas/a.mkv"))
	path := e.write(Op{Kind: KindTitle, Path: a, Title: "A"})
	if _, err := e.do(t.Context(), path, ExecOptions{Go: true}); err == nil || cli.Code(err) != cli.ExitInternal {
		t.Fatalf("err = %v", err)
	}
	if _, err := e.do(t.Context(), path, ExecOptions{}); err != nil {
		t.Fatalf("a dry run needs no runner: %v", err)
	}
}

func TestExecDryRunTouchesNothing(t *testing.T) {
	e := newXEnv(t)
	ops, _ := e.allKinds()
	path := e.write(ops...)
	base := filepath.Dir(e.media)
	before := snapshot(t, base)
	files := treeFiles(t, base)

	rep, err := e.do(t.Context(), path, ExecOptions{})
	if err != nil {
		t.Fatalf("dry run: %v", err)
	}
	if !rep.DryRun || rep.Applied != 0 || len(rep.Results) != 12 || rep.Error != "" || rep.Bytes != rep.Check.Bytes || rep.Bytes == 0 {
		t.Fatalf("report = %+v", rep)
	}
	for _, s := range statuses(rep) {
		if s != StatusPending {
			t.Fatalf("statuses = %v", statuses(rep))
		}
	}
	if e.fake.total() != 0 {
		t.Errorf("a dry run started %d processes", e.fake.total())
	}
	after := snapshot(t, base)
	if len(after) != len(before) {
		t.Fatalf("paths %d -> %d", len(before), len(after))
	}
	for p, v := range before {
		if after[p] != v {
			t.Errorf("%s changed: %q -> %q", p, v, after[p])
		}
	}
	if !mapsEqual(files, treeFiles(t, base)) {
		t.Error("file contents changed")
	}
	if present(JournalPath(path)) {
		t.Error("a dry run created a journal")
	}
	if entries, _ := os.ReadDir(e.cfg.LockDir); len(entries) != 0 {
		t.Errorf("a dry run created lock files: %v", entries)
	}
}

func TestExecFindingsTouchNothing(t *testing.T) {
	for _, goFlag := range []bool{false, true} {
		t.Run(map[bool]string{false: "dry run", true: "--go"}[goFlag], func(t *testing.T) {
			e := newXEnv(t)
			ok := writeMedia(t, e.path("nas/ok.mkv"))
			path := e.write(
				Op{Kind: KindTitle, Path: ok, Title: "T"},
				Op{Kind: KindRename, Src: e.path("nas/missing.mkv"), Dst: e.path("nas/x.mkv")},
			)
			base := filepath.Dir(e.media)
			outsideLocks := func() map[string]string {
				snap := snapshot(t, base)
				for p := range snap {
					if p == e.cfg.LockDir || strings.HasPrefix(p, e.cfg.LockDir+string(filepath.Separator)) {
						delete(snap, p)
					}
				}
				return snap
			}
			before := outsideLocks()
			lockBefore, _ := os.ReadDir(e.cfg.LockDir)

			rep, err := e.do(t.Context(), path, ExecOptions{Go: goFlag})
			wantCode(t, err, cli.ExitIncomplete)
			if err.(*cli.Error).Err != nil {
				t.Errorf("findings exit silently, got %v", err)
			}
			if rep.Check.Clean || len(rep.Check.Findings) != 1 || len(rep.Results) != 0 || rep.Applied != 0 || rep.Error != "" {
				t.Fatalf("report = %+v", rep)
			}
			if rep.DryRun == goFlag {
				t.Errorf("dry_run = %v", rep.DryRun)
			}
			after := outsideLocks()
			for p, v := range before {
				if after[p] != v {
					t.Errorf("%s changed: %q -> %q", p, v, after[p])
				}
			}
			if len(after) != len(before) {
				t.Errorf("paths %d -> %d", len(before), len(after))
			}
			if present(JournalPath(path)) || e.fake.total() != 0 {
				t.Error("findings must create no journal and start no process")
			}
			// --go takes the lock first, so a lock file is the one thing it may leave.
			lockAfter, _ := os.ReadDir(e.cfg.LockDir)
			if want := len(lockBefore) + map[bool]int{false: 0, true: 1}[goFlag]; len(lockAfter) != want {
				t.Errorf("lock dir has %d entries, want %d", len(lockAfter), want)
			}
		})
	}
}

func TestExecRefusals(t *testing.T) {
	t.Run("archive destination outside ARCHIVE_ROOT is a finding and nothing moves", func(t *testing.T) {
		e := newXEnv(t)
		a := writeMedia(t, e.path("nas/a.mkv"))
		b := writeMedia(t, e.path("nas/b.mkv"))
		path := e.write(
			Op{Kind: KindTitle, Path: b, Title: "T"},
			Op{Kind: KindArchiveFile, Src: a, Dst: e.path("nas/elsewhere/a.mkv")},
		)
		rep, err := e.do(t.Context(), path, ExecOptions{Go: true})
		wantCode(t, err, cli.ExitIncomplete)
		if !hasProblem(rep.Check, 2, "outside ARCHIVE_ROOT") {
			t.Fatalf("findings = %v", problems(rep.Check))
		}
		if !present(a) || media(t, b).Title != "" || present(JournalPath(path)) {
			t.Fatal("something ran")
		}
	})
	t.Run("unusable ARCHIVE_ROOT with archive ops is exit 3", func(t *testing.T) {
		e := newXEnv(t)
		a := writeMedia(t, e.path("nas/a.mkv"))
		e.cfg.Paths.Archive = ""
		path := e.write(Op{Kind: KindArchiveFile, Src: a, Dst: filepath.Join(e.archive, "a.mkv")})
		for _, goFlag := range []bool{false, true} {
			if _, err := e.do(t.Context(), path, ExecOptions{Go: goFlag}); cli.Code(err) != cli.ExitRefused || !strings.Contains(err.Error(), "ARCHIVE_ROOT") {
				t.Fatalf("go=%v: %v", goFlag, err)
			}
		}
	})
	t.Run("a Python-era journal is exit 3", func(t *testing.T) {
		e := newXEnv(t)
		a := writeMedia(t, e.path("nas/a.mkv"))
		path := e.write(Op{Kind: KindTitle, Path: a, Title: "T"})
		putFile(t, strings.TrimSuffix(path, ".json")+".journal", "[1]")
		for _, goFlag := range []bool{false, true} {
			if _, err := e.do(t.Context(), path, ExecOptions{Go: goFlag}); cli.Code(err) != cli.ExitRefused || !strings.Contains(err.Error(), "Python-era") {
				t.Fatalf("go=%v: %v", goFlag, err)
			}
		}
		if media(t, a).Title != "" {
			t.Fatal("the op ran")
		}
	})
	t.Run("a manifest changed after partial progress is exit 3", func(t *testing.T) {
		e := newXEnv(t)
		a := writeMedia(t, e.path("nas/a.mkv"))
		b := writeMedia(t, e.path("nas/b.mkv"))
		ops := []Op{{Kind: KindTitle, Path: a, Title: "A"}, {Kind: KindTitle, Path: b, Title: "B"}}
		path := e.write(ops...)
		ctx, cancel := cancelOn(t, e.fake, "mkvpropedit", a)
		defer cancel()
		if _, err := e.do(ctx, path, ExecOptions{Go: true}); cli.Code(err) != cli.ExitIncomplete {
			t.Fatalf("first run: %v", err)
		}
		ops[1].Title = "B changed"
		e.write(ops...)
		_, err := e.do(t.Context(), path, ExecOptions{Go: true})
		if cli.Code(err) != cli.ExitRefused || !strings.Contains(err.Error(), "the manifest changed after 1 ops were applied") {
			t.Fatalf("err = %v", err)
		}
		if media(t, b).Title != "" {
			t.Fatal("the changed manifest ran")
		}
	})
	t.Run("a second exec --go on one manifest is exit 3", func(t *testing.T) {
		e := newXEnv(t)
		a := writeMedia(t, e.path("nas/a.mkv"))
		path := e.write(Op{Kind: KindTitle, Path: a, Title: "A"})
		release, err := e.cfg.Lock(mustLoad(t, path))
		if err != nil {
			t.Fatal(err)
		}
		defer release()
		if _, err := e.do(t.Context(), path, ExecOptions{Go: true}); cli.Code(err) != cli.ExitRefused || !strings.Contains(err.Error(), "another exec --go") {
			t.Fatalf("err = %v", err)
		}
		if _, err := e.do(t.Context(), path, ExecOptions{}); err != nil {
			t.Fatalf("a dry run does not need the lock: %v", err)
		}
		if media(t, a).Title != "" || present(JournalPath(path)) {
			t.Fatal("the locked-out run did something")
		}
	})
	t.Run("a journal that cannot be created is exit 3", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root can write anywhere")
		}
		e := newXEnv(t)
		a := writeMedia(t, e.path("nas/a.mkv"))
		path := e.write(Op{Kind: KindTitle, Path: a, Title: "A"})
		if err := os.Chmod(e.plans, 0o500); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { os.Chmod(e.plans, 0o770) })
		if _, err := e.do(t.Context(), path, ExecOptions{Go: true}); cli.Code(err) != cli.ExitRefused || !strings.Contains(err.Error(), "the journal cannot be created") {
			t.Fatalf("err = %v", err)
		}
		if media(t, a).Title != "" {
			t.Fatal("the op ran without a journal")
		}
	})
	t.Run("a journal that cannot be written is exit 4", func(t *testing.T) {
		full, err := os.OpenFile("/dev/full", os.O_WRONLY, 0)
		if err != nil {
			t.Skip("no /dev/full")
		}
		defer full.Close()
		e := newXEnv(t)
		a := writeMedia(t, e.path("nas/a.mkv"))
		path := e.write(Op{Kind: KindTitle, Path: a, Title: "A"})
		journal := JournalPath(path)
		e.fake.hook = func(argv []string) {
			if argv[0] != "mkvpropedit" {
				return
			}
			e.fake.hook = nil
			fds, err := os.ReadDir("/proc/self/fd")
			if err != nil {
				t.Errorf("reading /proc/self/fd: %v", err)
				return
			}
			for _, fd := range fds {
				target, err := os.Readlink("/proc/self/fd/" + fd.Name())
				if err != nil || target != journal {
					continue
				}
				n, err := strconv.Atoi(fd.Name())
				if err != nil {
					t.Errorf("fd name %q: %v", fd.Name(), err)
					return
				}
				if err := unix.Dup2(int(full.Fd()), n); err != nil {
					t.Errorf("dup2 onto the journal fd: %v", err)
				}
				return
			}
			t.Errorf("no open fd for %s", journal)
		}
		_, err = e.do(t.Context(), path, ExecOptions{Go: true})
		if cli.Code(err) != cli.ExitInternal || !strings.Contains(err.Error(), journal) {
			t.Fatalf("err = %v", err)
		}
		if got, want := journalEvents(t, path), []string{"start", "begin 1"}; !reflect.DeepEqual(got, want) {
			t.Fatalf("journal = %v, want %v", got, want)
		}
		rep, err := e.do(t.Context(), path, ExecOptions{Go: true})
		if err != nil {
			t.Fatalf("rerun: %v", err)
		}
		if rep.Applied != 1 || media(t, a).Title != "A" {
			t.Fatalf("rerun applied %d, title %q", rep.Applied, media(t, a).Title)
		}
		events := journalEvents(t, path)
		if n := len(events); n < 2 || events[n-2] != "done 1" || events[n-1] != "finish" {
			t.Fatalf("journal = %v, want it to end done 1, finish", events)
		}
	})
	t.Run("a journal that is not a regular file is exit 3", func(t *testing.T) {
		for name, mk := range map[string]func(t *testing.T, journal string){
			"symlink to /dev/zero": func(t *testing.T, journal string) {
				if err := os.Symlink("/dev/zero", journal); err != nil {
					t.Fatal(err)
				}
			},
			"fifo": func(t *testing.T, journal string) {
				if err := unix.Mkfifo(journal, 0o600); err != nil {
					t.Fatal(err)
				}
			},
			"directory": func(t *testing.T, journal string) {
				if err := os.Mkdir(journal, 0o700); err != nil {
					t.Fatal(err)
				}
			},
		} {
			for _, goFlag := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s go=%v", name, goFlag), func(t *testing.T) {
					e := newXEnv(t)
					a := writeMedia(t, e.path("nas/a.mkv"))
					path := e.write(Op{Kind: KindTitle, Path: a, Title: "A"})
					mk(t, JournalPath(path))
					_, err := e.do(t.Context(), path, ExecOptions{Go: goFlag})
					if cli.Code(err) != cli.ExitRefused || !strings.Contains(err.Error(), "not a regular file") {
						t.Fatalf("err = %v", err)
					}
					if media(t, a).Title != "" || e.fake.total() != 0 {
						t.Fatal("something ran against an unreadable journal")
					}
				})
			}
		}
	})
}

func mapsEqual(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

func keys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
