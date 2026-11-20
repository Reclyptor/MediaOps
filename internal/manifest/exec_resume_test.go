package manifest

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"

	"github.com/reclyptor/mediaops/internal/cli"
	"github.com/reclyptor/mediaops/internal/library"
	"github.com/reclyptor/mediaops/kit/proc"
)

const dirMarker = "<dir>"

// seed writes a fake media file under MEDIA_ROOT whose bytes depend only on rel,
// so two trees built in different temp dirs start out identical.
func (e *xEnv) seed(rel string) string {
	e.t.Helper()
	p := e.path(rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o770); err != nil {
		e.t.Fatal(err)
	}
	if err := writeFakeMedia(p, sampleMedia(), []byte("payload of "+rel)); err != nil {
		e.t.Fatal(err)
	}
	return p
}

func (e *xEnv) rel(p string) string {
	r, err := filepath.Rel(e.media, p)
	if err != nil {
		e.t.Fatal(err)
	}
	return r
}

// dump is every file (by its bytes) and directory (as dirMarker) under
// MEDIA_ROOT, keyed by path relative to it.
func (e *xEnv) dump() map[string]string {
	e.t.Helper()
	return dumpTree(e.t, e.media)
}

func dumpTree(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil || p == root {
			return err
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		if d.IsDir() {
			out[rel] = dirMarker
			return nil
		}
		data, err := os.ReadFile(p)
		out[rel] = string(data)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func payloadOf(content string) string { return content[headerLen:] }

func moveByHand(t *testing.T, src, dst string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(dst), 0o770); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(src, dst); err != nil {
		t.Fatal(err)
	}
}

func copyByHand(t *testing.T, src, dst string) {
	t.Helper()
	data, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	putFile(t, dst, string(data))
}

func readBytes(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// killInside writes the journal a run killed inside op n leaves: ops done
// journaled done, op n begun with the state its source has right now.
func (e *xEnv) killInside(path string, n int, done ...int) {
	e.t.Helper()
	op := mustLoad(e.t, path).Ops[n-1]
	var st BeginState
	switch {
	case op.IsFileMove():
		st.Size = sizeOf(e.t, op.Src)
	case op.IsDirMove():
		tree, err := library.TreeManifest(op.Src)
		if err != nil {
			e.t.Fatal(err)
		}
		st.Tree = tree
	}
	e.beginOps(path, done, map[int]BeginState{n: st})
}

// dropFinish removes the journal's closing record, which a killed run never
// got to write.
func dropFinish(t *testing.T, manifestPath string) {
	t.Helper()
	data := readBytes(t, JournalPath(manifestPath))
	lines := bytes.Split(bytes.TrimRight(data, "\n"), []byte("\n"))
	last := lines[len(lines)-1]
	if !bytes.Contains(last, []byte(`"event":"finish"`)) {
		t.Fatalf("the journal does not end with finish: %s", last)
	}
	rest := append(bytes.Join(lines[:len(lines)-1], []byte("\n")), '\n')
	if err := os.WriteFile(JournalPath(manifestPath), rest, 0o660); err != nil {
		t.Fatal(err)
	}
}

// cancelDuring returns a context that is cancelled the moment the run first
// touches op's source (or, for edits, its target): by a rename of it or by a
// child that names it. The op itself is not cut short unless a copy reads the
// context afterwards, so on one device the run stops cleanly after it.
func (e *xEnv) cancelDuring(op Op) context.Context {
	e.t.Helper()
	ctx, cancel := context.WithCancel(e.t.Context())
	trigger := op.Src
	if trigger == "" {
		trigger = op.Path
	}
	// A remux is cut by a context cancelled at its mkvmerge call, since the
	// move that archives the source reads it; only its rename is a clean cut.
	if op.Kind != KindRemux {
		e.fake.hook = func(argv []string) {
			if slices.Contains(argv, trigger) {
				cancel()
			}
		}
	}
	rename := e.cfg.Mover.Rename
	if rename == nil {
		rename = os.Rename
	}
	e.cfg.Mover.Rename = func(oldpath, newpath string) error {
		if oldpath == trigger {
			cancel()
		}
		return rename(oldpath, newpath)
	}
	return ctx
}

// multiOps seeds a tree and returns eleven ops, ten of which touch their source
// or target in a way cancelDuring can see.
func (e *xEnv) multiOps() []Op {
	a, b, c := e.seed("nas/show/a.mkv"), e.seed("nas/show/b.mkv"), e.seed("nas/show/c.avi")
	d, f, g, h := e.seed("nas/show/d.mkv"), e.seed("nas/show/e.mkv"), e.seed("nas/show/f.mkv"), e.seed("nas/show/g.mkv")
	for _, dir := range []string{"one", "two", "three"} {
		e.seed("nas/dirs/" + dir + "/x.mkv")
		e.seed("nas/dirs/" + dir + "/sub/y.mkv")
	}
	e.mkdir("nas/empty")
	return []Op{
		{Kind: KindRename, Src: a, Dst: e.path("nas/out/A (2001).mkv"), Title: "Show A", Size: sizeOf(e.t, a)},
		{Kind: KindArchiveFile, Src: b, Dst: filepath.Join(e.archive, "old/b.mkv"), Size: sizeOf(e.t, b)},
		{Kind: KindRemux, Src: c, Dst: e.path("nas/out/C.mkv"), ArchiveDst: filepath.Join(e.archive, "old/c.avi"), Title: "Show C", Size: sizeOf(e.t, c)},
		{Kind: KindTitle, Path: d, Title: "Show D"},
		{Kind: KindFlags, Path: f, AudioDefault: 2, SubDefault: intp(4)},
		{Kind: KindRenameDir, Src: e.path("nas/dirs/one"), Dst: e.path("nas/moved/one")},
		{Kind: KindMoveDir, Src: e.path("nas/dirs/two"), Dst: e.path("nas2/moved/two")},
		{Kind: KindAudioLang, Path: g, Langs: map[int]string{1: "fre", 2: "ger"}},
		{Kind: KindVideoLang, Path: h, Langs: map[int]string{0: "jpn"}},
		{Kind: KindArchiveDir, Src: e.path("nas/dirs/three"), Dst: filepath.Join(e.archive, "dirs/three")},
		{Kind: KindRmdirIfEmpty, Path: e.path("nas/empty")},
	}
}

// remuxCalls is how many times mkvmerge was asked to write a file.
func (f *fakeRunner) remuxCalls() int {
	n := 0
	for _, c := range f.calls {
		if c[0] == "mkvmerge" && c[1] != "-J" {
			n++
		}
	}
	return n
}

// touched reports whether a call of program named file.
func (f *fakeRunner) touched(program, file string) bool {
	for _, c := range f.calls {
		if c[0] == program && slices.Contains(c, file) {
			return true
		}
	}
	return false
}

// filesOnly drops the directories from a dump.
func filesOnly(dump map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range dump {
		if v != dirMarker {
			out[k] = v
		}
	}
	return out
}

func countOf(events []string, name string) int {
	n := 0
	for _, ev := range events {
		if ev == name {
			n++
		}
	}
	return n
}

func TestResumeAfterKillBetweenOps(t *testing.T) {
	ref := newXEnv(t)
	refRep, err := ref.goRun(ref.multiOps()...)
	if err != nil {
		t.Fatalf("uninterrupted run: %v", err)
	}
	want := ref.dump()
	total := len(refRep.Results)

	for k := 1; k < total; k++ {
		t.Run(fmt.Sprintf("after op %d", k), func(t *testing.T) {
			e := newXEnv(t)
			ops := e.multiOps()
			path := e.write(ops...)

			rep, err := e.do(e.cancelDuring(ops[k-1]), path, ExecOptions{Go: true})
			wantCode(t, err, cli.ExitIncomplete)
			if rep.Applied != k {
				t.Fatalf("the first run applied %d ops, want %d: %+v", rep.Applied, k, rep.Results)
			}
			dropFinish(t, path)
			e.fake.hook, e.cfg.Mover.Rename = nil, nil

			rep, err = e.do(t.Context(), path, ExecOptions{Go: true})
			if err != nil {
				t.Fatalf("rerun: %v\n%+v", err, rep)
			}
			if rep.AlreadyDone != k || rep.Applied != total-k || len(rep.Results) != total-k {
				t.Fatalf("rerun: already_done %d, applied %d, results %d; want %d, %d, %d", rep.AlreadyDone, rep.Applied, len(rep.Results), k, total-k, total-k)
			}
			for _, r := range rep.Results {
				if r.Op <= k {
					t.Errorf("op %d was done before the kill and appears again: %+v", r.Op, r)
				}
				if r.Status == StatusPending || r.Status == StatusFailed {
					t.Errorf("op %d ended %s", r.Op, r.Status)
				}
			}

			events := journalEvents(t, path)
			if countOf(events, "start") != 2 || events[len(events)-1] != "finish" || countOf(events, "done 1") != 1 {
				t.Errorf("journal = %v", events)
			}
			if diff := firstMapDifference(want, e.dump()); diff != "" {
				t.Errorf("the resumed tree differs from an uninterrupted run: %s", diff)
			}
		})
	}
}

// firstMapDifference describes the first key, sorted, where got departs from
// want, or "" when they are equal.
func firstMapDifference(want, got map[string]string) string {
	keys := map[string]bool{}
	for k := range want {
		keys[k] = true
	}
	for k := range got {
		keys[k] = true
	}
	sorted := make([]string, 0, len(keys))
	for k := range keys {
		sorted = append(sorted, k)
	}
	slices.Sort(sorted)
	for _, k := range sorted {
		w, inWant := want[k]
		g, inGot := got[k]
		switch {
		case !inGot:
			return k + " is missing"
		case !inWant:
			return k + " is unexpected"
		case w != g:
			return k + " has other contents"
		}
	}
	return ""
}

// doubt is one row of the in-doubt table: a tree arranged the way a death
// inside op 1 would leave it, and what a rerun must make of it.
type doubt struct {
	name  string
	cross bool
	// setup seeds the tree with the source whole and returns the op.
	setup func(e *xEnv) Op
	// arrange turns that tree into the dead run's state.
	arrange func(e *xEnv, op Op)
	outcome Outcome
	// status is what the op's result says; "" means a check finding and nothing touched.
	status  string
	problem string
	// detail must appear in the result's detail.
	detail string
	// verify inspects the tree after a rerun. before is the tree as set up.
	verify func(e *xEnv, op Op, before map[string]string)
}

func runDoubt(t *testing.T, tc doubt) {
	t.Helper()
	e := newXEnv(t)
	if tc.cross {
		e.crossDevice()
	}
	op := tc.setup(e)
	path := e.write(op)
	before := e.dump()
	e.killInside(path, 1)
	if tc.arrange != nil {
		tc.arrange(e, op)
	}
	arranged := e.dump()
	journalBefore := readBytes(t, JournalPath(path))

	// check sees the same picture as the run, and a dry run touches nothing.
	dry, err := e.do(t.Context(), path, ExecOptions{})
	if tc.status != "" {
		if err != nil {
			t.Fatalf("dry run: %v\n%+v", err, dry.Check.Findings)
		}
		if len(dry.Check.InDoubt) != 1 || dry.Check.InDoubt[0].Op != 1 || dry.Check.InDoubt[0].Outcome != tc.outcome {
			t.Fatalf("in doubt = %+v, want op 1 %s", dry.Check.InDoubt, tc.outcome)
		}
		if dry.Results[0].Status != StatusPending {
			t.Fatalf("dry run status = %q", dry.Results[0].Status)
		}
	} else {
		wantCode(t, err, cli.ExitIncomplete)
		if !hasProblem(dry.Check, 1, tc.problem) {
			t.Fatalf("no finding says %q: %+v", tc.problem, dry.Check.Findings)
		}
	}
	if diff := firstMapDifference(arranged, e.dump()); diff != "" || e.fake.total() != 0 {
		t.Fatalf("the dry run touched the tree (%s) or ran %d children", diff, e.fake.total())
	}

	rep, err := e.do(t.Context(), path, ExecOptions{Go: true})
	if tc.status == "" {
		wantCode(t, err, cli.ExitIncomplete)
		if !hasProblem(rep.Check, 1, tc.problem) || rep.Applied != 0 {
			t.Fatalf("report = %+v", rep)
		}
		if diff := firstMapDifference(arranged, e.dump()); diff != "" || e.fake.total() != 0 {
			t.Fatalf("a refused run touched the tree (%s) or ran %d children", diff, e.fake.total())
		}
		if !bytes.Equal(journalBefore, readBytes(t, JournalPath(path))) {
			t.Fatal("a refused run wrote to the journal")
		}
		return
	}
	if err != nil {
		t.Fatalf("exec: %v\n%+v", err, rep)
	}
	if len(rep.Results) != 1 || rep.Results[0].Status != tc.status || rep.Applied != 1 {
		t.Fatalf("results = %+v, want one %s", rep.Results, tc.status)
	}
	if !strings.Contains(rep.Results[0].Detail, tc.detail) {
		t.Errorf("detail %q does not say %q", rep.Results[0].Detail, tc.detail)
	}
	events := journalEvents(t, path)
	if events[len(events)-2] != "done 1" || events[len(events)-1] != "finish" || countOf(events, "start") != 2 {
		t.Errorf("journal = %v", events)
	}
	tc.verify(e, op, before)
}

func runDoubts(t *testing.T, cases []doubt) {
	t.Helper()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) { runDoubt(t, tc) })
	}
}

