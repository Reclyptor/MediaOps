package library

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// exdev makes every first-try rename fail as it does across two filesystems.
func exdev(oldpath, newpath string) error {
	return &os.LinkError{Op: "rename", Old: oldpath, New: newpath, Err: syscall.EXDEV}
}

// pair is a source file and a destination directory in a temp dir.
func pair(t *testing.T, content string) (src, dst string) {
	t.Helper()
	dir := t.TempDir()
	src = putFile(t, filepath.Join(dir, "from", "ep.mkv"), content)
	if err := os.MkdirAll(filepath.Join(dir, "to"), 0o770); err != nil {
		t.Fatal(err)
	}
	return src, filepath.Join(dir, "to", "ep.mkv")
}

func TestMoveRenames(t *testing.T) {
	src, dst := pair(t, "payload")
	if err := (Mover{}).Move(context.Background(), src, dst); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(dst); string(got) != "payload" || exists(src) || exists(dst+partialSuffix) {
		t.Fatalf("dst = %q, src exists %v", got, exists(src))
	}
}

func TestMoveRefusals(t *testing.T) {
	ctx := context.Background()

	t.Run("destination exists is never overwritten", func(t *testing.T) {
		src, dst := pair(t, "new")
		putFile(t, dst, "precious")
		err := Mover{Rename: exdev}.Move(ctx, src, dst)
		if !errors.Is(err, ErrExists) {
			t.Fatalf("err = %v, want ErrExists", err)
		}
		if got, _ := os.ReadFile(dst); string(got) != "precious" || !exists(src) || exists(dst+partialSuffix) {
			t.Fatalf("a refused move changed something: dst = %q", got)
		}
	})

	t.Run("dangling symlink at the destination counts as taken", func(t *testing.T) {
		src, dst := pair(t, "new")
		if err := os.Symlink("/nonexistent/elsewhere", dst); err != nil {
			t.Fatal(err)
		}
		if err := (Mover{}).Move(ctx, src, dst); !errors.Is(err, ErrExists) {
			t.Fatalf("err = %v, want ErrExists", err)
		}
		if !exists(src) {
			t.Fatal("source lost")
		}
	})

	t.Run("source is missing, a directory or a symlink", func(t *testing.T) {
		_, dst := pair(t, "x")
		dir := t.TempDir()
		link := filepath.Join(dir, "link.mkv")
		if err := os.Symlink(dst, link); err != nil {
			t.Fatal(err)
		}
		for name, src := range map[string]string{"missing": filepath.Join(dir, "nope.mkv"), "directory": dir, "symlink": link} {
			if err := (Mover{}).Move(ctx, src, dst); err == nil {
				t.Errorf("%s: move succeeded", name)
			}
		}
	})

	t.Run("onto itself", func(t *testing.T) {
		src, _ := pair(t, "x")
		if err := (Mover{}).Move(ctx, src, src); err == nil || !exists(src) {
			t.Fatalf("err = %v, source exists %v", err, exists(src))
		}
	})

	t.Run("a rename error other than EXDEV is not a license to copy", func(t *testing.T) {
		src, dst := pair(t, "x")
		denied := func(o, n string) error { return &os.LinkError{Op: "rename", Old: o, New: n, Err: syscall.EACCES} }
		if err := (Mover{Rename: denied}).Move(ctx, src, dst); err == nil || !strings.Contains(err.Error(), "permission denied") {
			t.Fatalf("err = %v", err)
		}
		if !exists(src) || exists(dst) || exists(dst+partialSuffix) {
			t.Fatal("a failed rename left a copy behind")
		}
	})

	t.Run("a cancelled context moves nothing", func(t *testing.T) {
		src, dst := pair(t, "x")
		cancelled, cancel := context.WithCancel(ctx)
		cancel()
		if err := (Mover{}).Move(cancelled, src, dst); !errors.Is(err, context.Canceled) || !exists(src) || exists(dst) {
			t.Fatalf("err = %v", err)
		}
	})
}

