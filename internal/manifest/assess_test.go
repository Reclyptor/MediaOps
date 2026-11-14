package manifest

import (
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func asFile(t *testing.T, path string, size int) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, make([]byte, size), 0o644); err != nil {
		t.Fatal(err)
	}
}

func asDir(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
}

func asLink(t *testing.T, target, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
}

func asSize(n int64) *int64 { return &n }

type asEntry struct {
	mode  fs.FileMode
	size  int64
	mtime time.Time
}

func asSnapshot(t *testing.T, root string) map[string]asEntry {
	t.Helper()
	snap := map[string]asEntry{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		snap[rel] = asEntry{info.Mode(), info.Size(), info.ModTime()}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return snap
}

type asCase struct {
	name   string
	setup  func(t *testing.T, src, dst string)
	st     BeginState
	want   Outcome
	reason string
}

func asRun(t *testing.T, kinds []string, cases []asCase) {
	t.Helper()
	for _, kind := range kinds {
		for _, c := range cases {
			t.Run(kind+"/"+c.name, func(t *testing.T) {
				root := t.TempDir()
				op := Op{Kind: kind, Src: filepath.Join(root, "s", "item"), Dst: filepath.Join(root, "d", "item")}
				asDir(t, filepath.Join(root, "s"))
				asDir(t, filepath.Join(root, "d"))
				c.setup(t, op.Src, op.Dst)
				got := Assess(op, c.st)
				if got.Outcome != c.want {
					t.Fatalf("outcome = %v (%s), want %v", got.Outcome, got.Reason, c.want)
				}
				if got.Reason == "" {
					t.Error("empty reason")
				}
				if c.reason != "" && !strings.Contains(got.Reason, c.reason) {
					t.Errorf("reason %q does not contain %q", got.Reason, c.reason)
				}
				if got.Outcome == Conflict && !strings.Contains(got.Reason, root) {
					t.Errorf("conflict reason %q names no absolute path", got.Reason)
				}
			})
		}
	}
}

func TestAssessFileMove(t *testing.T) {
	asRun(t, []string{KindRename, KindArchiveFile}, []asCase{
		{"src gone dst whole", func(t *testing.T, s, d string) { asFile(t, d, 10) }, BeginState{}, Settled, ""},
		{"src gone dst matches recorded size", func(t *testing.T, s, d string) { asFile(t, d, 10) }, BeginState{Size: asSize(10)}, Settled, ""},
		{"src gone dst size differs", func(t *testing.T, s, d string) { asFile(t, d, 4) }, BeginState{Size: asSize(10)}, Conflict, "not whole"},
		{"src gone dst empty but recorded size nonzero", func(t *testing.T, s, d string) { asFile(t, d, 0) }, BeginState{Size: asSize(10)}, Conflict, "not whole"},
		{"src gone dst is a dir", func(t *testing.T, s, d string) { asDir(t, d) }, BeginState{}, Conflict, "not a regular file"},
		{"src gone dst is a symlink", func(t *testing.T, s, d string) { asLink(t, s, d) }, BeginState{}, Conflict, "not a regular file"},
		{"src whole dst absent", func(t *testing.T, s, d string) { asFile(t, s, 10) }, BeginState{Size: asSize(10)}, Redo, ""},
		{"both present", func(t *testing.T, s, d string) { asFile(t, s, 10); asFile(t, d, 10) }, BeginState{}, Conflict, "both exist"},
		{"neither exists", func(t *testing.T, s, d string) {}, BeginState{}, Conflict, "neither"},
		{"src is a symlink to a file", func(t *testing.T, s, d string) {
			asFile(t, filepath.Join(filepath.Dir(s), "real"), 10)
			asLink(t, "real", s)
		}, BeginState{}, Conflict, "not a regular file"},
		{"src is a dangling symlink", func(t *testing.T, s, d string) { asLink(t, "nowhere", s) }, BeginState{}, Conflict, "not a regular file"},
		{"src is a dir", func(t *testing.T, s, d string) { asDir(t, s) }, BeginState{}, Conflict, "not a regular file"},
	})
}

func TestAssessFileMoveConflictNamesBothPaths(t *testing.T) {
	root := t.TempDir()
	op := Op{Kind: KindRename, Src: filepath.Join(root, "a.mkv"), Dst: filepath.Join(root, "b.mkv")}
	asFile(t, op.Src, 1)
	asFile(t, op.Dst, 1)
	got := Assess(op, BeginState{})
	if got.Outcome != Conflict || !strings.Contains(got.Reason, op.Src) || !strings.Contains(got.Reason, op.Dst) {
		t.Fatalf("got %v %q", got.Outcome, got.Reason)
	}
}

func asTreeOf(files map[string]int) func(t *testing.T, dir string) {
	return func(t *testing.T, dir string) {
		asDir(t, dir)
		for rel, n := range files {
			asFile(t, filepath.Join(dir, rel), n)
		}
	}
}

func TestAssessDirMove(t *testing.T) {
	recorded := map[string]int64{"a.mkv": 5, "sub/b.mkv": 7, "sub/deep/c.srt": 3}
	full := map[string]int{"a.mkv": 5, "sub/b.mkv": 7, "sub/deep/c.srt": 3}
	dstWith := func(mut func(m map[string]int)) func(t *testing.T, s, d string) {
		return func(t *testing.T, s, d string) {
			m := map[string]int{}
			for k, v := range full {
				m[k] = v
			}
			mut(m)
			asTreeOf(m)(t, d)
		}
	}
	asRun(t, []string{KindRenameDir, KindMoveDir, KindArchiveDir}, []asCase{
		{"settled with nested subdirs", dstWith(func(map[string]int) {}), BeginState{Tree: recorded}, Settled, ""},
		{"settled nil tree and empty dst", func(t *testing.T, s, d string) { asDir(t, d) }, BeginState{}, Settled, ""},
		{"settled empty tree and empty dst", func(t *testing.T, s, d string) { asDir(t, d) }, BeginState{Tree: map[string]int64{}}, Settled, ""},
		{"settled ignores empty dirs in dst", func(t *testing.T, s, d string) {
			dstWith(func(map[string]int) {})(t, s, d)
			asDir(t, filepath.Join(d, "extra", "empty"))
		}, BeginState{Tree: recorded}, Settled, ""},
		{"nil tree but dst has a file", func(t *testing.T, s, d string) { asTreeOf(map[string]int{"x": 1})(t, d) }, BeginState{}, Conflict, "x was not recorded"},
		{"dst differs by one size", dstWith(func(m map[string]int) { m["sub/b.mkv"] = 8 }), BeginState{Tree: recorded}, Conflict, "sub/b.mkv is 8 bytes, recorded 7"},
		{"dst has one extra file", dstWith(func(m map[string]int) { m["z.nfo"] = 1 }), BeginState{Tree: recorded}, Conflict, "z.nfo was not recorded"},
		{"dst misses one file", dstWith(func(m map[string]int) { delete(m, "sub/deep/c.srt") }), BeginState{Tree: recorded}, Conflict, "sub/deep/c.srt is missing"},
		{"src gone dst missing", func(t *testing.T, s, d string) {}, BeginState{Tree: recorded}, Conflict, "neither"},
		{"src gone dst is a file", func(t *testing.T, s, d string) { asFile(t, d, 1) }, BeginState{}, Conflict, "not a directory"},
		{"src dir dst absent", asDstAbsent(full), BeginState{Tree: recorded}, Redo, ""},
		{"src empty dir dst absent", func(t *testing.T, s, d string) { asDir(t, s) }, BeginState{}, Redo, ""},
		{"resume disjoint files", func(t *testing.T, s, d string) {
			asTreeOf(map[string]int{"a.mkv": 5})(t, d)
			asTreeOf(map[string]int{"sub/b.mkv": 7, "sub/deep/c.srt": 3})(t, s)
		}, BeginState{Tree: recorded}, Resume, ""},
		{"resume src only empty dirs", func(t *testing.T, s, d string) {
			asDir(t, filepath.Join(s, "sub", "deep"))
			asTreeOf(full)(t, d)
		}, BeginState{Tree: recorded}, Resume, ""},
		{"resume nothing moved yet", func(t *testing.T, s, d string) {
			asTreeOf(full)(t, s)
			asDir(t, d)
		}, BeginState{Tree: recorded}, Resume, ""},
		{"relpath collision", func(t *testing.T, s, d string) {
			asTreeOf(map[string]int{"sub/b.mkv": 7, "q": 1})(t, s)
			asTreeOf(map[string]int{"sub/b.mkv": 7})(t, d)
		}, BeginState{Tree: recorded}, Conflict, filepath.Join("sub", "b.mkv") + " exists at both"},
		{"relpath collision of different sizes", func(t *testing.T, s, d string) {
			asTreeOf(map[string]int{"a.mkv": 1})(t, s)
			asTreeOf(map[string]int{"a.mkv": 2})(t, d)
		}, BeginState{}, Conflict, "exists at both"},
		{"src is a file", func(t *testing.T, s, d string) { asFile(t, s, 1); asDir(t, d) }, BeginState{}, Conflict, "not a directory"},
		{"src is a symlink to a dir", func(t *testing.T, s, d string) {
			asTreeOf(full)(t, filepath.Join(filepath.Dir(s), "real"))
			asLink(t, "real", s)
		}, BeginState{Tree: recorded}, Conflict, "not a directory"},
		{"dst is a file with src present", func(t *testing.T, s, d string) { asDir(t, s); asFile(t, d, 1) }, BeginState{}, Conflict, "not a directory"},
		{"dst is a symlink to a dir", func(t *testing.T, s, d string) {
			asDir(t, s)
			asDir(t, filepath.Join(filepath.Dir(d), "real"))
			asLink(t, "real", d)
		}, BeginState{}, Conflict, "not a directory"},
	})
}

func asDstAbsent(files map[string]int) func(t *testing.T, s, d string) {
	return func(t *testing.T, s, d string) { asTreeOf(files)(t, s) }
}

func TestAssessDirMoveTreeReadError(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads everything")
	}
	root := t.TempDir()
	op := Op{Kind: KindMoveDir, Src: filepath.Join(root, "s"), Dst: filepath.Join(root, "d")}
	asFile(t, filepath.Join(op.Dst, "locked", "f"), 1)
	if err := os.Chmod(filepath.Join(op.Dst, "locked"), 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(filepath.Join(op.Dst, "locked"), 0o755) })
	got := Assess(op, BeginState{})
	if got.Outcome != Conflict || !strings.Contains(got.Reason, "locked") {
		t.Fatalf("got %v %q", got.Outcome, got.Reason)
	}
}

