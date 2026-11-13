package library

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

// This file holds every call that removes something from the filesystem. The
// tools are built so that nothing can delete media: a source file goes only
// after its copy has been proven whole, and the rest are files and directories
// this package made or found empty. TestNoDeletionOutsideRemoveGo keeps the
// list this short.

// unlinkSource removes src once dst holds all of it: dst must be a regular
// file of exactly want bytes. This is the last step of a cross-device move, so
// the file exists whole in at least one place at every instant.
func unlinkSource(src, dst string, want int64) error {
	info, err := os.Lstat(dst)
	if err != nil {
		return fmt.Errorf("not removing %s: cannot read the copy at %s: %w", src, dst, err)
	}
	if !info.Mode().IsRegular() || info.Size() != want {
		return fmt.Errorf("not removing %s: the copy at %s is not a regular file of %d bytes", src, dst, want)
	}
	return os.Remove(src)
}

// discardPartial removes the temporary copy of an interrupted move. Only a
// name ending in .partial is ever removed.
func discardPartial(path string) error {
	if len(path) <= len(partialSuffix) || path[len(path)-len(partialSuffix):] != partialSuffix {
		return fmt.Errorf("refusing to discard %s: it is not a %s file", path, partialSuffix)
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// removeProbe removes an empty file created to prove a destination can be
// created. It refuses anything that has content.
func removeProbe(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() != 0 {
		return fmt.Errorf("refusing to remove %s: it is not an empty file", path)
	}
	return os.Remove(path)
}

// rmdirEmpty removes dir if it is empty. unlinkat(AT_REMOVEDIR) can never
// remove a file, whatever the path turns out to name.
func rmdirEmpty(dir string) error { return unix.Rmdir(dir) }