// verifyFileMoved is the end state of every file move: dst whole with the
// source's payload, src gone.
func verifyFileMoved(e *xEnv, op Op, before map[string]string) {
	e.t.Helper()
	if present(op.Src) {
		e.t.Errorf("%s is still there", op.Src)
	}
	got := string(readBytes(e.t, op.Dst))
	if payloadOf(got) != payloadOf(before[e.rel(op.Src)]) || len(got) != len(before[e.rel(op.Src)]) {
		e.t.Errorf("%s does not hold the source's file", op.Dst)
	}
	if op.Title != "" {
		if title := media(e.t, op.Dst).Title; title != op.Title {
			e.t.Errorf("title of %s = %q, want %q", op.Dst, title, op.Title)
		}
	}
}

func TestResumeInDoubtFileMoves(t *testing.T) {
	kinds := []struct {
		kind string
		dst  func(e *xEnv) string
		note string
	}{
		{KindRename, func(e *xEnv) string { return e.path("nas/out/A (2001).mkv") }, "Show A"},
		{KindArchiveFile, func(e *xEnv) string { return filepath.Join(e.archive, "old/a.mkv") }, ""},
	}
	for _, k := range kinds {
		setup := func(e *xEnv) Op {
			src := e.seed("nas/show/a.mkv")
			return Op{Kind: k.kind, Src: src, Dst: k.dst(e), Title: k.note, Size: sizeOf(e.t, src)}
		}
		t.Run(k.kind, func(t *testing.T) {
			runDoubts(t, []doubt{
				{
					name: "source gone and destination whole is settled", setup: setup, outcome: Settled, status: StatusSettled,
					arrange: func(e *xEnv, op Op) { moveByHand(e.t, op.Src, op.Dst) },
					detail:  "is whole", verify: verifyFileMoved,
				},
				{
					name: "source whole and destination absent is redone", setup: setup, outcome: Redo, status: StatusDone,
					verify: verifyFileMoved,
				},
				{
					name: "both present is a conflict", setup: setup, outcome: Conflict, problem: "both exist",
					arrange: func(e *xEnv, op Op) { copyByHand(e.t, op.Src, op.Dst) },
				},
				{
					name: "destination short with the source gone is a conflict", setup: setup, outcome: Conflict, problem: "not whole",
					arrange: func(e *xEnv, op Op) {
						putFile(e.t, op.Dst, string(readBytes(e.t, op.Src)[:headerLen+3]))
						if err := os.Remove(op.Src); err != nil {
							e.t.Fatal(err)
						}
					},
				},
				{
					name: "cross-device source gone and destination whole is settled", cross: true, setup: setup, outcome: Settled, status: StatusSettled,
					arrange: func(e *xEnv, op Op) { moveByHand(e.t, op.Src, op.Dst) },
					detail:  "is whole", verify: verifyFileMoved,
				},
			})
		})
	}
}

