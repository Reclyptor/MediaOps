package library

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/reclyptor/mediaops/internal/cli"
	"github.com/reclyptor/mediaops/kit/proc"
)

// pend is a rip folder on the same share as the library, so a promotion there
// is a rename; stage is one on another share, so it copies.
const (
	pend  = "box/_pending/anime/X"
	libEp = "box/anime/X/Season 01/X - S01E0"
)

func move(src, dst, title string) PromoteEntry { return PromoteEntry{Src: src, Dst: dst, Title: title} }
func keep(path string) PromoteEntry            { return PromoteEntry{Keep: path} }

// threeEpisodes is a rip folder of three mkv files and the entries that
// promote them.
func threeEpisodes(t *testing.T, r *rig, from string) []PromoteEntry {
	t.Helper()
	var entries []PromoteEntry
	for _, n := range []string{"1", "2", "3"} {
		r.put(from+"/t0"+n+".mkv", episode("ep"+n))
		entries = append(entries, move(from+"/t0"+n+".mkv", libEp+n+".mkv", "X - S01E0"+n))
	}
	return entries
}

func TestPromoteStep1NoDoubleMapping(t *testing.T) {
	r := newRig(t)
	entries := threeEpisodes(t, r, pend)

	dupSrc := append(slices.Clone(entries), move(pend+"/t01.mkv", libEp+"9.mkv", ""))
	_, err := r.d.PlanPromote(dupSrc)
	wantCode(t, err, cli.ExitUsage, "source mapped more than once")

	dupDst := append(slices.Clone(entries), move(pend+"/t09.mkv", libEp+"1.mkv", ""))
	r.put(pend+"/t09.mkv", episode("nine"))
	_, err = r.d.PlanPromote(dupDst)
	wantCode(t, err, cli.ExitUsage, "destination collision")

	// The same file spelt two ways is still one file.
	respelt := append(slices.Clone(entries), move(pend+"/./t01.mkv", libEp+"9.mkv", ""))
	_, err = r.d.PlanPromote(respelt)
	wantCode(t, err, cli.ExitUsage, "source mapped more than once")

	if err := os.Remove(r.path(pend + "/t09.mkv")); err != nil {
		t.Fatal(err)
	}
	if _, err := r.d.PlanPromote(entries); err != nil {
		t.Fatalf("a clean manifest was refused: %v", err)
	}
}

func TestPromoteSourceValidation(t *testing.T) {
	r := newRig(t)
	r.mkdir(pend + "/dir.mkv")
	real := r.put(pend+"/real.mkv", episode("r"))
	if err := os.Symlink(real, r.path(pend+"/link.mkv")); err != nil {
		t.Fatal(err)
	}
	for _, src := range []string{pend + "/missing.mkv", pend + "/dir.mkv", pend + "/link.mkv"} {
		_, err := r.d.PlanPromote([]PromoteEntry{move(src, libEp+"1.mkv", "")})
		wantCode(t, err, cli.ExitUsage, "source missing")
	}
	for _, e := range []PromoteEntry{
		move("/etc/hostname", libEp+"1.mkv", ""),
		move(pend+"/../../../../x", libEp+"1.mkv", ""),
		move(pend+"/real.mkv", "/tmp/out.mkv", ""),
		move(pend+"/real.mkv", "../escape.mkv", ""),
		keep("/anything"),
	} {
		_, err := r.d.PlanPromote([]PromoteEntry{e})
		wantCode(t, err, cli.ExitUsage, "entry 0")
	}
}

