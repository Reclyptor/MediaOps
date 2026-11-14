package library

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"syscall"

	"github.com/reclyptor/mediaops/kit/fsutil"
)

// tree is what a directory holds: its subdirectories, parents before children,
// and its regular files with their sizes, both by path relative to the root.
type tree struct {
	dirs  []string
	files map[string]int64
}

// scanTree reads everything under root without following a link. Only
// directories and regular files are a tree this package will move; a symlink
// or a special file anywhere refuses the whole scan, naming the entry, so a
// tree is never half-moved because of something it could not carry.
func scanTree(root string) (tree, error) {
	info, err := os.Lstat(root)
	if err != nil {
		return tree{}, fmt.Errorf("cannot read %s: %w", root, err)
	}
	if !info.IsDir() {
		return tree{}, fmt.Errorf("%s is not a directory", root)
	}
	t := tree{files: map[string]int64{}}
	err = filepath.WalkDir(root, func(path string, e fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		switch {
		case e.IsDir():
			if rel != "." {
				t.dirs = append(t.dirs, rel)
			}
		case e.Type().IsRegular():
			fi, err := e.Info()
			if err != nil {
				return err
			}
			t.files[rel] = fi.Size()
		case e.Type()&fs.ModeSymlink != 0:
			return fmt.Errorf("%s is a symlink; a tree holding one is never moved", path)
		default:
			return fmt.Errorf("%s is a special file (%s); a tree holding one is never moved", path, specialKind(e.Type()))
		}
		return nil
	})
	if err != nil {
		return tree{}, fmt.Errorf("scanning %s: %w", root, err)
	}
	return t, nil
}

// specialKind names a non-regular, non-directory, non-symlink file type.
func specialKind(m fs.FileMode) string {
	switch {
	case m&fs.ModeNamedPipe != 0:
		return "named pipe"
	case m&fs.ModeSocket != 0:
		return "socket"
	case m&fs.ModeDevice != 0 && m&fs.ModeCharDevice != 0:
		return "character device"
	case m&fs.ModeDevice != 0:
		return "block device"
	default:
		return m.Type().String()
	}
}

// TreeManifest is the size of every regular file under dir, by path relative to
// dir. It fails closed: a symlink or special file anywhere, or a dir that is
// not a directory, is an error rather than an entry left out of the comparison.
func TreeManifest(dir string) (map[string]int64, error) {
	t, err := scanTree(dir)
	if err != nil {
		return nil, err
	}
	return t.files, nil
}