// dirCase seeds a directory of kind with a file, a nested file and a third
// file, and moves it to dst.
func dirSetup(kind string, src string, dst func(e *xEnv) string) func(e *xEnv) Op {
	return func(e *xEnv) Op {
		for _, f := range []string{"a.mkv", "sub/b.mkv", "sub/deep/c.mkv"} {
			e.seed(filepath.Join(src, f))
		}
		return Op{Kind: kind, Src: e.path(src), Dst: dst(e)}
	}
}

func verifyDirMoved(e *xEnv, op Op, before map[string]string) {
	e.t.Helper()
	if present(op.Src) {
		e.t.Errorf("%s is still there", op.Src)
	}
	srcRel, files := e.rel(op.Src)+"/", 0
	for rel, content := range before {
		if !strings.HasPrefix(rel, srcRel) || content == dirMarker {
			continue
		}
		files++
		got := string(readBytes(e.t, filepath.Join(op.Dst, strings.TrimPrefix(rel, srcRel))))
		if got != content {
			e.t.Errorf("%s did not arrive intact", rel)
		}
	}
	got, err := library.TreeManifest(op.Dst)
	if err != nil || len(got) != files {
		e.t.Errorf("%s holds %d files (%v), want %d", op.Dst, len(got), err, files)
	}
}