func TestPromoteStep2EveryVideoIsAccountedFor(t *testing.T) {
	setup := func(t *testing.T) (*rig, []PromoteEntry) {
		r := newRig(t)
		return r, threeEpisodes(t, r, pend)
	}

	t.Run("an unmapped video is a hard error naming it", func(t *testing.T) {
		r, entries := setup(t)
		r.put(pend+"/t04.mkv", episode("forgotten"))
		_, err := r.d.Promote(context.Background(), entries, true)
		wantCode(t, err, cli.ExitUsage, "1 source VIDEO files unaccounted for")
		if !strings.Contains(err.Error(), pend+"/t04.mkv") {
			t.Errorf("the error does not name the file: %v", err)
		}
		if !r.exists(r.path(pend + "/t01.mkv")) {
			t.Fatal("a refused promotion moved a file")
		}
	})

	t.Run("a nested video counts", func(t *testing.T) {
		r, entries := setup(t)
		r.put(pend+"/Extras/deep/nced.mkv", episode("nced"))
		_, err := r.d.PlanPromote(entries)
		wantCode(t, err, cli.ExitUsage, "unaccounted for")
	})

	t.Run("every container the library holds, in any case", func(t *testing.T) {
		for _, ext := range videoExts {
			for _, name := range []string{"stray" + ext, "STRAY" + strings.ToUpper(ext)} {
				r, entries := setup(t)
				r.put(pend+"/"+name, "x")
				_, err := r.d.PlanPromote(entries)
				if cli.Code(err) != cli.ExitUsage {
					t.Errorf("%s was not accounted for: %v", name, err)
				}
			}
		}
	})

	t.Run("non-video is not demanded", func(t *testing.T) {
		r, entries := setup(t)
		for _, name := range []string{"Extras/OST/01.flac", "cover.jpg", "booklet.pdf", "t01.mkv.partial", "notes.nfo"} {
			r.put(pend+"/"+name, "x")
		}
		if _, err := r.d.PlanPromote(entries); err != nil {
			t.Fatalf("non-video files were demanded: %v", err)
		}
	})

	t.Run("a keep accounts for a video", func(t *testing.T) {
		r, entries := setup(t)
		r.put(pend+"/Extras/bundle/track.mkv", episode("bundle"))
		_, err := r.d.PlanPromote(entries)
		wantCode(t, err, cli.ExitUsage, "unaccounted for")

		rep, err := r.d.PlanPromote(append(entries, keep(pend+"/Extras/bundle/track.mkv")))
		if err != nil || len(rep.Kept) != 1 || rep.Kept[0] != pend+"/Extras/bundle/track.mkv" {
			t.Fatalf("kept = %v, err = %v", rep.Kept, err)
		}
	})

	t.Run("only the source directories are walked", func(t *testing.T) {
		r, entries := setup(t)
		r.put("box/_pending/anime/Other/t01.mkv", episode("not ours"))
		if _, err := r.d.PlanPromote(entries); err != nil {
			t.Fatalf("a video in another directory was demanded: %v", err)
		}
	})

	t.Run("the error is capped but counts every file", func(t *testing.T) {
		r, entries := setup(t)
		for i := range 25 {
			r.put(fmt.Sprintf("%s/stray%02d.mkv", pend, i), "x")
		}
		_, err := r.d.PlanPromote(entries)
		wantCode(t, err, cli.ExitUsage, "25 source VIDEO files unaccounted for")
		if got := strings.Count(err.Error(), "stray"); got != maxUnaccounted {
			t.Errorf("the error lists %d files, want %d", got, maxUnaccounted)
		}
	})

	t.Run("an unreadable source directory fails closed", func(t *testing.T) {
		skipIfRoot(t)
		r, entries := setup(t)
		r.put(pend+"/Extras/a.mkv", "x")
		locked := r.path(pend + "/Extras")
		if err := os.Chmod(locked, 0); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(locked, 0o770) })
		if _, err := r.d.PlanPromote(entries); err == nil {
			t.Fatal("a directory that could not be read passed the completeness check")
		}
	})
}

func TestPromoteStep3DestinationFree(t *testing.T) {
	r := newRig(t)
	entries := threeEpisodes(t, r, pend)
	r.put(libEp+"2.mkv", "already here")
	_, err := r.d.Promote(context.Background(), entries, true)
	wantCode(t, err, cli.ExitRefused, "destination already occupied")
	if r.read(r.path(libEp+"2.mkv")) != "already here" || !r.exists(r.path(pend+"/t01.mkv")) {
		t.Fatal("a refused promotion changed the tree")
	}

	r = newRig(t)
	entries = threeEpisodes(t, r, pend)
	if err := os.MkdirAll(filepath.Dir(r.path(libEp+"1.mkv")), 0o770); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/nowhere", r.path(libEp+"1.mkv")); err != nil {
		t.Fatal(err)
	}
	_, err = r.d.PlanPromote(entries)
	wantCode(t, err, cli.ExitRefused, "destination already occupied")
}

