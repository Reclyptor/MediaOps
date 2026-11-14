package manifest

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/reclyptor/mediaops/internal/cli"
)

var execFixtureNames = []string{"penguindrum", "outlanders", "films"}

// fixtureRun is one fixture's generated tree with the fake tools wired in.
type fixtureRun struct {
	t     *testing.T
	cfg   Config
	path  string
	fake  *fakeRunner
	media string
	m     *Manifest
}

// newFixtureRun generates the tree for testdata/<name>.json. Every file is fake
// media whose bytes depend only on its path under MEDIA_ROOT, so equal trees
// built in different temp dirs compare equal byte for byte. The fixtures carry
// no size fields, so nothing else has to match.
func newFixtureRun(t *testing.T, name string, crossDevice bool) *fixtureRun {
	t.Helper()
	cfg, path := buildFixtureRel(t, name, func(rel string) []byte {
		return sampleMedia().bytes("payload of " + rel)
	})
	fake := &fakeRunner{}
	cfg.Runner = fake
	if crossDevice {
		cfg.Mover = exdevMover()
	}
	return &fixtureRun{t: t, cfg: cfg, path: path, fake: fake, media: cfg.Paths.MediaRoot, m: mustLoad(t, path)}
}

func (f *fixtureRun) exec(ctx context.Context, opts ExecOptions) (ExecReport, error) {
	f.t.Helper()
	m, schema, err := Load(f.path)
	if err != nil {
		f.t.Fatal(err)
	}
	return f.cfg.Exec(ctx, m, schema, opts)
}

func (f *fixtureRun) dump() map[string]string {
	f.t.Helper()
	return dumpTree(f.t, f.media)
}

// fixtureFile is what a generated file should be after the run.
type fixtureFile struct {
	size  int64
	title string
	flags *Op
	langs map[int]string
}

// expectedFiles replays the ops over the files the tree starts with: where each
// file ends up, its size, its title and the flag and language edits it got.
func (f *fixtureRun) expectedFiles() map[string]fixtureFile {
	f.t.Helper()
	files := map[string]fixtureFile{}
	for rel, content := range filesOnly(f.dump()) {
		files[filepath.Join(f.media, rel)] = fixtureFile{size: int64(len(content))}
	}
	for _, op := range f.m.Ops {
		switch op.Kind {
		case KindRename, KindArchiveFile:
			file := files[op.Src]
			delete(files, op.Src)
			if op.Title != "" {
				file.title = op.Title
			}
			files[op.Dst] = file
		case KindRemux:
			file := files[op.Src]
			delete(files, op.Src)
			files[op.ArchiveDst] = file
			file.title = op.Title
			files[op.Dst] = file
		case KindRenameDir, KindMoveDir, KindArchiveDir:
			for p, file := range files {
				if within(op.Src, p) {
					delete(files, p)
					files[filepath.Join(op.Dst, strings.TrimPrefix(p, op.Src))] = file
				}
			}
		case KindTitle:
			file := files[op.Path]
			file.title = op.Title
			files[op.Path] = file
		case KindFlags:
			file := files[op.Path]
			file.flags = &op
			files[op.Path] = file
		case KindAudioLang:
			file := files[op.Path]
			file.langs = op.Langs
			files[op.Path] = file
		}
	}
	return files
}

// verifyApplied holds the tree to what the ops promise: every destination is
// where it should be with the size it had, every source is gone, every title
// is on its file, and every flag and language edit reads back.
func (f *fixtureRun) verifyApplied(want map[string]fixtureFile) {
	t := f.t
	t.Helper()
	got := map[string]bool{}
	for rel := range filesOnly(f.dump()) {
		got[filepath.Join(f.media, rel)] = true
	}
	for p := range got {
		if _, ok := want[p]; !ok {
			t.Errorf("unexpected file %s", p)
		}
	}
	for p, file := range want {
		if !got[p] {
			t.Errorf("missing file %s", p)
			continue
		}
		info, err := os.Stat(p)
		if err != nil || info.Size() != file.size {
			t.Errorf("%s: size %v (%v), want %d", p, info, err, file.size)
		}
		m := media(t, p)
		if m.Title != file.title {
			t.Errorf("%s: title %q, want %q", p, m.Title, file.title)
		}
		if file.flags != nil {
			for i, s := range m.Streams {
				wantDefault := (s.Type == "audio" && i == file.flags.AudioDefault) ||
					(s.Type == "subtitle" && file.flags.SubDefault != nil && i == *file.flags.SubDefault)
				if (s.Type == "audio" || s.Type == "subtitle") && s.Default != wantDefault {
					t.Errorf("%s: stream %d default = %v, want %v", p, i, s.Default, wantDefault)
				}
			}
		}
		for idx, lang := range file.langs {
			if m.Streams[idx].Lang != lang {
				t.Errorf("%s: stream %d language %q, want %q", p, idx, m.Streams[idx].Lang, lang)
			}
		}
	}
	for _, op := range f.m.Ops {
		if op.IsDirMove() && present(op.Src) && !holdsFile(want, op.Src) {
			t.Errorf("%s is still there after its move", op.Src)
		}
	}
}