func TestResumeInDoubtDirMoves(t *testing.T) {
	kinds := []struct {
		kind string
		dst  func(e *xEnv) string
	}{
		{KindRenameDir, func(e *xEnv) string { return e.path("nas/moved/show") }},
		{KindMoveDir, func(e *xEnv) string { return e.path("nas2/moved/show") }},
		{KindArchiveDir, func(e *xEnv) string { return filepath.Join(e.archive, "dirs/show") }},
	}
	for _, k := range kinds {
		setup := dirSetup(k.kind, "nas/show", k.dst)
		t.Run(k.kind, func(t *testing.T) {
			cases := []doubt{
				{
					name: "source gone and destination equal to the record is settled", setup: setup, outcome: Settled, status: StatusSettled,
					arrange: func(e *xEnv, op Op) { moveByHand(e.t, op.Src, op.Dst) },
					detail:  "holds the recorded tree", verify: verifyDirMoved,
				},
				{
					name: "source in place and destination absent is redone", setup: setup, outcome: Redo, status: StatusDone,
					verify: verifyDirMoved,
				},
				{
					name: "a file on both sides is a conflict", setup: setup, outcome: Conflict, problem: "exists at both",
					arrange: func(e *xEnv, op Op) { copyByHand(e.t, filepath.Join(op.Src, "a.mkv"), filepath.Join(op.Dst, "a.mkv")) },
				},
				{
					name: "source gone and destination missing a file is a conflict", setup: setup, outcome: Conflict, problem: "is missing",
					arrange: func(e *xEnv, op Op) {
						moveByHand(e.t, op.Src, op.Dst)
						if err := os.Remove(filepath.Join(op.Dst, "sub/b.mkv")); err != nil {
							e.t.Fatal(err)
						}
					},
				},
			}
			for _, cross := range []bool{false, true} {
				for _, moved := range []string{"a.mkv", "sub/b.mkv", "sub/deep/c.mkv"} {
					cases = append(cases, doubt{
						name:  fmt.Sprintf("partly moved (%s first, cross-device %v) is resumed", moved, cross),
						cross: cross, setup: setup, outcome: Resume, status: StatusDone, detail: "resumed",
						arrange: func(e *xEnv, op Op) { moveByHand(e.t, filepath.Join(op.Src, moved), filepath.Join(op.Dst, moved)) },
						verify:  verifyDirMoved,
					})
				}
			}
			runDoubts(t, cases)
		})
	}
}