func TestPromoteStep4DestinationsAreCreatableBeforeAnythingMoves(t *testing.T) {
	t.Run("a name the filesystem refuses", func(t *testing.T) {
		r := newRig(t)
		entries := threeEpisodes(t, r, pend)
		entries[2].Dst = "box/anime/X/Season 01/" + strings.Repeat("x", 300) + ".mkv"
		before := r.contents()
		_, err := r.d.Promote(context.Background(), entries, true)
		wantCode(t, err, cli.ExitRefused, "destination not creatable")
		if !slices.Equal(before, r.contents()) || !r.exists(r.path(pend+"/t01.mkv")) || r.exists(r.path(libEp+"1.mkv")) {
			t.Fatal("a file moved before the destinations were proven")
		}
	})

	t.Run("a directory that cannot be written", func(t *testing.T) {
		skipIfRoot(t)
		r := newRig(t)
		entries := threeEpisodes(t, r, pend)
		locked := r.mkdir("box/anime/X/Season 01")
		if err := os.Chmod(locked, 0o500); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(locked, 0o770) })
		_, err := r.d.Promote(context.Background(), entries, true)
		wantCode(t, err, cli.ExitRefused, "destination not creatable")
		if !r.exists(r.path(pend + "/t01.mkv")) {
			t.Fatal("a file moved")
		}
	})

	t.Run("the probe files are gone afterwards, the directories stay", func(t *testing.T) {
		r := newRig(t)
		entries := threeEpisodes(t, r, pend)
		if err := r.d.touchValidate([]PromoteMove{{dst: r.path(libEp + "1.mkv")}, {dst: r.path(libEp + "2.mkv")}}); err != nil {
			t.Fatal(err)
		}
		if r.exists(r.path(libEp+"1.mkv")) || r.exists(r.path(libEp+"2.mkv")) {
			t.Fatal("a probe file survived")
		}
		if !r.exists(filepath.Dir(r.path(libEp + "1.mkv"))) {
			t.Fatal("the directory was not kept")
		}
		_ = entries
	})

	t.Run("a dry run does not create directories", func(t *testing.T) {
		r := newRig(t)
		entries := threeEpisodes(t, r, pend)
		if _, err := r.d.Promote(context.Background(), entries, false); err != nil {
			t.Fatal(err)
		}
		if r.exists(r.path("box/anime")) {
			t.Fatal("a dry run created the destination directories")
		}
	})
}

func TestPromoteGoSameShareRenames(t *testing.T) {
	r := newRig(t)
	entries := threeEpisodes(t, r, pend)
	r.put(pend+"/cover.jpg", "art")
	before := r.contents()

	rep, err := r.d.Promote(context.Background(), entries, true)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Moved != 3 || rep.DecodeChecked != 3 || rep.Titled != 3 || rep.CopiedBytes != 0 || rep.Error != "" {
		t.Fatalf("report = %+v", rep)
	}
	for _, n := range []string{"1", "2", "3"} {
		dst := r.path(libEp + n + ".mkv")
		if r.read(dst) != episode("ep"+n) || r.exists(r.path(pend+"/t0"+n+".mkv")) {
			t.Errorf("episode %s did not move whole", n)
		}
		if r.tools.titles[dst] != "X - S01E0"+n {
			t.Errorf("episode %s title = %q", n, r.tools.titles[dst])
		}
	}
	if !slices.Equal(before, r.contents()) {
		t.Fatal("the library lost or invented bytes")
	}
	// Step 7: the rip folder still holds the cover, so it is not swept.
	if !r.exists(r.path(pend + "/cover.jpg")) {
		t.Fatal("the sweep removed a directory holding a non-video file")
	}
}

