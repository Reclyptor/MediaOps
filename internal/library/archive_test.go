package library

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/reclyptor/mediaops/internal/cli"
)

func archiveEntries(rels ...string) []ArchiveEntry {
	var out []ArchiveEntry
	for _, r := range rels {
		out = append(out, ArchiveEntry{Path: r})
	}
	return out
}

func wantCode(t *testing.T, err error, code int, contains string) {
	t.Helper()
	if err == nil {
		t.Fatalf("no error, want exit %d containing %q", code, contains)
	}
	if got := cli.Code(err); got != code {
		t.Errorf("exit code %d, want %d (%v)", got, code, err)
	}
	if !strings.Contains(err.Error(), contains) {
		t.Errorf("error %q does not contain %q", err, contains)
	}
}

func TestArchiveDst(t *testing.T) {
	r := newRig(t)
	root := r.archive
	got, err := r.d.ArchiveDst(r.path("box/anime/X/ep 1.mkv"), root)
	if err != nil || got != filepath.Join(root, "anime", "X", "ep 1.mkv") {
		t.Fatalf("got %q, %v", got, err)
	}
	// The share is MEDIA_ROOT's immediate child whether or not it is a mount,
	// so a path on a share the rig has no device for lands the same way.
	r.d.Paths.StatDev = func(string) (uint64, error) { return 1, nil }
	if again, _ := r.d.ArchiveDst(r.path("box/anime/X/ep 1.mkv"), root); again != got {
		t.Fatalf("the destination depends on the mounts: %q vs %q", again, got)
	}
	for _, bad := range []string{r.path("box"), filepath.Join(filepath.Dir(r.media), "other", "a.mkv"), r.media + "2/box/a.mkv"} {
		if _, err := r.d.ArchiveDst(bad, root); err == nil {
			t.Errorf("ArchiveDst(%s) succeeded", bad)
		}
	}
}

func TestPlanArchive(t *testing.T) {
	t.Run("refuses an archive root that is not real storage", func(t *testing.T) {
		r := newRig(t)
		r.put("box/anime/X/a.mkv", "x")
		for name, mutate := range map[string]func(){
			"unset":          func() { r.d.Paths.Archive = "" },
			"missing":        func() { r.d.Paths.Archive = filepath.Join(r.archive, "nowhere") },
			"on the overlay": func() { r.d.Paths.StatDev = func(string) (uint64, error) { return 1, nil } },
		} {
			t.Run(name, func(t *testing.T) {
				c := r.d
				defer func() { r.d = c }()
				mutate()
				_, err := r.d.PlanArchive(archiveEntries("box/anime/X/a.mkv"))
				wantCode(t, err, cli.ExitRefused, "ARCHIVE_ROOT")
			})
		}
	})

	t.Run("a source that is not a file", func(t *testing.T) {
		r := newRig(t)
		r.mkdir("box/anime/X")
		for _, rel := range []string{"box/anime/X", "box/anime/X/nope.mkv"} {
			_, err := r.d.PlanArchive(archiveEntries(rel))
			wantCode(t, err, cli.ExitUsage, "not a file")
		}
		src := r.put("box/anime/X/real.mkv", "x")
		if err := os.Symlink(src, r.path("box/anime/X/link.mkv")); err != nil {
			t.Fatal(err)
		}
		_, err := r.d.PlanArchive(archiveEntries("box/anime/X/link.mkv"))
		wantCode(t, err, cli.ExitUsage, "not a file")
	})

	t.Run("paths that leave MEDIA_ROOT", func(t *testing.T) {
		r := newRig(t)
		r.put("box/anime/a.mkv", "x")
		for _, rel := range []string{"../archive/a.mkv", "/etc/passwd", "box/../../x", ""} {
			_, err := r.d.PlanArchive(archiveEntries(rel))
			wantCode(t, err, cli.ExitUsage, "entry 0")
		}
	})

	t.Run("a file with no share component", func(t *testing.T) {
		r := newRig(t)
		r.put("loose.mkv", "x")
		_, err := r.d.PlanArchive(archiveEntries("loose.mkv"))
		wantCode(t, err, cli.ExitUsage, "no share component")
	})

	t.Run("an occupied destination is refused, and nothing is overwritten", func(t *testing.T) {
		r := newRig(t)
		r.put("box/anime/X/a.mkv", "new")
		held := putFile(t, filepath.Join(r.archive, "anime", "X", "a.mkv"), "old")
		_, err := r.d.Archive(context.Background(), archiveEntries("box/anime/X/a.mkv"), true)
		wantCode(t, err, cli.ExitRefused, "archive destination occupied")
		if r.read(held) != "old" || r.read(r.path("box/anime/X/a.mkv")) != "new" {
			t.Fatal("a refused archive changed a file")
		}
	})

	t.Run("two entries for one destination", func(t *testing.T) {
		r := newRig(t)
		r.put("box/anime/X/a.mkv", "a")
		r.put("box2/anime/X/a.mkv", "b") // another box, the same category and path
		_, err := r.d.PlanArchive(archiveEntries("box/anime/X/a.mkv", "box2/anime/X/a.mkv"))
		wantCode(t, err, cli.ExitUsage, "collision")

		_, err = r.d.PlanArchive(archiveEntries("box/anime/X/a.mkv", "box/anime/X/a.mkv"))
		wantCode(t, err, cli.ExitUsage, "collision")
	})

	t.Run("copy accounting counts only what crosses a filesystem", func(t *testing.T) {
		r := newRig(t)
		r.put("box/anime/X/a.mkv", "12345")
		r.put("box/anime/X/b.mkv", "123")
		rep, err := r.d.PlanArchive(archiveEntries("box/anime/X/a.mkv", "box/anime/X/b.mkv"))
		if err != nil {
			t.Fatal(err)
		}
		if rep.Files != 2 || rep.Bytes != 8 || rep.CopiedBytes != 8 || !rep.Moves[0].Copy {
			t.Fatalf("report = %+v", rep)
		}

		// The archive on the share's own device: a rename, which costs nothing.
		r.d.Paths.StatDev = func(path string) (uint64, error) {
			if within(r.archive, path) || strictlyWithin(r.media, path) {
				return 5, nil
			}
			return 1, nil
		}
		rep, err = r.d.PlanArchive(archiveEntries("box/anime/X/a.mkv", "box/anime/X/b.mkv"))
		if err != nil || rep.CopiedBytes != 0 || rep.Moves[0].Copy {
			t.Fatalf("report = %+v, %v", rep, err)
		}
	})
}