func TestResumeInDoubtRemux(t *testing.T) {
	setup := func(e *xEnv) Op {
		src := e.seed("nas/show/c.avi")
		return Op{Kind: KindRemux, Src: src, Dst: e.path("nas/out/C.mkv"), ArchiveDst: filepath.Join(e.archive, "old/c.avi"), Title: "Show C", Size: sizeOf(e.t, src)}
	}
	// verifyRemuxed: the titled copy is at dst, the original is archived whole.
	verifyRemuxed := func(e *xEnv, op Op, before map[string]string) {
		e.t.Helper()
		orig := before[e.rel(op.Src)]
		if present(op.Src) || present(op.Dst+partialSuffix) {
			e.t.Error("the source or a partial is still there")
		}
		if got := string(readBytes(e.t, op.ArchiveDst)); got != orig {
			e.t.Error("the archived original is not the source")
		}
		if got := string(readBytes(e.t, op.Dst)); payloadOf(got) != payloadOf(orig) {
			e.t.Error("dst does not hold the source's streams")
		}
	}
	remuxedByHand := func(e *xEnv, op Op) { copyByHand(e.t, op.Src, op.Dst) }

	runDoubts(t, []doubt{
		{
			name: "source gone with destination and archive present is settled", setup: setup, outcome: Settled, status: StatusSettled,
			arrange: func(e *xEnv, op Op) {
				remuxedByHand(e, op)
				moveByHand(e.t, op.Src, op.ArchiveDst)
			},
			detail: "both", verify: func(e *xEnv, op Op, before map[string]string) {
				e.t.Helper()
				if present(op.Src) || !present(op.Dst) || !present(op.ArchiveDst) {
					e.t.Error("a settled remux changed the tree")
				}
			},
		},
		{
			name: "source whole with a leftover partial is redone", setup: setup, outcome: Redo, status: StatusDone,
			arrange: func(e *xEnv, op Op) { putFile(e.t, op.Dst+partialSuffix, "half a remux") },
			verify: func(e *xEnv, op Op, before map[string]string) {
				e.t.Helper()
				verifyRemuxed(e, op, before)
				if title := media(e.t, op.Dst).Title; title != "Show C" {
					e.t.Errorf("title = %q", title)
				}
			},
		},
		{
			name: "destination written and source not archived is re-verified, titled and archived", setup: setup, outcome: Resume, status: StatusDone,
			arrange: remuxedByHand, detail: "resumed",
			verify: func(e *xEnv, op Op, before map[string]string) {
				e.t.Helper()
				verifyRemuxed(e, op, before)
				if title := media(e.t, op.Dst).Title; title != "Show C" {
					e.t.Errorf("title = %q", title)
				}
				if e.fake.remuxCalls() != 0 || !e.fake.touched("ffprobe", op.Dst) {
					e.t.Errorf("the resume did not verify dst, or it remuxed again: %v", e.fake.calls)
				}
			},
		},
		{
			name: "source and archive present with no destination is a conflict", setup: setup, outcome: Conflict, problem: "mix",
			arrange: func(e *xEnv, op Op) { copyByHand(e.t, op.Src, op.ArchiveDst) },
		},
		{
			name: "all three present is a conflict", setup: setup, outcome: Conflict, problem: "mix",
			arrange: func(e *xEnv, op Op) {
				remuxedByHand(e, op)
				copyByHand(e.t, op.Src, op.ArchiveDst)
			},
		},
		{
			name: "source gone with only the archive is a conflict", setup: setup, outcome: Conflict, problem: "is gone",
			arrange: func(e *xEnv, op Op) { moveByHand(e.t, op.Src, op.ArchiveDst) },
		},
		{
			name: "source gone with only the destination is a conflict", setup: setup, outcome: Conflict, problem: "is gone",
			arrange: func(e *xEnv, op Op) { moveByHand(e.t, op.Src, op.Dst) },
		},
		{
			name: "cross-device destination written and source not archived is resumed", cross: true, setup: setup, outcome: Resume, status: StatusDone,
			arrange: remuxedByHand, detail: "resumed", verify: verifyRemuxed,
		},
	})
}

func TestResumeInDoubtIdempotentKinds(t *testing.T) {
	file := func(op Op) func(e *xEnv) Op {
		return func(e *xEnv) Op {
			op.Path = e.seed("nas/show/d.mkv")
			return op
		}
	}
	rewrite := func(edit func(*fakeMedia)) func(e *xEnv, op Op) {
		return func(e *xEnv, op Op) {
			m, payload, err := readFakeMedia(op.Path)
			if err != nil {
				e.t.Fatal(err)
			}
			edit(&m)
			if err := writeFakeMedia(op.Path, m, payload); err != nil {
				e.t.Fatal(err)
			}
		}
	}
	title := file(Op{Kind: KindTitle, Title: "New"})
	flags := file(Op{Kind: KindFlags, AudioDefault: 2, SubDefault: intp(4)})
	lang := file(Op{Kind: KindAudioLang, Langs: map[int]string{1: "fre"}})
	videoLang := file(Op{Kind: KindVideoLang, Langs: map[int]string{0: "jpn"}})
	rmdir := func(e *xEnv) Op { return Op{Kind: KindRmdirIfEmpty, Path: e.mkdir("nas/empty")} }

	hasTitle := func(e *xEnv, op Op, _ map[string]string) {
		if got := media(e.t, op.Path).Title; got != "New" {
			e.t.Errorf("title = %q", got)
		}
	}
	hasFlags := func(e *xEnv, op Op, _ map[string]string) {
		s := media(e.t, op.Path).Streams
		if s[1].Default || !s[2].Default || s[3].Default || !s[4].Default {
			e.t.Errorf("flags = %+v", s)
		}
	}
	hasLang := func(e *xEnv, op Op, _ map[string]string) {
		if got := media(e.t, op.Path).Streams[1].Lang; got != "fre" {
			e.t.Errorf("language = %q", got)
		}
	}
	hasVideoLang := func(e *xEnv, op Op, _ map[string]string) {
		if got := media(e.t, op.Path).Streams[0].Lang; got != "jpn" {
			e.t.Errorf("video language = %q", got)
		}
	}
	dirGone := func(e *xEnv, op Op, _ map[string]string) {
		if present(op.Path) {
			e.t.Error("the directory is still there")
		}
	}
	runDoubts(t, []doubt{
		{name: "title not yet applied", setup: title, outcome: Redo, status: StatusDone, verify: hasTitle},
		{name: "title already applied", setup: title, outcome: Redo, status: StatusDone, verify: hasTitle,
			arrange: rewrite(func(m *fakeMedia) { m.Title = "New" })},
		{name: "flags not yet applied", setup: flags, outcome: Redo, status: StatusDone, verify: hasFlags},
		{name: "flags already applied", setup: flags, outcome: Redo, status: StatusDone, verify: hasFlags,
			arrange: rewrite(func(m *fakeMedia) {
				m.Streams[1].Default, m.Streams[2].Default, m.Streams[3].Default, m.Streams[4].Default = false, true, false, true
			})},
		{name: "language not yet applied", setup: lang, outcome: Redo, status: StatusDone, verify: hasLang},
		{name: "language already applied", setup: lang, outcome: Redo, status: StatusDone, verify: hasLang,
			arrange: rewrite(func(m *fakeMedia) { m.Streams[1].Lang = "fre" })},
		{name: "video language not yet applied", setup: videoLang, outcome: Redo, status: StatusDone, verify: hasVideoLang},
		{name: "video language already applied", setup: videoLang, outcome: Redo, status: StatusDone, verify: hasVideoLang,
			arrange: rewrite(func(m *fakeMedia) { m.Streams[0].Lang = "jpn" })},
		{name: "rmdir not yet done", setup: rmdir, outcome: Redo, status: StatusDone, verify: dirGone},
		{name: "rmdir already done", setup: rmdir, outcome: Redo, status: StatusSkipped, detail: "already absent", verify: dirGone,
			arrange: func(e *xEnv, op Op) {
				if err := os.Remove(op.Path); err != nil {
					e.t.Fatal(err)
				}
			}},
	})
}