// TestMoveAcrossDevices is the EXDEV path, taken by injecting the rename error.
func TestMoveAcrossDevices(t *testing.T) {
	ctx := context.Background()
	body := strings.Repeat("0123456789abcdef", 1<<16) // 1 MiB, several reads long

	t.Run("copies, keeps mode and times, and unlinks the source last", func(t *testing.T) {
		src, dst := pair(t, body)
		when := time.Date(2020, 3, 4, 5, 6, 7, 0, time.UTC)
		if err := os.Chmod(src, 0o640); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(src, when, when); err != nil {
			t.Fatal(err)
		}

		var sawPartial bool
		m := Mover{Rename: exdev}
		m.copy = func(w io.Writer, r io.Reader, buf []byte) (int64, error) {
			// While the bytes are in flight the file exists once, whole, at the
			// source; the copy is under a name nothing would mistake for it.
			sawPartial = exists(dst+partialSuffix) && exists(src) && !exists(dst)
			return io.CopyBuffer(w, r, buf)
		}
		if err := m.Move(ctx, src, dst); err != nil {
			t.Fatal(err)
		}
		if !sawPartial {
			t.Error("during the copy the source was gone, the destination existed, or there was no .partial")
		}
		info, err := os.Stat(dst)
		if err != nil {
			t.Fatal(err)
		}
		if got, _ := os.ReadFile(dst); string(got) != body {
			t.Error("the copy differs from the source")
		}
		if info.Mode().Perm() != 0o640 || !info.ModTime().Equal(when) {
			t.Errorf("mode %v, mtime %v; want 0640, %v", info.Mode().Perm(), info.ModTime(), when)
		}
		if exists(src) || exists(dst+partialSuffix) {
			t.Error("the source or the .partial survived a finished move")
		}
	})

	t.Run("a short copy fails, leaves the source whole and no partial", func(t *testing.T) {
		src, dst := pair(t, body)
		m := Mover{Rename: exdev}
		m.copy = func(w io.Writer, r io.Reader, buf []byte) (int64, error) {
			return io.CopyBuffer(w, io.LimitReader(r, int64(len(body)/2)), buf)
		}
		err := m.Move(ctx, src, dst)
		if err == nil || !strings.Contains(err.Error(), "short copy") {
			t.Fatalf("err = %v, want a short copy", err)
		}
		if got, _ := os.ReadFile(src); string(got) != body {
			t.Fatal("the source was damaged by a failed copy")
		}
		if exists(dst) || exists(dst+partialSuffix) {
			t.Fatal("a failed copy left a destination or a .partial")
		}
	})

	t.Run("a source that changes while it is copied is refused", func(t *testing.T) {
		src, dst := pair(t, body)
		m := Mover{Rename: exdev}
		m.copy = func(w io.Writer, r io.Reader, buf []byte) (int64, error) {
			n, err := io.CopyBuffer(w, r, buf)
			f, openErr := os.OpenFile(src, os.O_APPEND|os.O_WRONLY, 0)
			if openErr != nil {
				t.Fatal(openErr)
			}
			_, _ = f.WriteString("more")
			_ = f.Close()
			return n, err
		}
		err := m.Move(ctx, src, dst)
		if err == nil || !strings.Contains(err.Error(), "changed while") {
			t.Fatalf("err = %v, want changed-while-copied", err)
		}
		if !exists(src) || exists(dst) || exists(dst+partialSuffix) {
			t.Fatal("a refused copy left the tree different")
		}
	})

	t.Run("a copy error discards the partial", func(t *testing.T) {
		src, dst := pair(t, body)
		m := Mover{Rename: exdev}
		m.copy = func(w io.Writer, r io.Reader, buf []byte) (int64, error) {
			n, _ := io.CopyBuffer(w, io.LimitReader(r, 1000), buf)
			return n, errors.New("input/output error")
		}
		if err := m.Move(ctx, src, dst); err == nil || !strings.Contains(err.Error(), "input/output error") {
			t.Fatalf("err = %v", err)
		}
		if !exists(src) || exists(dst) || exists(dst+partialSuffix) {
			t.Fatal("a failed copy left the tree different")
		}
	})

	t.Run("a cancelled context stops the copy and keeps the source", func(t *testing.T) {
		src, dst := pair(t, body)
		cancelCtx, cancel := context.WithCancel(ctx)
		m := Mover{Rename: exdev}
		m.copy = func(w io.Writer, r io.Reader, buf []byte) (int64, error) {
			cancel()
			return io.CopyBuffer(w, r, buf)
		}
		if err := m.Move(cancelCtx, src, dst); !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want context.Canceled", err)
		}
		if !exists(src) || exists(dst) || exists(dst+partialSuffix) {
			t.Fatal("a stopped copy left the tree different")
		}
	})

	t.Run("a stale partial from an interrupted attempt is started over", func(t *testing.T) {
		src, dst := pair(t, body)
		putFile(t, dst+partialSuffix, "junk from last time, longer than nothing at all")
		if err := (Mover{Rename: exdev}).Move(ctx, src, dst); err != nil {
			t.Fatal(err)
		}
		if got, _ := os.ReadFile(dst); string(got) != body || exists(dst+partialSuffix) {
			t.Fatal("the stale partial leaked into the move")
		}
	})

	t.Run("an empty file moves", func(t *testing.T) {
		src, dst := pair(t, "")
		if err := (Mover{Rename: exdev}).Move(ctx, src, dst); err != nil {
			t.Fatal(err)
		}
		if info, err := os.Stat(dst); err != nil || info.Size() != 0 || exists(src) {
			t.Fatalf("stat %v, %v", info, err)
		}
	})

	t.Run("when the source cannot be removed both files are kept and the error says so", func(t *testing.T) {
		skipIfRoot(t)
		src, dst := pair(t, body)
		if err := os.Chmod(filepath.Dir(src), 0o500); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(filepath.Dir(src), 0o770) })

		err := (Mover{Rename: exdev}).Move(ctx, src, dst)
		var both *keptBothError
		if !errors.As(err, &both) || !strings.Contains(err.Error(), "is complete") {
			t.Fatalf("err = %v, want a kept-both error", err)
		}
		if got, _ := os.ReadFile(dst); string(got) != body {
			t.Fatal("the destination was not kept")
		}
		if !exists(src) {
			t.Fatal("the source is gone though its removal failed")
		}
	})
}

