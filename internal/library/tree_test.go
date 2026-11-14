package library

import (
	"context"
	"errors"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// treePair is a source tree with nested files, an empty subdirectory and a
// file mode worth keeping, next to a destination parent.
func treePair(t *testing.T) (src, dst string) {
	t.Helper()
	dir := t.TempDir()
	src = filepath.Join(dir, "from", "Show")
	putFile(t, filepath.Join(src, "S01", "e01.mkv"), "episode one")
	putFile(t, filepath.Join(src, "S01", "e02.mkv"), "ep two")
	putFile(t, filepath.Join(src, "extras", "deep", "x.txt"), "x")
	if err := os.MkdirAll(filepath.Join(src, "empty", "nested"), 0o770); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(src, "S01", "e02.mkv"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "to"), 0o770); err != nil {
		t.Fatal(err)
	}
	return src, filepath.Join(dir, "to", "Show")
}

func TestMoveTreeRenamesOnOneDevice(t *testing.T) {
	src, dst := treePair(t)
	var renames int
	m := Mover{Rename: func(a, b string) error { renames++; return os.Rename(a, b) }}
	if err := m.MoveTree(context.Background(), src, dst); err != nil {
		t.Fatal(err)
	}
	if renames != 1 || exists(src) {
		t.Fatalf("renames = %d, src exists = %v", renames, exists(src))
	}
	if got, _ := TreeManifest(dst); len(got) != 3 {
		t.Fatalf("manifest = %v", got)
	}
}

func TestMoveTreeAcrossDevices(t *testing.T) {
	src, dst := treePair(t)
	want, err := TreeManifest(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := (Mover{Rename: exdev}).MoveTree(context.Background(), src, dst); err != nil {
		t.Fatal(err)
	}
	if exists(src) {
		t.Fatal("the source tree is still there")
	}
	got, err := TreeManifest(dst)
	if err != nil || !maps.Equal(got, want) {
		t.Fatalf("manifest = %v, %v; want %v", got, err, want)
	}
	for _, dir := range []string{"empty/nested", "extras/deep"} {
		if info, err := os.Stat(filepath.Join(dst, dir)); err != nil || !info.IsDir() || info.Mode().Perm() != 0o770 {
			t.Fatalf("%s: %v %v", dir, info, err)
		}
	}
	if info, err := os.Stat(filepath.Join(dst, "S01", "e02.mkv")); err != nil || info.Mode().Perm() != 0o640 {
		t.Fatalf("mode: %v %v", info, err)
	}
}

func TestMoveTreeAcrossRigDevices(t *testing.T) {
	r := newRig(t)
	r.put("anime/Show/S01/e01.mkv", "one")
	r.mkdir("anime/Show/empty")
	dst := filepath.Join(r.archive, "Show")
	if err := r.d.Mover.MoveTree(context.Background(), r.path("anime/Show"), dst); err != nil {
		t.Fatal(err)
	}
	if exists(r.path("anime/Show")) || r.read(filepath.Join(dst, "S01", "e01.mkv")) != "one" || !exists(filepath.Join(dst, "empty")) {
		t.Fatalf("%v", r.snapshot())
	}
}

func TestMoveTreeRefusals(t *testing.T) {
	ctx := context.Background()
	cases := map[string]func(t *testing.T, src, dst string){
		"dst exists": func(t *testing.T, src, dst string) {
			if err := os.Mkdir(dst, 0o770); err != nil {
				t.Fatal(err)
			}
		},
		"dst parent missing": func(t *testing.T, src, dst string) {
			if err := os.Remove(filepath.Dir(dst)); err != nil {
				t.Fatal(err)
			}
		},
		"src is a file": func(t *testing.T, src, dst string) {
			if err := os.RemoveAll(src); err != nil {
				t.Fatal(err)
			}
			putFile(t, src, "x")
		},
		"symlink inside": func(t *testing.T, src, dst string) {
			if err := os.Symlink("e01.mkv", filepath.Join(src, "S01", "link.mkv")); err != nil {
				t.Fatal(err)
			}
		},
		"fifo inside": func(t *testing.T, src, dst string) {
			if err := syscall.Mkfifo(filepath.Join(src, "extras", "pipe"), 0o660); err != nil {
				t.Fatal(err)
			}
		},
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			src, dst := treePair(t)
			setup(t, src, dst)
			before := dirSnapshot(t, filepath.Dir(filepath.Dir(src)))
			if err := (Mover{Rename: exdev}).MoveTree(ctx, src, dst); err == nil {
				t.Fatal("MoveTree succeeded")
			}
			if after := dirSnapshot(t, filepath.Dir(filepath.Dir(src))); !maps.Equal(before, after) {
				t.Fatalf("something moved:\n before %v\n after  %v", before, after)
			}
		})
	}
}

func TestMoveTreeRefusesASymlinkSource(t *testing.T) {
	src, dst := treePair(t)
	link := filepath.Join(filepath.Dir(src), "link")
	if err := os.Symlink(src, link); err != nil {
		t.Fatal(err)
	}
	if err := (Mover{Rename: exdev}).MoveTree(context.Background(), link, dst); err == nil || exists(dst) {
		t.Fatalf("err = %v", err)
	}
	if got, _ := TreeManifest(src); len(got) != 3 {
		t.Fatalf("the real tree changed: %v", got)
	}
}