// appendRaw adds bytes to the journal with no newline, as a death mid-write does.
func appendRaw(t *testing.T, manifestPath, text string) {
	t.Helper()
	f, err := os.OpenFile(JournalPath(manifestPath), os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(text); err != nil {
		t.Fatal(err)
	}
}

func TestTornJournalLineIsRepairedAndTheRunCompletes(t *testing.T) {
	cases := []struct {
		name string
		torn string
		// tree puts the files where the dead run left them; ops are three renames.
		tree   func(e *xEnv, ops []Op)
		status []string
	}{
		{
			name: "a begin cut short", torn: `{"event":"begin","op":2,"kind":"ren`,
			tree:   func(e *xEnv, ops []Op) {},
			status: []string{StatusDone, StatusDone},
		},
		{
			name: "a done cut short settles its op", torn: `{"event":"done","op":2,"at":"2026-11-14T0`,
			tree:   func(e *xEnv, ops []Op) { moveByHand(e.t, ops[1].Src, ops[1].Dst) },
			status: []string{StatusSettled, StatusDone},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newXEnv(t)
			var ops []Op
			for _, n := range []string{"a", "b", "c"} {
				src := e.seed("nas/show/" + n + ".mkv")
				ops = append(ops, Op{Kind: KindRename, Src: src, Dst: e.path("nas/out/" + n + ".mkv"), Size: sizeOf(t, src)})
			}
			path := e.write(ops...)
			e.killInside(path, 2, 1)
			moveByHand(t, ops[0].Src, ops[0].Dst)
			if tc.name == "a begin cut short" {
				// op 2 never began: replace its begin with the torn text
				data := readBytes(t, JournalPath(path))
				lines := bytes.Split(bytes.TrimRight(data, "\n"), []byte("\n"))
				data = append(bytes.Join(lines[:len(lines)-1], []byte("\n")), '\n')
				if err := os.WriteFile(JournalPath(path), data, 0o660); err != nil {
					t.Fatal(err)
				}
			}
			appendRaw(t, path, tc.torn)
			tc.tree(e, ops)

			rep, err := e.do(t.Context(), path, ExecOptions{Go: true})
			if err != nil {
				t.Fatalf("exec: %v\n%+v", err, rep)
			}
			if rep.AlreadyDone != 1 || !eqStrings(statuses(rep), tc.status) {
				t.Fatalf("already_done %d, statuses %v, want 1 and %v", rep.AlreadyDone, statuses(rep), tc.status)
			}
			raw := string(readBytes(t, JournalPath(path)))
			if strings.Count(raw, `"event":"repair"`) != 1 || !strings.Contains(raw, tc.torn+"\n") {
				t.Errorf("the journal was not repaired in place:\n%s", raw)
			}
			j, err := ReadJournal(mustLoad(t, path))
			if err != nil || j.DoneCount() != 3 {
				t.Errorf("ReadJournal after the repair: %d done, %v", j.DoneCount(), err)
			}
			if rec := lastRecord(t, path); rec["event"] != "finish" {
				t.Errorf("last record = %v", rec)
			}
			for _, op := range ops {
				if present(op.Src) || !present(op.Dst) {
					t.Errorf("%s did not end up at %s", op.Src, op.Dst)
				}
			}
		})
	}
}