func TestAssessRemux(t *testing.T) {
	cases := []struct {
		src, dst, arc bool
		want          Outcome
	}{
		{false, false, false, Conflict},
		{false, false, true, Conflict},
		{false, true, false, Conflict},
		{false, true, true, Settled},
		{true, false, false, Redo},
		{true, false, true, Conflict},
		{true, true, false, Resume},
		{true, true, true, Conflict},
	}
	for _, c := range cases {
		name := map[bool]string{true: "present", false: "absent"}
		t.Run("src "+name[c.src]+" dst "+name[c.dst]+" archive "+name[c.arc], func(t *testing.T) {
			root := t.TempDir()
			op := Op{Kind: KindRemux, Src: filepath.Join(root, "in", "a.mkv"), Dst: filepath.Join(root, "out", "a.mkv"), ArchiveDst: filepath.Join(root, "arc", "a.mkv")}
			for _, f := range []struct {
				on   bool
				path string
			}{{c.src, op.Src}, {c.dst, op.Dst}, {c.arc, op.ArchiveDst}} {
				if f.on {
					asFile(t, f.path, 3)
				}
			}
			got := Assess(op, BeginState{})
			if got.Outcome != c.want || got.Reason == "" {
				t.Fatalf("got %v %q, want %v", got.Outcome, got.Reason, c.want)
			}
			if got.Outcome == Conflict && !strings.Contains(got.Reason, root) {
				t.Errorf("conflict reason %q names no absolute path", got.Reason)
			}
		})
	}
}

