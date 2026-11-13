// Package fsutil writes files the way the NAS group expects them: atomically,
// with explicit modes that no umask can change.
package fsutil

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// FileMode and DirMode are the library convention: group-writable, closed to
// everyone else.
const (
	FileMode os.FileMode = 0o660
	DirMode  os.FileMode = 0o770
)

// syncDir is replaced in tests to observe when the directory is synced.
var syncDir = realSyncDir

func realSyncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	if err := d.Sync(); err != nil {
		d.Close()
		return err
	}
	return d.Close()
}

// SyncDir flushes dir, so a name created or renamed in it survives a crash.
func SyncDir(dir string) error { return syncDir(dir) }

// WriteFile replaces path with data in one step. The temp file is chmodded
// explicitly because creating it gives 0600 and the umask would otherwise
// decide the rest; the directory is synced after the rename so the new name
// survives a crash. The temp file never outlives a failure.
func WriteFile(path string, data []byte, mode os.FileMode) (err error) {
	dir, base := filepath.Split(path)
	if dir == "" {
		dir = "."
	}
	tmp, err := os.CreateTemp(dir, "."+base+".tmp-*")
	if err != nil {
		return fmt.Errorf("creating a temp file for %s: %w", path, err)
	}
	defer func() {
		if err != nil {
			tmp.Close()
			os.Remove(tmp.Name())
		}
	}()

	if err := tmp.Chmod(mode); err != nil {
		return fmt.Errorf("setting the mode of %s: %w", tmp.Name(), err)
	}
	if _, err := tmp.Write(data); err != nil {
		return fmt.Errorf("writing %s: %w", tmp.Name(), err)
	}
	if err := tmp.Sync(); err != nil {
		return fmt.Errorf("syncing %s: %w", tmp.Name(), err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("closing %s: %w", tmp.Name(), err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("replacing %s: %w", path, err)
	}
	if err := syncDir(dir); err != nil {
		return fmt.Errorf("syncing the directory of %s: %w", path, err)
	}
	return nil
}

// WriteJSON writes v as indented JSON with a trailing newline, mode FileMode.
func WriteJSON(path string, v any) error {
	data, err := json.MarshalIndent(v, "", " ")
	if err != nil {
		return fmt.Errorf("encoding %s: %w", path, err)
	}
	return WriteFile(path, append(data, '\n'), FileMode)
}

// ApplyLibraryModes brings a rip folder to the library convention: DirMode on
// the folder and FileMode on the regular files directly in it. makemkvcon
// creates its files 0666 whatever the umask, so this is what keeps them from
// reaching the library world-writable. Ownership is never touched; the
// owning group comes from the NAS export.
func ApplyLibraryModes(dir string) error {
	if err := os.Chmod(dir, DirMode); err != nil {
		return fmt.Errorf("setting the mode of %s: %w", dir, err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("listing %s: %w", dir, err)
	}
	for _, e := range entries {
		if !e.Type().IsRegular() {
			continue
		}
		path := filepath.Join(dir, e.Name())
		if err := os.Chmod(path, FileMode); err != nil {
			return fmt.Errorf("setting the mode of %s: %w", path, err)
		}
	}
	return nil
}