// MoveTree moves the directory src to dst, which must not exist and whose
// parent must. A rename is the whole job on one filesystem. Across filesystems
// it is done file by file through Move, so every file is copied, proven and
// only then unlinked, and the emptied source directories are removed last:
//
//  1. scan src completely and refuse, before anything moves, a tree that holds
//     anything but directories and regular files;
//  2. recreate the directories under dst, empty ones too;
//  3. Move each file, in sorted order, checking ctx between files;
//  4. remove the source directories bottom-up, src last, each only if empty;
//  5. check that dst holds exactly the files, with exactly the sizes, that
//     src held. A mismatch is an error and removes nothing.
//
// An interruption leaves every file whole in exactly one place, or in both
// where Move says so. ResumeTree finishes such a move.
func (m Mover) MoveTree(ctx context.Context, src, dst string) error {
	src, dst = filepath.Clean(src), filepath.Clean(dst)
	if src == dst || within(src, dst) {
		return fmt.Errorf("cannot move %s into itself at %s", src, dst)
	}
	srcInfo, err := os.Lstat(src)
	if err != nil {
		return fmt.Errorf("cannot read the source %s: %w", src, err)
	}
	if !srcInfo.IsDir() {
		return fmt.Errorf("the source %s is not a directory", src)
	}
	if _, err := os.Lstat(dst); err == nil {
		return fmt.Errorf("%s: %w", dst, ErrExists)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("cannot check the destination %s: %w", dst, err)
	}
	parent, err := os.Stat(filepath.Dir(dst))
	if err != nil {
		return fmt.Errorf("cannot read the directory of the destination %s: %w", dst, err)
	}
	if !parent.IsDir() {
		return fmt.Errorf("the directory of the destination %s is not a directory", dst)
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

	t, err := scanTree(src)
	if err != nil {
		return err
	}
	if err := os.Mkdir(dst, fsutil.DirMode); err != nil {
		return fmt.Errorf("creating %s: %w", dst, err)
	}
	if err := m.moveContents(ctx, src, dst, t); err != nil {
		return err
	}
	got, err := TreeManifest(dst)
	if err != nil {
		return fmt.Errorf("verifying %s: %w", dst, err)
	}
	if !maps.Equal(got, t.files) {
		return fmt.Errorf("%s does not hold what %s held: %d files expected, %d found", dst, src, len(t.files), len(got))
	}
	return nil
}

// ResumeTree finishes a cross-device MoveTree that was interrupted: some files
// are already under dst, the rest are still under src. Both must be
// directories. src is scanned as MoveTree scans it, and a file that exists at
// the same relative path on both sides is a conflict that is reported and never
// settled by deleting either one. The remaining files are moved and the emptied
// source directories removed.
//
// ResumeTree does not verify the finished tree: it does not know what src held
// before the first attempt. The caller compares TreeManifest(dst) with the
// manifest it recorded then.
func (m Mover) ResumeTree(ctx context.Context, src, dst string) error {
	src, dst = filepath.Clean(src), filepath.Clean(dst)
	if src == dst || within(src, dst) || within(dst, src) {
		return fmt.Errorf("cannot resume moving %s to %s: one holds the other", src, dst)
	}
	for _, dir := range []string{src, dst} {
		info, err := os.Lstat(dir)
		if err != nil {
			return fmt.Errorf("cannot read %s: %w", dir, err)
		}
		if !info.IsDir() {
			return fmt.Errorf("%s is not a directory", dir)
		}
	}
	t, err := scanTree(src)
	if err != nil {
		return err
	}
	for _, rel := range slices.Sorted(maps.Keys(t.files)) {
		if exists(filepath.Join(dst, rel)) {
			return fmt.Errorf("%s exists in both %s and %s; neither is removed", rel, src, dst)
		}
	}
	return m.moveContents(ctx, src, dst, t)
}

// moveContents moves what t holds from src into the existing directory dst and
// removes the source directories it emptied.
func (m Mover) moveContents(ctx context.Context, src, dst string, t tree) error {
	for _, rel := range t.dirs {
		dir := filepath.Join(dst, rel)
		if err := os.Mkdir(dir, fsutil.DirMode); err != nil && !errors.Is(err, os.ErrExist) {
			return fmt.Errorf("creating %s: %w", dir, err)
		}
		if err := os.Chmod(dir, fsutil.DirMode); err != nil {
			return fmt.Errorf("setting the mode of %s: %w", dir, err)
		}
	}
	for _, rel := range slices.Sorted(maps.Keys(t.files)) {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := m.Move(ctx, filepath.Join(src, rel), filepath.Join(dst, rel)); err != nil {
			return fmt.Errorf("moving %s: %w", rel, err)
		}
	}
	for _, rel := range slices.Backward(t.dirs) {
		if err := removeEmptiedDir(filepath.Join(src, rel)); err != nil {
			return err
		}
	}
	return removeEmptiedDir(src)
}

// removeEmptiedDir removes a source directory every file of which has moved.
// One that is not empty means something appeared in it since the scan, and it
// is left alone.
func removeEmptiedDir(dir string) error {
	removed, err := RemoveDirIfEmpty(dir)
	if err != nil {
		return err
	}
	if !removed {
		return fmt.Errorf("%s is not empty after its files moved, or is gone; leaving it", dir)
	}
	return nil
}
