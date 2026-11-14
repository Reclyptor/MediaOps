package manifest

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// treeManifest maps every regular file under dir to its size, keyed by the
// path relative to dir. Symlinks and special files are not listed, matching
// library.TreeManifest, which is the executor's read-back.
func treeManifest(dir string) (map[string]int64, error) {
	tree := map[string]int64{}
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.Type().IsRegular() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		tree[rel] = info.Size()
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("reading the tree of %s: %w", dir, err)
	}
	return tree, nil
}

// regularFile reports whether path is a regular file, not following a symlink:
// a link is never moved as if it were the file it points at.
func regularFile(path string) (os.FileInfo, bool) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return nil, false
	}
	return info, true
}

// realDir reports whether path is a directory, not following a symlink.
func realDir(path string) bool {
	info, err := os.Lstat(path)
	return err == nil && info.IsDir()
}

// exists reports whether anything, a dangling symlink included, is at path.
func exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

// within reports whether path is root or lies below it. Unlike a string-prefix
// test it does not take /mnt/media2 for a child of /mnt/media.
func within(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !hasDotDotPrefix(rel)
}

func hasDotDotPrefix(rel string) bool {
	return len(rel) >= 3 && rel[:2] == ".." && rel[2] == filepath.Separator
}

func strictlyWithin(root, path string) bool {
	return within(root, path) && filepath.Clean(root) != filepath.Clean(path)
}