// TestUnlinkSourceNeedsAWholeCopy is the last line of defence: whatever calls
// it, a source is removed only when the destination is a regular file of the
// size it was before the move.
func TestUnlinkSourceNeedsAWholeCopy(t *testing.T) {
	cases := map[string]func(t *testing.T, dst string){
		"destination missing": func(*testing.T, string) {},
		"destination short":   func(t *testing.T, dst string) { putFile(t, dst, "abc") },
		"destination long":    func(t *testing.T, dst string) { putFile(t, dst, "abcdefghij") },
		"destination a link": func(t *testing.T, dst string) {
			putFile(t, dst+".real", "abcdef")
			if err := os.Symlink(dst+".real", dst); err != nil {
				t.Fatal(err)
			}
		},
		"destination a directory": func(t *testing.T, dst string) {
			if err := os.Mkdir(dst, 0o770); err != nil {
				t.Fatal(err)
			}
		},
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			src, dst := pair(t, "abcdef")
			setup(t, dst)
			if err := unlinkSource(src, dst, 6); err == nil {
				t.Fatal("unlinkSource removed a source with no whole copy")
			}
			if !exists(src) {
				t.Fatal("the source is gone")
			}
		})
	}

	src, dst := pair(t, "abcdef")
	putFile(t, dst, "abcdef")
	if err := unlinkSource(src, dst, 6); err != nil || exists(src) {
		t.Fatalf("err = %v, source exists %v", err, exists(src))
	}
}
