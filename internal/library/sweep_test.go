package library

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSweep(t *testing.T) {
	t.Run("removes what the move emptied, up to but not including the share", func(t *testing.T) {
		r := newRig(t)
		src := r.put("box/anime/X/Season 01/a.mkv", "x")
		if err := os.Remove(src); err != nil { // the move took the file
			t.Fatal(err)
		}
		removed, warnings := r.d.sweep([]string{src})
		if len(removed) != 3 || len(warnings) != 0 {
			t.Fatalf("removed %v, warnings %v", removed, warnings)
		}
		share := r.path("box")
		if !r.exists(share) {
			t.Fatal("the sweep removed the share")
		}
		if entries, _ := os.ReadDir(share); len(entries) != 0 {
			t.Fatalf("the share still holds %v", entries)
		}
	})

	t.Run("stops at the first directory that is not empty", func(t *testing.T) {
		r := newRig(t)
		src := r.put("box/anime/X/Season 01/a.mkv", "x")
		sibling := r.put("box/anime/X/Season 02/b.mkv", "y")
		_ = os.Remove(src)
		removed, _ := r.d.sweep([]string{src})
		if len(removed) != 1 || removed[0] != r.path("box/anime/X/Season 01") {
			t.Fatalf("removed %v", removed)
		}
		if !r.exists(sibling) {
			t.Fatal("a sibling's file is gone")
		}
	})

	t.Run("leaves a directory holding anything, hidden files included", func(t *testing.T) {
		r := newRig(t)
		src := r.put("box/anime/X/a.mkv", "x")
		keep := r.put("box/anime/X/.hidden", "h")
		_ = os.Remove(src)
		if removed, _ := r.d.sweep([]string{src}); len(removed) != 0 || !r.exists(keep) {
			t.Fatalf("removed %v", removed)
		}
	})

	t.Run("never climbs out of the share or removes it", func(t *testing.T) {
		r := newRig(t)
		// Two sources that empty everything on their share, and a lookalike
		// sibling share whose name extends the first's.
		a := r.put("box/anime/a.mkv", "x")
		other := r.mkdir("boxes/anime")
		_ = os.Remove(a)
		r.d.sweep([]string{a})
		for _, p := range []string{r.path("box"), r.path("boxes"), other, r.media} {
			if !r.exists(p) {
				t.Errorf("%s was removed", p)
			}
		}
	})

	t.Run("never removes the staging tree", func(t *testing.T) {
		r := newRig(t)
		staging := r.d.Paths.Staging()
		src := filepath.Join(staging, "anime", "X", "a.mkv")
		putFile(t, src, "x")
		_ = os.Remove(src)
		removed, _ := r.d.sweep([]string{src})
		if len(removed) != 2 {
			t.Fatalf("removed %v, want the two directories below the staging tree", removed)
		}
		if !r.exists(staging) {
			t.Fatal("the sweep removed the staging tree")
		}
	})

	t.Run("a source outside MEDIA_ROOT sweeps nothing", func(t *testing.T) {
		r := newRig(t)
		outside := filepath.Join(filepath.Dir(r.media), "elsewhere", "a")
		putFile(t, filepath.Join(outside, "a.mkv"), "x")
		_ = os.Remove(filepath.Join(outside, "a.mkv"))
		if removed, _ := r.d.sweep([]string{filepath.Join(outside, "a.mkv")}); len(removed) != 0 || !r.exists(outside) {
			t.Fatalf("removed %v", removed)
		}
	})

	t.Run("a source directly under MEDIA_ROOT is not a share to sweep", func(t *testing.T) {
		r := newRig(t)
		src := r.put("loose.mkv", "x")
		_ = os.Remove(src)
		if removed, _ := r.d.sweep([]string{src}); len(removed) != 0 || !r.exists(r.media) {
			t.Fatalf("removed %v", removed)
		}
	})

	t.Run("with no mount to find, the share is MEDIA_ROOT's immediate child", func(t *testing.T) {
		r := newRig(t)
		// Every path on one device: ShareRoot has nothing to find but "/".
		r.d.Paths.StatDev = func(string) (uint64, error) { return 1, nil }
		src := r.put("box/anime/X/a.mkv", "x")
		_ = os.Remove(src)
		removed, _ := r.d.sweep([]string{src})
		if len(removed) != 2 || !r.exists(r.path("box")) {
			t.Fatalf("removed %v", removed)
		}
	})

	t.Run("a directory that cannot be removed is a warning, not a crash", func(t *testing.T) {
		skipIfRoot(t)
		r := newRig(t)
		src := r.put("box/anime/X/a.mkv", "x")
		_ = os.Remove(src)
		locked := r.path("box/anime")
		if err := os.Chmod(locked, 0o500); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(locked, 0o770) })
		removed, warnings := r.d.sweep([]string{src})
		if len(removed) != 0 || len(warnings) != 1 {
			t.Fatalf("removed %v, warnings %v", removed, warnings)
		}
	})

	t.Run("two sources in one directory", func(t *testing.T) {
		r := newRig(t)
		a, b := r.put("box/anime/X/a.mkv", "x"), r.put("box/anime/X/b.mkv", "y")
		_, _ = os.Remove(a), os.Remove(b)
		removed, warnings := r.d.sweep([]string{a, b})
		if len(removed) != 2 || len(warnings) != 0 {
			t.Fatalf("removed %v, warnings %v", removed, warnings)
		}
	})
}