func TestPromoteGoAcrossShares(t *testing.T) {
	r := newRig(t)
	staging := filepath.Join(r.d.Paths.Staging(), "anime", "X")
	rel, _ := filepath.Rel(r.media, staging)
	entries := threeEpisodes(t, r, rel)
	before := r.contents()

	rep, err := r.d.Promote(context.Background(), entries, true)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Moved != 3 || rep.CopiedBytes == 0 {
		t.Fatalf("report = %+v", rep)
	}
	if !slices.Equal(before, r.contents()) {
		t.Fatal("the library lost or invented bytes")
	}
	// The rip folder and its category are swept; the staging tree is not.
	if r.exists(staging) || !r.exists(r.d.Paths.Staging()) {
		t.Fatalf("sweep wrong: removed %v", rep.RemovedDirs)
	}
}

func TestPromoteCrossShareFreeSpaceRefusal(t *testing.T) {
	r := newRig(t)
	staging, _ := filepath.Rel(r.media, filepath.Join(r.d.Paths.Staging(), "anime", "X"))
	entries := threeEpisodes(t, r, staging)
	r.free = 10
	before := r.contents()
	_, err := r.d.Promote(context.Background(), entries, true)
	wantCode(t, err, cli.ExitRefused, "to copy into")
	if !slices.Equal(before, r.contents()) || r.exists(r.path(libEp+"1.mkv")) {
		t.Fatal("a refused promotion changed the tree")
	}

	// The same batch within one share is a rename and needs no space.
	r = newRig(t)
	r.free = 0
	entries = threeEpisodes(t, r, pend)
	if _, err := r.d.Promote(context.Background(), entries, true); err != nil {
		t.Fatalf("a rename-only batch was refused for space: %v", err)
	}
}