func TestAssessRemuxIgnoresPartial(t *testing.T) {
	root := t.TempDir()
	op := Op{Kind: KindRemux, Src: filepath.Join(root, "a.mkv"), Dst: filepath.Join(root, "o", "a.mkv"), ArchiveDst: filepath.Join(root, "arc", "a.mkv")}
	asFile(t, op.Src, 3)
	asFile(t, op.Dst+".partial", 1)
	if got := Assess(op, BeginState{}); got.Outcome != Redo {
		t.Fatalf("got %v %q, want redo", got.Outcome, got.Reason)
	}
}

func TestAssessRemuxNonRegular(t *testing.T) {
	root := t.TempDir()
	op := Op{Kind: KindRemux, Src: filepath.Join(root, "a.mkv"), Dst: filepath.Join(root, "o", "a.mkv"), ArchiveDst: filepath.Join(root, "arc", "a.mkv")}
	asFile(t, filepath.Join(root, "real"), 3)
	asLink(t, "real", op.Src)
	if got := Assess(op, BeginState{}); got.Outcome != Conflict {
		t.Fatalf("symlink src: got %v %q", got.Outcome, got.Reason)
	}
	os.Remove(op.Src)
	asFile(t, op.Src, 3)
	asDir(t, op.Dst)
	if got := Assess(op, BeginState{}); got.Outcome != Conflict {
		t.Fatalf("dir dst: got %v %q", got.Outcome, got.Reason)
	}
}

