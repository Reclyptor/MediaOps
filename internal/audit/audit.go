// Package audit is mkv-audit's core: read-only checks of the library against
// the anime-normalize rules, and of a manifest run against its manifest. The
// only child it starts is ffprobe, and it only ever opens regular files.
package audit

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"sync"

	"github.com/reclyptor/mediaops/internal/library"
	"github.com/reclyptor/mediaops/kit/paths"
	"github.com/reclyptor/mediaops/kit/proc"
)

// DefaultWorkers bounds the ffprobe calls in flight.
const DefaultWorkers = 8

// Deps is what mkv-audit touches outside itself.
type Deps struct {
	Paths  paths.Config
	Runner proc.Runner
}

// DefaultDeps is the real machine: MEDIA_ROOT from the environment and the
// real ffprobe.
func DefaultDeps() Deps {
	return Deps{Paths: paths.FromEnv(), Runner: &proc.Exec{}}
}

// Finding is one thing an audit objects to. Scope separates a series' own
// content from its Extras/ where a rule treats them apart, and Op is the
// 1-based manifest position for the manifest audits.
type Finding struct {
	Code   string `json:"code"`
	Path   string `json:"path"`
	Detail string `json:"detail"`
	Scope  string `json:"scope,omitempty"`
	Op     *int   `json:"op,omitempty"`
}

func opFinding(n int, code, path, detail string) Finding {
	return Finding{Code: code, Path: path, Detail: detail, Op: &n}
}

// forEach runs fn for every index below n on at most workers goroutines. It
// stops handing out work once ctx ends and returns the context's error.
func forEach(ctx context.Context, workers, n int, fn func(i int)) error {
	jobs := make(chan int)
	var wg sync.WaitGroup
	for range min(max(workers, 1), max(n, 1)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				fn(i)
			}
		}()
	}
dispatch:
	for i := range n {
		select {
		case jobs <- i:
		case <-ctx.Done():
			break dispatch
		}
	}
	close(jobs)
	wg.Wait()
	return ctx.Err()
}

// videoTree is the regular video files under a directory, sorted, and the
// video-named entries that were not regular (a FIFO, a socket, a symlink),
// which are named and never opened: opening a FIFO blocks until a writer
// appears.
type videoTree struct {
	files   []string
	skipped []string
}

// walkVideos lists dir without following symlinks.
func walkVideos(dir string) (videoTree, error) {
	var t videoTree
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		switch {
		case err != nil:
			return err
		case d.IsDir() || !library.IsVideo(d.Name()):
			return nil
		case d.Type().IsRegular():
			t.files = append(t.files, p)
		default:
			t.skipped = append(t.skipped, p)
		}
		return nil
	})
	return t, err
}

// subdirs lists the directories directly under dir by name, not following
// symlinks.
func subdirs(dir string) ([]os.DirEntry, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var dirs []os.DirEntry
	for _, e := range entries {
		if e.IsDir() {
			dirs = append(dirs, e)
		}
	}
	return dirs, nil
}