func TestPromoteStep5VerifiesWhatMoved(t *testing.T) {
	t.Run("a destination that is missing", func(t *testing.T) {
		r := newRig(t)
		entries := threeEpisodes(t, r, pend)
		r.d.Mover.Rename = func(string, string) error { return nil }
		rep, err := r.d.Promote(context.Background(), entries, true)
		if err == nil || !strings.Contains(err.Error(), "destination missing") || rep.Moved != 0 || rep.Error == "" {
			t.Fatalf("report = %+v, err = %v", rep, err)
		}
	})

	t.Run("a destination of the wrong size", func(t *testing.T) {
		r := newRig(t)
		entries := threeEpisodes(t, r, pend)
		r.d.Mover.Rename = func(_, dst string) error { return os.WriteFile(dst, []byte("1421.5 short"), 0o660) }
		_, err := r.d.Promote(context.Background(), entries, true)
		if err == nil || !strings.Contains(err.Error(), "size mismatch") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("a source that is still there", func(t *testing.T) {
		r := newRig(t)
		entries := threeEpisodes(t, r, pend)
		r.d.Mover.Rename = func(src, dst string) error {
			data, _ := os.ReadFile(src)
			return os.WriteFile(dst, data, 0o660) // a copy, no unlink
		}
		_, err := r.d.Promote(context.Background(), entries, true)
		if err == nil || !strings.Contains(err.Error(), "source still present") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("a failure part-way reports what moved and stops", func(t *testing.T) {
		r := newRig(t)
		entries := threeEpisodes(t, r, pend)
		r.put(libEp+"3.mkv", "late arrival") // appears after the plan is checked
		real := r.d.Mover.Rename
		n := 0
		r.d.Mover.Rename = func(a, b string) error {
			n++
			if n == 3 {
				return errors.New("input/output error")
			}
			return real(a, b)
		}
		// Step 3 would catch the occupied destination, so take the file away
		// again and let the move itself fail.
		_ = os.Remove(r.path(libEp + "3.mkv"))
		rep, err := r.d.Promote(context.Background(), entries, true)
		if err == nil || rep.Moved != 2 || !rep.Moves[1].Moved || rep.Moves[2].Moved || rep.Error == "" {
			t.Fatalf("report = %+v, err = %v", rep, err)
		}
		if cli.Code(err) != cli.ExitInternal {
			t.Errorf("exit code %d", cli.Code(err))
		}
		if !r.exists(r.path(pend + "/t03.mkv")) {
			t.Fatal("the source of the failed move is gone")
		}
		if r.tools.calls() != 0 {
			t.Fatal("a half-promoted batch was decode-checked or titled")
		}
	})
}

func TestPromoteStep6DecodeCheckFailsClosed(t *testing.T) {
	cases := map[string]string{
		"a failed probe":         "BROKEN",
		"a zero duration":        "ZERO",
		"a not-a-number":         "NAN",
		"no duration at all":     "NODUR",
		"an unparseable content": "garbage",
	}
	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			r := newRig(t)
			entries := threeEpisodes(t, r, pend)
			r.put(pend+"/t02.mkv", content)
			rep, err := r.d.Promote(context.Background(), entries, true)
			wantCode(t, err, cli.ExitIncomplete, "1 of 3 files failed the decode check")
			if len(rep.DecodeFailures) != 1 || rep.DecodeFailures[0].Path != libEp+"2.mkv" || rep.DecodeFailures[0].Reason == "" {
				t.Fatalf("failures = %+v", rep.DecodeFailures)
			}
			// The files have moved, and the failure says so rather than hiding it.
			if rep.Moved != 3 || rep.Error == "" || !r.exists(r.path(libEp+"2.mkv")) {
				t.Fatalf("report = %+v", rep)
			}
			if len(r.tools.edited()) != 0 {
				t.Fatal("a title was set on a batch that failed its decode check")
			}
			if !r.exists(r.path(pend)) && len(rep.RemovedDirs) > 0 {
				t.Fatal("a failed promotion swept its source directories")
			}
		})
	}

	t.Run("a probe that cannot run is a failure, never a pass", func(t *testing.T) {
		r := newRig(t)
		entries := threeEpisodes(t, r, pend)
		r.tools.probeErr = errors.New("ffprobe: executable file not found")
		rep, err := r.d.Promote(context.Background(), entries, true)
		wantCode(t, err, cli.ExitIncomplete, "3 of 3 files failed the decode check")
		if len(rep.DecodeFailures) != 3 {
			t.Fatalf("failures = %+v", rep.DecodeFailures)
		}
	})

	t.Run("the failures are named in manifest order", func(t *testing.T) {
		r := newRig(t)
		entries := threeEpisodes(t, r, pend)
		r.put(pend+"/t01.mkv", "BROKEN")
		r.put(pend+"/t03.mkv", "BROKEN")
		rep, _ := r.d.Promote(context.Background(), entries, true)
		got := []string{rep.DecodeFailures[0].Path, rep.DecodeFailures[1].Path}
		if !slices.Equal(got, []string{libEp + "1.mkv", libEp + "3.mkv"}) {
			t.Fatalf("failures = %v", got)
		}
	})

	t.Run("only mkv files are probed, in any case", func(t *testing.T) {
		r := newRig(t)
		entries := []PromoteEntry{
			move(pend+"/a.mkv", libEp+"1.mkv", ""),
			move(pend+"/b.MKV", libEp+"2.MKV", ""),
			move(pend+"/c.avi", libEp+"3.avi", ""),
			move(pend+"/d.ogm", libEp+"4.ogm", ""),
		}
		for _, e := range entries {
			r.put(e.Src, "BROKEN") // would fail if it were probed
		}
		r.put(pend+"/a.mkv", episode("a"))
		r.put(pend+"/b.MKV", episode("b"))
		rep, err := r.d.Promote(context.Background(), entries, true)
		if err != nil || rep.DecodeChecked != 2 || len(r.tools.probed()) != 2 {
			t.Fatalf("report = %+v, err = %v, probed %v", rep, err, r.tools.probed())
		}
	})
}

func TestPromoteTitles(t *testing.T) {
	t.Run("set after the decode check, with a read-back", func(t *testing.T) {
		r := newRig(t)
		entries := threeEpisodes(t, r, pend)
		rep, err := r.d.Promote(context.Background(), entries, true)
		if err != nil || rep.Titled != 3 {
			t.Fatalf("report = %+v, err = %v", rep, err)
		}
		var sawEdit bool
		for _, a := range r.tools.argv {
			switch a[0] {
			case "mkvpropedit":
				sawEdit = true
			case "ffprobe":
				if sawEdit {
					t.Fatal("a file was decode-checked after titling began")
				}
			}
		}
	})

	t.Run("an entry without a title is left alone, and so is a non-mkv", func(t *testing.T) {
		r := newRig(t)
		r.put(pend+"/a.mkv", episode("a"))
		r.put(pend+"/b.avi", episode("b"))
		r.put(pend+"/c.mkv", episode("c"))
		entries := []PromoteEntry{
			move(pend+"/a.mkv", libEp+"1.mkv", ""),
			move(pend+"/b.avi", libEp+"2.avi", "X - S01E02"),
			move(pend+"/c.mkv", libEp+"3.mkv", "X - S01E03"),
		}
		rep, err := r.d.Promote(context.Background(), entries, true)
		if err != nil || rep.Titled != 1 || !slices.Equal(r.tools.edited(), []string{r.path(libEp + "3.mkv")}) {
			t.Fatalf("report = %+v, err = %v, edited %v", rep, err, r.tools.edited())
		}
	})

	t.Run("mkvpropedit warnings are accepted, failures are not", func(t *testing.T) {
		r := newRig(t)
		entries := threeEpisodes(t, r, pend)
		r.tools.propeditExit = 1
		if rep, err := r.d.Promote(context.Background(), entries, true); err != nil || rep.Titled != 3 {
			t.Fatalf("exit 1 was refused: %+v, %v", rep, err)
		}

		r = newRig(t)
		entries = threeEpisodes(t, r, pend)
		r.tools.propeditExit = 2
		rep, err := r.d.Promote(context.Background(), entries, true)
		if err == nil || !strings.Contains(err.Error(), "mkvpropedit failed") || rep.Titled != 0 || rep.Error == "" {
			t.Fatalf("report = %+v, err = %v", rep, err)
		}
	})

	t.Run("a title that does not read back is an error", func(t *testing.T) {
		r := newRig(t)
		entries := threeEpisodes(t, r, pend)
		r.tools.readBack = func(string) string { return "something else" }
		_, err := r.d.Promote(context.Background(), entries, true)
		if err == nil || !strings.Contains(err.Error(), "reads back") {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestPromoteStep7SweepStaysInsideTheShare(t *testing.T) {
	r := newRig(t)
	entries := threeEpisodes(t, r, pend)
	// A directory that is empty already, beside the source, must not be swept
	// by accident: only the path a source lived on is walked.
	idle := r.mkdir("box/_pending/anime/Idle")
	rep, err := r.d.Promote(context.Background(), entries, true)
	if err != nil {
		t.Fatal(err)
	}
	if r.exists(r.path(pend)) || !r.exists(idle) {
		t.Fatalf("removed %v", rep.RemovedDirs)
	}
	if !r.exists(r.path("box")) || !r.exists(r.path("box/_pending/anime")) {
		t.Fatalf("the sweep removed a directory that is not empty or is the share: %v", rep.RemovedDirs)
	}
}

func TestPromoteKeep(t *testing.T) {
	r := newRig(t)
	entries := threeEpisodes(t, r, pend)
	bundle := r.put(pend+"/Extras/bundle/track.mkv", episode("bundle"))
	entries = append(entries, keep(pend+"/Extras/bundle/track.mkv"))

	rep, err := r.d.Promote(context.Background(), entries, true)
	if err != nil || rep.Moved != 3 || len(rep.Kept) != 1 {
		t.Fatalf("report = %+v, err = %v", rep, err)
	}
	if r.read(bundle) != episode("bundle") {
		t.Fatal("a kept file was moved or changed")
	}
	if !r.exists(r.path(pend + "/Extras/bundle")) {
		t.Fatal("the sweep removed the directory of a kept file")
	}
	for _, p := range r.tools.probed() {
		if p == bundle {
			t.Fatal("a kept file was decode-checked as if promoted")
		}
	}

	t.Run("a path promoted and kept is contradictory", func(t *testing.T) {
		r := newRig(t)
		entries := threeEpisodes(t, r, pend)
		_, err := r.d.PlanPromote(append(entries, keep(pend+"/t01.mkv")))
		wantCode(t, err, cli.ExitUsage, "both promoted and kept")
	})

	t.Run("a keep alone is an empty promotion", func(t *testing.T) {
		r := newRig(t)
		rep, err := r.d.Promote(context.Background(), []PromoteEntry{keep(pend + "/a.mkv")}, true)
		if err != nil || rep.Moved != 0 || len(rep.Kept) != 1 {
			t.Fatalf("report = %+v, err = %v", rep, err)
		}
	})
}

func TestPromoteDryRunTouchesNothing(t *testing.T) {
	r := newRig(t)
	entries := threeEpisodes(t, r, pend)
	before := r.snapshot()

	rep, err := r.d.Promote(context.Background(), entries, false)
	if err != nil {
		t.Fatal(err)
	}
	if !rep.DryRun || rep.Files != 3 || rep.Moved != 0 || rep.Moves[0].Moved || rep.Moves[0].Title != "X - S01E01" {
		t.Fatalf("report = %+v", rep)
	}
	after := r.snapshot()
	if len(after) != len(before) {
		t.Fatal("the dry run changed the tree")
	}
	for k, v := range before {
		if after[k] != v {
			t.Fatalf("%s changed", k)
		}
	}
	if r.tools.calls() != 0 {
		t.Fatal("the dry run ran a program")
	}

	// A dry run is only worth trusting if it refuses what the real run would.
	r.put(pend+"/stray.mkv", "x")
	if _, err := r.d.Promote(context.Background(), entries, false); cli.Code(err) != cli.ExitUsage {
		t.Fatalf("the dry run passed a manifest the real run would refuse: %v", err)
	}
}

// TestPromoteNeverLosesAFile runs the whole pipeline over a tree with awkward
// names and every container, and proves no byte went missing or was invented:
// the multiset of file contents is the same afterwards, wherever they landed.
func TestPromoteNeverLosesAFile(t *testing.T) {
	for _, across := range []bool{false, true} {
		t.Run(fmt.Sprintf("across shares %v", across), func(t *testing.T) {
			r := newRig(t)
			from := pend
			if across {
				from, _ = filepath.Rel(r.media, filepath.Join(r.d.Paths.Staging(), "anime", "X"))
			}
			var entries []PromoteEntry
			for i, ext := range videoExts {
				name := fmt.Sprintf("Ｔｉｔｌｅ, %d – 'q' #%s", i, ext)
				r.put(from+"/"+name+ext, episode(name))
				entries = append(entries, move(from+"/"+name+ext, "box/anime/X/Extras/"+name+ext, ""))
			}
			r.put(from+"/OST/01.flac", "flac")
			before := r.contents()
			if _, err := r.d.Promote(context.Background(), entries, true); err != nil {
				t.Fatal(err)
			}
			if after := r.contents(); !slices.Equal(before, after) {
				t.Fatalf("contents changed:\n%v\n%v", before, after)
			}
		})
	}
}

// TestPromoteArgvThroughTheGuard: the fake runs proc.Check on every argv, so
// reaching here means every program the promotion starts is allowlisted and no
// re-encoding option is in sight.
func TestPromoteArgvThroughTheGuard(t *testing.T) {
	r := newRig(t)
	entries := threeEpisodes(t, r, pend)
	if _, err := r.d.Promote(context.Background(), entries, true); err != nil {
		t.Fatal(err)
	}
	for _, a := range r.tools.argv {
		if err := proc.Check(a); err != nil {
			t.Errorf("%v: %v", a, err)
		}
		switch a[0] {
		case "ffprobe", "mkvpropedit", "mkvmerge":
		default:
			t.Errorf("an unexpected program ran: %v", a)
		}
	}
}

func TestPromoteStoppedDuringTheDecodeCheck(t *testing.T) {
	r := newRig(t)
	entries := threeEpisodes(t, r, pend)
	ctx, cancel := context.WithCancel(context.Background())
	r.tools.onProbe = cancel
	rep, err := r.d.Promote(ctx, entries, true)
	if !errors.Is(err, context.Canceled) || rep.Moved != 3 || cli.Code(err) != cli.ExitInternal {
		t.Fatalf("report = %+v, err = %v", rep, err)
	}
	if len(r.tools.edited()) != 0 {
		t.Fatal("a stopped promotion went on to set titles")
	}
}