func holdsFile(files map[string]fixtureFile, dir string) bool {
	for p := range files {
		if within(dir, p) {
			return true
		}
	}
	return false
}

func TestFixtureExecDryRunTouchesNothing(t *testing.T) {
	for _, name := range execFixtureNames {
		t.Run(name, func(t *testing.T) {
			f := newFixtureRun(t, name, false)
			mediaBefore, plansBefore := snapshot(t, f.media), snapshot(t, filepath.Dir(f.path))
			locksBefore := snapshot(t, f.cfg.LockDir)

			rep, err := f.exec(t.Context(), ExecOptions{Films: name == "films"})
			if err != nil {
				t.Fatalf("dry run: %v\n%+v", err, rep.Check.Findings)
			}
			if !rep.DryRun || rep.Applied != 0 || rep.AlreadyDone != 0 || len(rep.Results) != len(f.m.Ops) || rep.Bytes == 0 {
				t.Fatalf("report: dry_run %v applied %d already_done %d results %d bytes %d", rep.DryRun, rep.Applied, rep.AlreadyDone, len(rep.Results), rep.Bytes)
			}
			for _, r := range rep.Results {
				if r.Status != StatusPending {
					t.Errorf("op %d is %s in a dry run", r.Op, r.Status)
				}
			}
			if present(JournalPath(f.path)) || f.fake.total() != 0 {
				t.Errorf("a dry run wrote a journal or ran %d children", f.fake.total())
			}
			for what, pair := range map[string][2]map[string]string{
				"media": {mediaBefore, snapshot(t, f.media)}, "plans": {plansBefore, snapshot(t, filepath.Dir(f.path))},
				"locks": {locksBefore, snapshot(t, f.cfg.LockDir)},
			} {
				if diff := firstMapDifference(pair[0], pair[1]); diff != "" {
					t.Errorf("a dry run changed %s: %s", what, diff)
				}
			}
		})
	}
}

func TestFixtureExecAppliesEveryOpAndARerunIsANoOp(t *testing.T) {
	for _, name := range execFixtureNames {
		for _, cross := range []bool{false, true} {
			label := name + "/same device"
			if cross {
				label = name + "/cross device"
			}
			t.Run(label, func(t *testing.T) {
				f := newFixtureRun(t, name, cross)
				want := f.expectedFiles()

				rep, err := f.exec(t.Context(), ExecOptions{Go: true, Films: name == "films"})
				if err != nil {
					t.Fatalf("exec: %v\n%+v", err, rep)
				}
				if rep.Applied != len(f.m.Ops) || len(rep.Results) != len(f.m.Ops) || rep.Error != "" || rep.FailedOp != nil {
					t.Fatalf("applied %d of %d, error %q", rep.Applied, len(f.m.Ops), rep.Error)
				}
				for i, r := range rep.Results {
					switch {
					case r.Status == StatusDone:
					case r.Status == StatusSkipped && f.m.Ops[i].Kind == KindRmdirIfEmpty:
					default:
						t.Errorf("op %d (%s) is %s: %s", r.Op, r.Kind, r.Status, r.Detail)
					}
				}
				f.verifyApplied(want)
				for _, op := range f.m.Ops {
					if op.Kind == KindRmdirIfEmpty && rep.Results[op.N-1].Status == StatusDone && present(op.Path) {
						t.Errorf("op %d reported %s removed but it is there", op.N, op.Path)
					}
				}
				events := journalEvents(t, f.path)
				if events[0] != "start" || events[len(events)-1] != "finish" || len(events) != 2+2*len(f.m.Ops) {
					t.Errorf("journal has %d records, starts %q ends %q", len(events), events[0], events[len(events)-1])
				}

				tree, calls := f.dump(), f.fake.total()
				rep, err = f.exec(t.Context(), ExecOptions{Go: true, Films: name == "films"})
				if err != nil {
					t.Fatalf("rerun: %v", err)
				}
				if rep.AlreadyDone != len(f.m.Ops) || len(rep.Results) != 0 || rep.Applied != 0 || f.fake.total() != calls {
					t.Fatalf("rerun: already_done %d, results %d, applied %d, %d new children", rep.AlreadyDone, len(rep.Results), rep.Applied, f.fake.total()-calls)
				}
				if diff := firstMapDifference(tree, f.dump()); diff != "" {
					t.Errorf("the rerun changed the tree: %s", diff)
				}
			})
		}
	}
}