// dirSnapshot maps every path under root to its type and, for files, content.
func dirSnapshot(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(root, func(path string, e os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		switch {
		case e.IsDir():
			out[path] = "dir"
		case e.Type().IsRegular():
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			out[path] = string(data)
		default:
			out[path] = e.Type().String()
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestMoveTreeInterruptedThenResumed(t *testing.T) {
	src, dst := treePair(t)
	want, _ := TreeManifest(src)

	// The context ends while the second file is being copied.
	ctx, cancel := context.WithCancel(context.Background())
	var renames int
	m := Mover{Rename: func(oldpath, newpath string) error {
		if renames++; renames == 3 { // the tree, the first file, the second
			cancel()
		}
		return exdev(oldpath, newpath)
	}}
	err := m.MoveTree(ctx, src, dst)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	for rel := range want {
		inSrc, inDst := exists(filepath.Join(src, rel)), exists(filepath.Join(dst, rel))
		if inSrc == inDst {
			t.Fatalf("%s: in src %v, in dst %v", rel, inSrc, inDst)
		}
	}
	if !exists(src) || exists(filepath.Join(dst, "S01", "e01.mkv.partial")) {
		t.Fatal("source gone or a partial left behind")
	}

	if err := (Mover{Rename: exdev}).ResumeTree(context.Background(), src, dst); err != nil {
		t.Fatal(err)
	}
	got, err := TreeManifest(dst)
	if err != nil || !maps.Equal(got, want) || exists(src) {
		t.Fatalf("manifest = %v, %v; src exists = %v", got, err, exists(src))
	}
}

func TestResumeTreeConflict(t *testing.T) {
	src, dst := treePair(t)
	putFile(t, filepath.Join(dst, "S01", "e01.mkv"), "different")
	before := dirSnapshot(t, filepath.Dir(filepath.Dir(src)))
	err := (Mover{Rename: exdev}).ResumeTree(context.Background(), src, dst)
	if err == nil {
		t.Fatal("ResumeTree settled a conflict")
	}
	for _, p := range []string{src, dst} {
		if !strings.Contains(err.Error(), p) {
			t.Errorf("error %q does not name %s", err, p)
		}
	}
	if after := dirSnapshot(t, filepath.Dir(filepath.Dir(src))); !maps.Equal(before, after) {
		t.Fatal("something moved or was removed")
	}
}

func TestResumeTreeRefusals(t *testing.T) {
	src, dst := treePair(t)
	m := Mover{Rename: exdev}
	if err := m.ResumeTree(context.Background(), src, dst); err == nil {
		t.Fatal("ResumeTree accepted a missing destination")
	}
	if err := os.Mkdir(dst, 0o770); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("e01.mkv", filepath.Join(src, "S01", "link")); err != nil {
		t.Fatal(err)
	}
	if err := m.ResumeTree(context.Background(), src, dst); err == nil {
		t.Fatal("ResumeTree accepted a symlink")
	}
	if err := m.ResumeTree(context.Background(), filepath.Join(src, "S01", "e01.mkv"), dst); err == nil {
		t.Fatal("ResumeTree accepted a file as the source")
	}
}

func TestTreeManifestErrors(t *testing.T) {
	dir := t.TempDir()
	file := putFile(t, filepath.Join(dir, "a", "f"), "abc")
	if _, err := TreeManifest(filepath.Join(dir, "missing")); err == nil {
		t.Error("a missing root is not an error")
	}
	if _, err := TreeManifest(file); err == nil {
		t.Error("a file root is not an error")
	}
	link := filepath.Join(dir, "linkroot")
	if err := os.Symlink(filepath.Dir(file), link); err != nil {
		t.Fatal(err)
	}
	if _, err := TreeManifest(link); err == nil {
		t.Error("a symlink root is not an error")
	}
	got, err := TreeManifest(filepath.Dir(file))
	if err != nil || got["f"] != 3 || len(got) != 1 {
		t.Fatalf("manifest = %v, %v", got, err)
	}
	if err := os.Symlink("f", filepath.Join(dir, "a", "l")); err != nil {
		t.Fatal(err)
	}
	if _, err := TreeManifest(dir); err == nil {
		t.Error("a nested symlink is not an error")
	}
}

func TestScanTreeNamesWhatItRefuses(t *testing.T) {
	dir := t.TempDir()
	putFile(t, filepath.Join(dir, "f"), "x")
	if err := os.Symlink("f", filepath.Join(dir, "l")); err != nil {
		t.Fatal(err)
	}
	if _, err := TreeManifest(dir); err == nil || !strings.Contains(err.Error(), "is a symlink") {
		t.Fatalf("symlink err = %v", err)
	}
	if err := os.Remove(filepath.Join(dir, "l")); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(filepath.Join(dir, "p"), 0o660); err != nil {
		t.Fatal(err)
	}
	if _, err := TreeManifest(dir); err == nil || !strings.Contains(err.Error(), "named pipe") {
		t.Fatalf("fifo err = %v", err)
	}
}