func TestAssessRedoKinds(t *testing.T) {
	states := map[string]func(t *testing.T, p string){
		"absent":  func(t *testing.T, p string) {},
		"file":    func(t *testing.T, p string) { asFile(t, p, 5) },
		"dir":     func(t *testing.T, p string) { asDir(t, p) },
		"full":    func(t *testing.T, p string) { asFile(t, filepath.Join(p, "x"), 5) },
		"symlink": func(t *testing.T, p string) { asLink(t, "nowhere", p) },
	}
	for _, kind := range []string{KindTitle, KindFlags, KindAudioLang, KindRmdirIfEmpty} {
		for name, mk := range states {
			t.Run(kind+"/"+name, func(t *testing.T) {
				root := t.TempDir()
				p := filepath.Join(root, "p")
				mk(t, p)
				got := Assess(Op{Kind: kind, Path: p}, BeginState{})
				if got.Outcome != Redo || got.Reason == "" {
					t.Fatalf("got %v %q, want redo", got.Outcome, got.Reason)
				}
			})
		}
	}
}

func TestAssessUnknownKind(t *testing.T) {
	got := Assess(Op{Kind: "teleport"}, BeginState{})
	if got.Outcome != Conflict || !strings.Contains(got.Reason, "teleport") {
		t.Fatalf("got %v %q", got.Outcome, got.Reason)
	}
}

func TestAssessLeavesTreeUntouched(t *testing.T) {
	root := t.TempDir()
	for _, f := range []string{"s/f", "d/f", "s/dir/a", "d/dir/b", "r/in", "r/out", "r/arc", "s/empty/.keep"} {
		asFile(t, filepath.Join(root, f), 4)
	}
	asDir(t, filepath.Join(root, "s", "emptydir"))
	asLink(t, "f", filepath.Join(root, "s", "link"))
	old := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
	filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err == nil && d.Type()&fs.ModeSymlink == 0 {
			os.Chtimes(p, old, old)
		}
		return nil
	})
	r := func(p string) string { return filepath.Join(root, p) }
	ops := []Op{
		{Kind: KindRename, Src: r("s/f"), Dst: r("d/f")},
		{Kind: KindArchiveFile, Src: r("s/link"), Dst: r("d/none")},
		{Kind: KindRenameDir, Src: r("s"), Dst: r("d")},
		{Kind: KindMoveDir, Src: r("s/dir"), Dst: r("d/new")},
		{Kind: KindArchiveDir, Src: r("gone"), Dst: r("d")},
		{Kind: KindRemux, Src: r("r/in"), Dst: r("r/out"), ArchiveDst: r("r/arc")},
		{Kind: KindRemux, Src: r("r/in"), Dst: r("r/out"), ArchiveDst: r("r/none")},
		{Kind: KindRmdirIfEmpty, Path: r("s/emptydir")},
		{Kind: KindTitle, Path: r("s/f")},
	}
	before := asSnapshot(t, root)
	for _, op := range ops {
		Assess(op, BeginState{Size: asSize(4), Tree: map[string]int64{"f": 4}})
	}
	if after := asSnapshot(t, root); !reflect.DeepEqual(before, after) {
		t.Fatalf("tree changed:\nbefore %v\nafter  %v", before, after)
	}
}