func TestArchiveFreeSpaceRefusal(t *testing.T) {
	setup := func(t *testing.T) *rig {
		r := newRig(t)
		r.put("box/anime/X/a.mkv", strings.Repeat("a", 600))
		r.put("box/anime/X/b.mkv", strings.Repeat("b", 400))
		return r
	}
	entries := archiveEntries("box/anime/X/a.mkv", "box/anime/X/b.mkv")

	t.Run("refuses a batch that does not fit, before anything moves", func(t *testing.T) {
		r := setup(t)
		r.free = 999
		before := r.snapshot()
		for _, run := range []bool{false, true} {
			_, err := r.d.Archive(context.Background(), entries, run)
			wantCode(t, err, cli.ExitRefused, "to copy into")
			if after := r.snapshot(); len(after) != len(before) || !r.exists(r.path("box/anime/X/a.mkv")) {
				t.Fatalf("a refused batch (run=%v) changed the tree", run)
			}
		}
	})

	t.Run("a batch that exactly fits goes", func(t *testing.T) {
		r := setup(t)
		r.free = 1000
		if _, err := r.d.Archive(context.Background(), entries, true); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("a batch that is only renames needs no free space", func(t *testing.T) {
		r := setup(t)
		r.free = 0
		r.d.Paths.StatDev = func(path string) (uint64, error) {
			if within(r.archive, path) || strictlyWithin(r.media, path) {
				return 5, nil
			}
			return 1, nil
		}
		r.d.Mover.Rename = nil
		rep, err := r.d.Archive(context.Background(), entries, true)
		if err != nil || rep.CopiedBytes != 0 || rep.Archived != 2 {
			t.Fatalf("report = %+v, %v", rep, err)
		}
	})

	t.Run("an unreadable free space is a refusal, not a pass", func(t *testing.T) {
		r := setup(t)
		r.d.FreeBytes = func(string) (uint64, error) { return 0, errors.New("statfs: stale file handle") }
		if _, err := r.d.Archive(context.Background(), entries, true); err == nil || !r.exists(r.path("box/anime/X/a.mkv")) {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestArchiveDryRunTouchesNothing(t *testing.T) {
	r := newRig(t)
	r.put("box/anime/X/a.mkv", "aaaa")
	before := r.snapshot()
	rep, err := r.d.Archive(context.Background(), archiveEntries("box/anime/X/a.mkv"), false)
	if err != nil {
		t.Fatal(err)
	}
	if !rep.DryRun || rep.Files != 1 || rep.Archived != 0 || rep.Moves[0].Moved {
		t.Fatalf("report = %+v", rep)
	}
	after := r.snapshot()
	if len(after) != len(before) {
		t.Fatalf("the dry run changed the tree: %v -> %v", before, after)
	}
	for k, v := range before {
		if after[k] != v {
			t.Fatalf("%s changed", k)
		}
	}
	if r.tools.calls() != 0 {
		t.Fatal("the dry run ran a program")
	}
}

func TestArchiveGoCrossDevice(t *testing.T) {
	r := newRig(t)
	a := r.put("box/anime/X/Season 01/a.mkv", episode("a"))
	b := r.put("box/anime/X/Season 01/b.mkv", episode("b"))
	r.put("box/anime/Y/keep.mkv", episode("untouched"))
	before := r.contents()

	rep, err := r.d.Archive(context.Background(), archiveEntries("box/anime/X/Season 01/a.mkv", "box/anime/X/Season 01/b.mkv"), true)
	if err != nil {
		t.Fatal(err)
	}
	if rep.DryRun || rep.Archived != 2 || !rep.Moves[0].Moved || !rep.Moves[1].Copy {
		t.Fatalf("report = %+v", rep)
	}
	for _, src := range []string{a, b} {
		if r.exists(src) {
			t.Errorf("%s is still at its source", src)
		}
	}
	if r.read(filepath.Join(r.archive, "anime", "X", "Season 01", "a.mkv")) != episode("a") {
		t.Error("a.mkv is not in the archive intact")
	}
	// Nothing was lost or made up: the same bytes, in different places.
	if after := r.contents(); !slices.Equal(before, after) {
		t.Fatalf("contents changed:\n%v\n%v", before, after)
	}
	if entries, _ := filepath.Glob(filepath.Join(r.archive, "*", "*", "*", "*.partial")); len(entries) != 0 {
		t.Fatalf("partials left behind: %v", entries)
	}
	// The directories the moves emptied are gone, the sibling's are not, and
	// the share is untouched.
	if r.exists(r.path("box/anime/X")) || !r.exists(r.path("box/anime/Y/keep.mkv")) || !r.exists(r.path("box")) {
		t.Errorf("sweep wrong: removed %v", rep.RemovedDirs)
	}
	if len(rep.RemovedDirs) != 2 {
		t.Errorf("removed %v, want Season 01 and X", rep.RemovedDirs)
	}
}

func TestArchiveGoRename(t *testing.T) {
	r := newRig(t)
	r.d.Paths.StatDev = func(path string) (uint64, error) {
		if within(r.archive, path) || strictlyWithin(r.media, path) {
			return 5, nil
		}
		return 1, nil
	}
	r.d.Mover.Rename = nil
	r.put("box/anime/X/a.mkv", episode("a"))
	rep, err := r.d.Archive(context.Background(), archiveEntries("box/anime/X/a.mkv"), true)
	if err != nil || rep.Archived != 1 || rep.Moves[0].Copy {
		t.Fatalf("report = %+v, %v", rep, err)
	}
	if r.read(filepath.Join(r.archive, "anime", "X", "a.mkv")) != episode("a") {
		t.Fatal("the file is not in the archive")
	}
}

// TestArchiveStopsAtTheFirstFailure: a move that fails leaves the rest of the
// batch alone, reports what already moved, and loses nothing.
func TestArchiveStopsAtTheFirstFailure(t *testing.T) {
	r := newRig(t)
	r.put("box/anime/X/a.mkv", episode("a"))
	r.put("box/anime/X/b.mkv", episode("b"))
	r.put("box/anime/X/c.mkv", episode("c"))
	before := r.contents()
	// The second file's copy fails.
	calls := 0
	r.d.Mover.copy = func(w io.Writer, rd io.Reader, buf []byte) (int64, error) {
		calls++
		if calls == 2 {
			return 0, errors.New("input/output error")
		}
		return io.CopyBuffer(w, rd, buf)
	}
	rep, err := r.d.Archive(context.Background(), archiveEntries("box/anime/X/a.mkv", "box/anime/X/b.mkv", "box/anime/X/c.mkv"), true)
	if err == nil || rep.Error == "" || rep.Archived != 1 || !rep.Moves[0].Moved || rep.Moves[1].Moved {
		t.Fatalf("report = %+v, err = %v", rep, err)
	}
	if cli.Code(err) != cli.ExitInternal {
		t.Errorf("exit code %d, want %d", cli.Code(err), cli.ExitInternal)
	}
	if !r.exists(r.path("box/anime/X/b.mkv")) || !r.exists(r.path("box/anime/X/c.mkv")) {
		t.Fatal("a source was lost")
	}
	if !slices.Equal(before, r.contents()) {
		t.Fatal("contents changed")
	}
	if r.exists(r.path("box/anime/X")) == false {
		t.Fatal("a failed batch swept a directory that still holds files")
	}
}

func TestArchiveVerifiesWhatItMoved(t *testing.T) {
	r := newRig(t)
	r.put("box/anime/X/a.mkv", episode("a"))
	// A mover that reports success and does nothing.
	r.d.Mover.Rename = func(string, string) error { return nil }
	rep, err := r.d.Archive(context.Background(), archiveEntries("box/anime/X/a.mkv"), true)
	if err == nil || !strings.Contains(err.Error(), "destination missing") || rep.Archived != 0 {
		t.Fatalf("report = %+v, err = %v", rep, err)
	}

	r = newRig(t)
	src := r.put("box/anime/X/a.mkv", episode("a"))
	// A mover that leaves the source in place and a short file at the destination.
	r.d.Mover.Rename = func(_, dst string) error { return os.WriteFile(dst, []byte("short"), 0o660) }
	if _, err = r.d.Archive(context.Background(), archiveEntries("box/anime/X/a.mkv"), true); err == nil || !strings.Contains(err.Error(), "size mismatch") {
		t.Fatalf("err = %v", err)
	}
	if !r.exists(src) {
		t.Fatal("source lost")
	}
}
