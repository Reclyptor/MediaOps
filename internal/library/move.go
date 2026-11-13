package library

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"

	"github.com/reclyptor/mediaops/kit/fsutil"
)

const partialSuffix = ".partial"

// copyBufferSize is the size of one copy read. The destination is a NAS, where
// a few large writes beat many small ones.
const copyBufferSize = 4 << 20

// ErrExists is returned when a move would land on a name that is taken. Nothing
// the library does overwrites a file.
var ErrExists = errors.New("destination already exists")

// Mover moves single files. The zero value is ready to use.
type Mover struct {
	// Rename is os.Rename. Tests replace it to make a rename fail with EXDEV.
	Rename func(oldpath, newpath string) error

	// copy is io.CopyBuffer. Tests replace it to make a copy short or to change
	// the source under it.
	copy copyFunc
}

// copyFunc is io.CopyBuffer's shape.
type copyFunc func(dst io.Writer, src io.Reader, buf []byte) (int64, error)

func (m Mover) rename(oldpath, newpath string) error {
	if m.Rename != nil {
		return m.Rename(oldpath, newpath)
	}
	return os.Rename(oldpath, newpath)
}

func (m Mover) copyBuffer(dst io.Writer, src io.Reader, buf []byte) (int64, error) {
	if m.copy != nil {
		return m.copy(dst, src, buf)
	}
	return io.CopyBuffer(dst, src, buf)
}

// Move moves the regular file src to dst, which must not exist and whose
// directory must. It renames where it can. Crossing a filesystem, the normal
// case now that the archive lives on another NAS, takes the safe route:
//
//  1. copy to dst + ".partial", so an interrupted copy can never be taken for a
//     finished file;
//  2. fsync the copy, give it the source's mode and times, and check it is as
//     long as the source and that the source did not change under it;
//  3. rename it into place and sync the directory;
//  4. only then unlink the source.
//
// Whatever fails, the source is left whole. A failure after step 3 leaves both
// files, and the error says so.
func (m Mover) Move(ctx context.Context, src, dst string) error {
	src, dst = filepath.Clean(src), filepath.Clean(dst)
	if src == dst {
		return fmt.Errorf("cannot move %s onto itself", src)
	}
	srcInfo, err := os.Lstat(src)
	if err != nil {
		return fmt.Errorf("cannot read the source %s: %w", src, err)
	}
	if !srcInfo.Mode().IsRegular() {
		return fmt.Errorf("the source %s is not a regular file", src)
	}
	if _, err := os.Lstat(dst); err == nil {
		return fmt.Errorf("%s: %w", dst, ErrExists)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("cannot check the destination %s: %w", dst, err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	err = m.rename(src, dst)
	if err == nil {
		return nil
	}
	if !errors.Is(err, syscall.EXDEV) {
		return fmt.Errorf("renaming %s to %s: %w", src, dst, err)
	}
	return m.copyAcross(ctx, src, dst, srcInfo)
}

func (m Mover) copyAcross(ctx context.Context, src, dst string, srcInfo os.FileInfo) (err error) {
	tmp := dst + partialSuffix
	size := srcInfo.Size()

	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("opening %s: %w", src, err)
	}
	defer in.Close()

	// A .partial left by an interrupted earlier attempt is this function's own
	// leftover, so it is started over rather than refused.
	out, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, fsutil.FileMode)
	if err != nil {
		return fmt.Errorf("creating %s: %w", tmp, err)
	}
	placed := false
	defer func() {
		if err == nil || placed {
			return
		}
		out.Close()
		if rmErr := discardPartial(tmp); rmErr != nil {
			err = errors.Join(err, fmt.Errorf("could not remove %s: %w", tmp, rmErr))
		}
	}()

	// The writer is wrapped so *os.File's ReadFrom does not swap the large
	// buffer for a small one.
	n, err := m.copyBuffer(struct{ io.Writer }{out}, ctxReader{ctx, in}, make([]byte, copyBufferSize))
	if err != nil {
		return fmt.Errorf("copying %s to %s: %w", src, tmp, err)
	}
	if err := out.Sync(); err != nil {
		return fmt.Errorf("syncing %s: %w", tmp, err)
	}
	if err := out.Close(); err != nil {
		return fmt.Errorf("closing %s: %w", tmp, err)
	}
	if err := os.Chmod(tmp, srcInfo.Mode().Perm()); err != nil {
		return fmt.Errorf("setting the mode of %s: %w", tmp, err)
	}
	if err := os.Chtimes(tmp, srcInfo.ModTime(), srcInfo.ModTime()); err != nil {
		return fmt.Errorf("setting the times of %s: %w", tmp, err)
	}

	tmpInfo, err := os.Lstat(tmp)
	if err != nil {
		return fmt.Errorf("cannot read back %s: %w", tmp, err)
	}
	if n != size || tmpInfo.Size() != size {
		return fmt.Errorf("short copy: %s is %d bytes, %d were copied and %s holds %d", src, size, n, tmp, tmpInfo.Size())
	}
	now, err := os.Lstat(src)
	if err != nil {
		return fmt.Errorf("cannot read the source %s after copying it: %w", src, err)
	}
	if now.Size() != size || !now.ModTime().Equal(srcInfo.ModTime()) {
		return fmt.Errorf("%s changed while it was being copied", src)
	}

	// Same directory, so this rename cannot cross a filesystem.
	if err := os.Rename(tmp, dst); err != nil {
		return fmt.Errorf("renaming %s into place: %w", tmp, err)
	}
	// The copy is final from here on: both files exist and neither is removed on
	// an error.
	placed = true
	if err := fsutil.SyncDir(filepath.Dir(dst)); err != nil {
		return keepBoth(src, dst, fmt.Errorf("syncing the directory of %s: %w", dst, err))
	}
	if err := unlinkSource(src, dst, size); err != nil {
		return keepBoth(src, dst, err)
	}
	return nil
}

// keepBoth is the error for a move that finished its copy but not its source
// removal. The destination must not be discarded, so it is not, and the caller
// is told where each file is.
func keepBoth(src, dst string, err error) error {
	return &keptBothError{src: src, dst: dst, err: err}
}

type keptBothError struct {
	src, dst string
	err      error
}

func (e *keptBothError) Error() string {
	return fmt.Sprintf("the copy at %s is complete but %s is still in place: %v", e.dst, e.src, e.err)
}

func (e *keptBothError) Unwrap() error { return e.err }

// ctxReader stops a copy when its context ends.
type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

func (c ctxReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(p)
}