func TestCancelBetweenOpsStopsCleanlyAndARerunFinishes(t *testing.T) {
	e := newXEnv(t)
	var ops []Op
	for _, n := range []string{"a", "b", "c", "d"} {
		ops = append(ops, Op{Kind: KindTitle, Path: e.seed("nas/show/" + n + ".mkv"), Title: "Title " + n})
	}
	path := e.write(ops...)
	ctx, _ := cancelOn(t, e.fake, "mkvpropedit", ops[1].Path)

	rep, err := e.do(ctx, path, ExecOptions{Go: true})
	wantCode(t, err, cli.ExitIncomplete)
	if !eqStrings(statuses(rep), []string{StatusDone, StatusDone, StatusPending, StatusPending}) {
		t.Fatalf("statuses = %v", statuses(rep))
	}
	if !strings.Contains(rep.Error, "stopped before op 3") || rep.FailedOp != nil || rep.Applied != 2 {
		t.Fatalf("error %q, failed_op %v, applied %d", rep.Error, rep.FailedOp, rep.Applied)
	}
	want := []string{"start", "begin 1", "done 1", "begin 2", "done 2", "finish"}
	if got := journalEvents(t, path); !eqStrings(got, want) {
		t.Fatalf("journal = %v, want %v", got, want)
	}
	if media(t, ops[2].Path).Title != "" {
		t.Fatal("op 3 ran after the cancel")
	}

	e.fake.hook = nil
	rep, err = e.do(t.Context(), path, ExecOptions{Go: true})
	if err != nil || rep.AlreadyDone != 2 || !eqStrings(statuses(rep), []string{StatusDone, StatusDone}) {
		t.Fatalf("rerun: %v %+v", err, rep)
	}
	for i, op := range ops {
		if got := media(t, op.Path).Title; got != op.Title {
			t.Errorf("op %d: title %q", i+1, got)
		}
	}
	if got := journalEvents(t, path); got[len(got)-1] != "finish" || countOf(got, "start") != 2 {
		t.Errorf("journal = %v", got)
	}
}

func TestCancelBeforeTheFirstOpBeginsNothing(t *testing.T) {
	e := newXEnv(t)
	op := Op{Kind: KindTitle, Path: e.seed("nas/show/a.mkv"), Title: "A"}
	path := e.write(op)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	rep, err := e.do(ctx, path, ExecOptions{Go: true})
	wantCode(t, err, cli.ExitIncomplete)
	if rep.Applied != 0 || rep.Results[0].Status != StatusPending || e.fake.total() != 0 {
		t.Fatalf("report %+v, %d children", rep, e.fake.total())
	}
	if got := journalEvents(t, path); !eqStrings(got, []string{"start", "finish"}) {
		t.Fatalf("journal = %v", got)
	}
	if rep, err := e.do(t.Context(), path, ExecOptions{Go: true}); err != nil || rep.Applied != 1 {
		t.Fatalf("rerun: %v %+v", err, rep)
	}
}

// cancelOnRename makes the first rename of src (an EXDEV, so the move copies)
// cancel the context, which the copy then sees on its first read.
func (e *xEnv) cancelOnRename(src string) context.Context {
	e.t.Helper()
	ctx, cancel := context.WithCancel(e.t.Context())
	e.cfg.Mover.Rename = func(oldpath, newpath string) error {
		if oldpath == src {
			cancel()
		}
		return &os.LinkError{Op: "rename", Old: oldpath, New: newpath, Err: syscall.EXDEV}
	}
	return ctx
}

func TestCancelDuringACrossDeviceCopyLeavesTheSourceWhole(t *testing.T) {
	e := newXEnv(t)
	src := e.seed("nas/show/a.mkv")
	orig := readBytes(t, src)
	op := Op{Kind: KindRename, Src: src, Dst: e.path("nas2/out/A.mkv"), Title: "A", Size: sizeOf(t, src)}
	path := e.write(op)

	rep, err := e.do(e.cancelOnRename(src), path, ExecOptions{Go: true})
	wantCode(t, err, cli.ExitIncomplete)
	if rep.FailedOp == nil || *rep.FailedOp != 1 || !strings.Contains(rep.Error, "canceled") {
		t.Fatalf("error %q, failed_op %v", rep.Error, rep.FailedOp)
	}
	if !bytes.Equal(readBytes(t, src), orig) || present(op.Dst) || present(op.Dst+partialSuffix) {
		t.Fatal("the cut-short copy lost the source or left a destination behind")
	}
	if got := journalEvents(t, path); !eqStrings(got, []string{"start", "begin 1", "failed 1"}) {
		t.Fatalf("journal = %v", got)
	}

	e.crossDevice()
	rep, err = e.do(t.Context(), path, ExecOptions{Go: true})
	if err != nil || !eqStrings(statuses(rep), []string{StatusDone}) {
		t.Fatalf("rerun: %v %+v", err, rep)
	}
	got := readBytes(t, op.Dst)
	if present(src) || payloadOf(string(got)) != payloadOf(string(orig)) || media(t, op.Dst).Title != "A" {
		t.Fatal("the rerun did not move the file whole and titled")
	}
}