// halfwayOp is the number of the first op at or past the middle of the
// manifest that calls a child on a path we can recognise, and leaves a later op
// to run: the run is cancelled when that op's child starts, so it stops about
// half way through.
func halfwayOp(f *fixtureRun) (Op, string) {
	last := len(f.m.Ops) - 1
	for _, op := range f.m.Ops[len(f.m.Ops)/2 : last] {
		switch {
		case op.Kind == KindTitle || op.Kind == KindFlags || op.Kind == KindAudioLang:
			return op, op.Path
		case op.Kind == KindRename && op.Title != "":
			return op, op.Dst
		case op.Kind == KindRemux:
			return op, op.Src
		}
	}
	f.t.Fatal("no op past the middle of the manifest runs a child")
	return Op{}, ""
}

func TestFixtureExecInterruptedThenResumedMatchesAnUninterruptedRun(t *testing.T) {
	for _, name := range execFixtureNames {
		for _, cross := range []bool{false, true} {
			label := name + "/same device"
			if cross {
				label = name + "/cross device"
			}
			t.Run(label, func(t *testing.T) {
				ref := newFixtureRun(t, name, cross)
				if _, err := ref.exec(t.Context(), ExecOptions{Go: true}); err != nil {
					t.Fatalf("uninterrupted run: %v", err)
				}
				want := ref.dump()

				f := newFixtureRun(t, name, cross)
				stop, trigger := halfwayOp(f)
				ctx, cancel := context.WithCancel(t.Context())
				f.fake.hook = func(argv []string) {
					if slices.Contains(argv, trigger) {
						cancel()
					}
				}
				rep, err := f.exec(ctx, ExecOptions{Go: true})
				wantCode(t, err, cli.ExitIncomplete)
				if rep.Error == "" || rep.Applied == 0 || rep.Applied >= len(f.m.Ops) {
					t.Fatalf("the run did not stop part way: applied %d of %d, error %q", rep.Applied, len(f.m.Ops), rep.Error)
				}
				if rep.Applied < stop.N-1 {
					t.Fatalf("stopped after %d ops, before op %d", rep.Applied, stop.N)
				}
				if events := journalEvents(t, f.path); countOf(events, "start") != 1 {
					t.Fatalf("journal = %v", events)
				}
				f.fake.hook = nil

				rep, err = f.exec(t.Context(), ExecOptions{Go: true})
				if err != nil {
					t.Fatalf("resumed run: %v\n%+v", err, rep)
				}
				if rep.AlreadyDone == 0 || rep.AlreadyDone+rep.Applied != len(f.m.Ops) {
					t.Fatalf("resumed run: already_done %d + applied %d != %d", rep.AlreadyDone, rep.Applied, len(f.m.Ops))
				}
				events := journalEvents(t, f.path)
				if countOf(events, "start") != 2 || events[len(events)-1] != "finish" {
					t.Errorf("journal = %v", events)
				}
				if diff := firstMapDifference(want, f.dump()); diff != "" {
					t.Errorf("the resumed tree differs from an uninterrupted run: %s", diff)
				}
			})
		}
	}
}
