package inventory

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/reclyptor/mediaops/internal/library"
	"github.com/reclyptor/mediaops/kit/ffprobe"
	"github.com/reclyptor/mediaops/kit/paths"
	"github.com/reclyptor/mediaops/kit/proc"
)

// DefaultWorkers is how many files are probed at once.
const DefaultWorkers = 12

// Walk resolves the PATH arguments of mkv-probe into the files to probe and the
// entries it refused to touch, both sorted and without duplicates. A file
// argument is probed when it is a regular file; a directory is walked, sorted
// and without following symlinks, for regular files with a video extension.
// A non-regular entry inside a walked directory (a FIFO, device, socket or
// symlink) is skipped, and named only when its name is a video's, the one kind
// the probe would otherwise have opened; a symlinked .jpg is not worth a line.
// A non-regular PATH argument is always named. Nothing skipped is ever opened:
// opening a FIFO blocks until a writer appears.
func Walk(roots []string) (files, skipped []string, err error) {
	seenFile := map[string]bool{}
	seenSkip := map[string]bool{}
	for _, root := range roots {
		info, err := os.Stat(root)
		if err != nil {
			return nil, nil, err
		}
		if !info.IsDir() {
			if info.Mode().IsRegular() && !seenFile[root] {
				seenFile[root] = true
				files = append(files, root)
			} else if !info.Mode().IsRegular() && !seenSkip[root] {
				seenSkip[root] = true
				skipped = append(skipped, root)
			}
			continue
		}
		err = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			switch {
			case err != nil:
				return err
			case d.IsDir():
				return nil
			case d.Type().IsRegular():
				if library.IsVideo(d.Name()) && !seenFile[p] {
					seenFile[p] = true
					files = append(files, p)
				}
			case library.IsVideo(d.Name()) && !seenSkip[p]:
				seenSkip[p] = true
				skipped = append(skipped, p)
			}
			return nil
		})
		if err != nil {
			return nil, nil, err
		}
	}
	sort.Strings(files)
	sort.Strings(skipped)
	return files, skipped, nil
}

// Probe builds the inventory of the given absolute roots: walk them, probe every
// file with a pool of workers, and sort the records by path. A file ffprobe
// cannot read becomes an error record and the run goes on; it is the caller's
// to turn Errors into an exit status.
func Probe(ctx context.Context, r proc.Runner, cfg paths.Config, roots []string, workers int) (Inventory, error) {
	if workers < 1 {
		return Inventory{}, fmt.Errorf("workers must be at least 1, got %d", workers)
	}
	files, skipped, err := Walk(roots)
	if err != nil {
		return Inventory{}, fmt.Errorf("walking the roots: %w", err)
	}

	records := make([]Record, len(files))
	jobs := make(chan int)
	var wg sync.WaitGroup
	for range min(workers, max(len(files), 1)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				records[i] = probeOne(ctx, r, cfg, files[i])
			}
		}()
	}
	for i := range files {
		jobs <- i
	}
	close(jobs)
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return Inventory{}, err
	}

	inv := Inventory{Roots: roots, Probed: len(records), Skipped: skipped, Files: records}
	if inv.Skipped == nil {
		inv.Skipped = []string{}
	}
	if inv.Files == nil {
		inv.Files = []Record{}
	}
	for _, rec := range records {
		if rec.Error != "" {
			inv.Errors++
		}
	}
	return inv, nil
}

func probeOne(ctx context.Context, r proc.Runner, cfg paths.Config, path string) Record {
	info, err := ffprobe.Probe(ctx, r, path)
	if err != nil {
		return Record{Path: path, Error: err.Error()}
	}
	rec := Record{
		Path:     path,
		Rel:      relTo(cfg.MediaRoot, path),
		Size:     info.Format.Size,
		Duration: info.Format.Duration,
		Title:    nonEmpty(info.Format.Title),
		Chapters: make([]float64, len(info.Chapters)),
		Streams:  make([]Stream, len(info.Streams)),
	}
	for i, c := range info.Chapters {
		rec.Chapters[i] = c.Start
	}
	for i, s := range info.Streams {
		rec.Streams[i] = Stream{
			Index: s.Index, Type: s.CodecType, Codec: s.CodecName,
			Lang: nonEmpty(s.Tags["language"]), Title: nonEmpty(s.Tags["title"]),
			Default: s.Default, Channels: s.Channels, Width: s.Width, Height: s.Height,
		}
	}
	return rec
}

func nonEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// relTo is path relative to root, or nil when path is not inside it.
func relTo(root, path string) *string {
	if root == "" {
		return nil
	}
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return nil
	}
	return &rel
}
