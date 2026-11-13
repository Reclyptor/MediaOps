package library

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"

	"github.com/reclyptor/mediaops/kit/paths"
	"github.com/reclyptor/mediaops/kit/proc"
)

// defaultWorkers is how many decode checks run at once.
const defaultWorkers = 12

// Deps is everything the movers touch outside themselves.
type Deps struct {
	Paths  paths.Config
	Runner proc.Runner
	Mover  Mover

	// FreeBytes is the space a writer can still use on the filesystem holding
	// path. Nil means statfs.
	FreeBytes func(path string) (uint64, error)

	// Workers is how many decode checks run at once. Zero means 12.
	Workers int
}

// DefaultDeps is the real machine: the NAS under MEDIA_ROOT, ARCHIVE_ROOT from
// the environment, and the real ffprobe and mkvpropedit.
func DefaultDeps() Deps {
	return Deps{Paths: paths.FromEnv(), Runner: &proc.Exec{}}
}

func (d Deps) workers() int {
	if d.Workers > 0 {
		return d.Workers
	}
	return defaultWorkers
}

func (d Deps) freeBytes(path string) (uint64, error) {
	if d.FreeBytes != nil {
		return d.FreeBytes(path)
	}
	var st unix.Statfs_t
	if err := unix.Statfs(path, &st); err != nil {
		return 0, err
	}
	return st.Bavail * uint64(st.Bsize), nil
}

// mediaPath resolves a manifest path, which is relative to MEDIA_ROOT, and
// refuses one that is absolute or that leaves it. The Python joined whatever it
// was given, so an absolute path silently replaced MEDIA_ROOT.
func (d Deps) mediaPath(rel string) (string, error) {
	switch {
	case rel == "":
		return "", fmt.Errorf("the path is empty")
	case filepath.IsAbs(rel):
		return "", fmt.Errorf("%s is absolute; manifest paths are relative to MEDIA_ROOT", rel)
	}
	abs := filepath.Join(d.Paths.MediaRoot, rel)
	if !strictlyWithin(d.Paths.MediaRoot, abs) {
		return "", fmt.Errorf("%s is outside MEDIA_ROOT", rel)
	}
	return abs, nil
}

// display is how a path is written back to the operator: relative to
// MEDIA_ROOT, as the manifest writes it, or absolute when it is outside it.
func (d Deps) display(path string) string {
	if strictlyWithin(d.Paths.MediaRoot, path) {
		rel, _ := filepath.Rel(d.Paths.MediaRoot, path)
		return rel
	}
	return path
}

// within reports whether path is root or lies below it. Unlike a string-prefix
// test it does not take /mnt/media2 for a child of /mnt/media.
func within(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func strictlyWithin(root, path string) bool {
	return within(root, path) && filepath.Clean(root) != filepath.Clean(path)
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

// exists reports whether anything, a dangling symlink included, is at path.
func exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}