func TestCancelDuringACrossDeviceDirMoveIsResumedByTheRerun(t *testing.T) {
	e := newXEnv(t)
	for _, f := range []string{"a.mkv", "b.mkv", "c.mkv"} {
		e.seed("nas/show/" + f)
	}
	before := e.dump()
	op := Op{Kind: KindMoveDir, Src: e.path("nas/show"), Dst: e.path("nas2/show")}
	path := e.write(op)

	_, err := e.do(e.cancelOnRename(e.path("nas/show/b.mkv")), path, ExecOptions{Go: true})
	wantCode(t, err, cli.ExitIncomplete)
	if !present(e.path("nas2/show/a.mkv")) || !present(e.path("nas/show/b.mkv")) || !present(e.path("nas/show/c.mkv")) {
		t.Fatalf("the interrupted move is not part-way: %v", e.dump())
	}
	for p := range e.dump() {
		if strings.HasSuffix(p, partialSuffix) {
			t.Fatalf("a partial copy was left: %s", p)
		}
	}

	e.crossDevice()
	dry, err := e.do(t.Context(), path, ExecOptions{})
	if err != nil || len(dry.Check.InDoubt) != 1 || dry.Check.InDoubt[0].Outcome != Resume {
		t.Fatalf("dry run: %v %+v", err, dry.Check)
	}
	rep, err := e.do(t.Context(), path, ExecOptions{Go: true})
	if err != nil || !eqStrings(statuses(rep), []string{StatusDone}) || !strings.Contains(rep.Results[0].Detail, "resumed") {
		t.Fatalf("rerun: %v %+v", err, rep)
	}
	if present(op.Src) {
		t.Fatal("the source is still there")
	}
	for _, f := range []string{"a.mkv", "b.mkv", "c.mkv"} {
		if got := string(readBytes(t, e.path("nas2/show/"+f))); got != before["nas/show/"+f] {
			t.Errorf("%s did not arrive intact", f)
		}
	}
}

// killedChild is a runner whose child dies, with the run's context cancelled,
// the first time match fires: leave runs first, so the child can have written
// some of its output.
type killedChild struct {
	*fakeRunner
	cancel context.CancelFunc
	match  func(argv []string) bool
	leave  func()
	fired  bool
}

func (k *killedChild) Run(ctx context.Context, argv ...string) (proc.Result, error) {
	if !k.fired && k.match(argv) {
		k.fired = true
		if k.leave != nil {
			k.leave()
		}
		k.cancel()
		return proc.Result{}, ctx.Err()
	}
	return k.fakeRunner.Run(ctx, argv...)
}

func TestCancelDuringAChildCallLeavesTheOpToBeRedone(t *testing.T) {
	cases := []struct {
		name string
		op   func(e *xEnv) Op
		// match picks the call whose child dies.
		match func(op Op, argv []string) bool
		// leave is what the dying child had already written.
		leave func(op Op)
		// untouched is true when the op's inputs must be exactly as before.
		done func(t *testing.T, e *xEnv, op Op)
	}{
		{
			name:  "a title edit",
			op:    func(e *xEnv) Op { return Op{Kind: KindTitle, Path: e.seed("nas/show/a.mkv"), Title: "Show A"} },
			match: func(op Op, argv []string) bool { return argv[0] == "mkvpropedit" && argv[1] == op.Path },
			done: func(t *testing.T, e *xEnv, op Op) {
				if got := media(t, op.Path).Title; got != "Show A" {
					t.Errorf("title = %q", got)
				}
			},
		},
		{
			name: "a remux",
			op: func(e *xEnv) Op {
				src := e.seed("nas/show/c.avi")
				return Op{Kind: KindRemux, Src: src, Dst: e.path("nas/out/C.mkv"), ArchiveDst: filepath.Join(e.archive, "c.avi"), Title: "Show C", Size: sizeOf(e.t, src)}
			},
			match: func(op Op, argv []string) bool { return argv[0] == "mkvmerge" && argv[1] != "-J" },
			leave: func(op Op) { putFile(t, op.Dst+partialSuffix, "half a remux") },
			done: func(t *testing.T, e *xEnv, op Op) {
				if present(op.Src) || present(op.Dst+partialSuffix) || !present(op.ArchiveDst) || media(t, op.Dst).Title != "Show C" {
					t.Error("the rerun did not finish the remux")
				}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newXEnv(t)
			op := tc.op(e)
			path := e.write(op)
			before := e.dump()

			ctx, cancel := context.WithCancel(t.Context())
			e.cfg.Runner = &killedChild{
				fakeRunner: e.fake, cancel: cancel,
				match: func(argv []string) bool { return tc.match(op, argv) },
				leave: func() {
					if tc.leave != nil {
						tc.leave(op)
					}
				},
			}
			rep, err := e.do(ctx, path, ExecOptions{Go: true})
			wantCode(t, err, cli.ExitIncomplete)
			if rep.FailedOp == nil || *rep.FailedOp != 1 {
				t.Fatalf("report = %+v", rep)
			}
			if got := journalEvents(t, path); !eqStrings(got, []string{"start", "begin 1", "failed 1"}) {
				t.Fatalf("journal = %v", got)
			}
			if diff := firstMapDifference(filesOnly(before), filesOnly(e.dump())); diff != "" {
				t.Fatalf("a killed child changed the files: %s", diff)
			}

			e.cfg.Runner = e.fake
			dry, err := e.do(t.Context(), path, ExecOptions{})
			if err != nil || len(dry.Check.InDoubt) != 1 || dry.Check.InDoubt[0].Outcome != Redo {
				t.Fatalf("dry run: %v %+v", err, dry.Check)
			}
			rep, err = e.do(t.Context(), path, ExecOptions{Go: true})
			if err != nil || !eqStrings(statuses(rep), []string{StatusDone}) {
				t.Fatalf("rerun: %v %+v", err, rep)
			}
			tc.done(t, e, op)
		})
	}
}
