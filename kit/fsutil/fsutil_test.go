package fsutil

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func mode(t *testing.T, path string) os.FileMode {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info.Mode().Perm()
}

func TestAtomicWriteMode(t *testing.T) {
	dir := t.TempDir()
	old := syscall.Umask(0o277)
	defer syscall.Umask(old)

	path := filepath.Join(dir, "state.json")
	for range 2 { // fresh create, then replace
		if err := WriteFile(path, []byte("x"), FileMode); err != nil {
			t.Fatal(err)
		}
		if got := mode(t, path); got != 0o660 {
			t.Fatalf("mode = %o, want 660", got)
		}
	}
}

func TestWriteFileLeavesNoTempOnError(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "state.json")
	// A non-empty directory in the way makes the final rename fail.
	if err := os.MkdirAll(filepath.Join(target, "child"), 0o770); err != nil {
		t.Fatal(err)
	}
	if err := WriteFile(target, []byte("x"), FileMode); err == nil {
		t.Fatal("expected the rename to fail")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "state.json" {
		t.Fatalf("directory holds %v, want only state.json", entries)
	}
}

func TestWriteFileSyncsDirectoryAfterRename(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	var synced []string
	syncDir = func(d string) error {
		// At sync time the new name must already be in place and the temp gone.
		data, err := os.ReadFile(path)
		if err != nil || string(data) != "new" {
			t.Errorf("directory synced before the rename: %v %q", err, data)
		}
		entries, _ := os.ReadDir(d)
		if len(entries) != 1 {
			t.Errorf("temp file still present at sync: %v", entries)
		}
		synced = append(synced, d)
		return nil
	}
	t.Cleanup(func() { syncDir = realSyncDir })

	if err := WriteFile(path, []byte("new"), FileMode); err != nil {
		t.Fatal(err)
	}
	if len(synced) != 1 || filepath.Clean(synced[0]) != dir {
		t.Fatalf("synced = %v, want exactly %s", synced, dir)
	}
}

func TestWriteFileReportsDirectorySyncFailure(t *testing.T) {
	boom := errors.New("boom")
	syncDir = func(string) error { return boom }
	t.Cleanup(func() { syncDir = realSyncDir })

	err := WriteFile(filepath.Join(t.TempDir(), "f"), []byte("x"), FileMode)
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want it to wrap the sync failure", err)
	}
}

func TestWriteJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.json")
	if err := WriteJSON(path, map[string]any{"a": []int{1}}); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(path)
	if want := "{\n \"a\": [\n  1\n ]\n}\n"; string(got) != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	if m := mode(t, path); m != FileMode {
		t.Fatalf("mode = %o", m)
	}
	if err := WriteJSON(path, make(chan int)); err == nil || !strings.Contains(err.Error(), "encoding") {
		t.Fatalf("unencodable value: err = %v", err)
	}
}

func TestLibraryModes(t *testing.T) {
	old := syscall.Umask(0)
	defer syscall.Umask(old)

	dir := filepath.Join(t.TempDir(), "rip")
	sub := filepath.Join(dir, "sub")
	inner := filepath.Join(sub, "inner.mkv")
	file := filepath.Join(dir, "t00.mkv")
	link := filepath.Join(dir, "link")
	for _, d := range []string{dir, sub} {
		if err := os.Mkdir(d, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, f := range []string{file, inner} {
		if err := os.WriteFile(f, []byte("x"), 0o666); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(file, link); err != nil {
		t.Fatal(err)
	}

	if err := ApplyLibraryModes(dir); err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]os.FileMode{dir: 0o770, file: 0o660, sub: 0o755, inner: 0o666} {
		if got := mode(t, path); got != want {
			t.Errorf("%s mode = %o, want %o", path, got, want)
		}
	}
	if info, err := os.Lstat(link); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Errorf("symlink was disturbed: %v", err)
	}
}

func TestApplyLibraryModesMissingDir(t *testing.T) {
	if err := ApplyLibraryModes(filepath.Join(t.TempDir(), "nope")); err == nil {
		t.Fatal("expected an error")
	}
}
