package library

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/reclyptor/mediaops/internal/cli"
	"github.com/reclyptor/mediaops/kit/fsutil"
)

// ArchiveMove is one file's journey into the archive.
type ArchiveMove struct {
	// Src is as the manifest wrote it, relative to MEDIA_ROOT; Dst is the
	// absolute archive path.
	Src   string `json:"src"`
	Dst   string `json:"dst"`
	Bytes int64  `json:"bytes"`
	// Copy is true when the move crosses a filesystem and so copies the bytes.
	Copy bool `json:"copy"`
	// Moved is set once the file is in the archive, verified, and gone from its
	// source.
	Moved bool `json:"moved"`

	src, dst string
}

// ArchiveReport is what mkv-archive prints.
type ArchiveReport struct {
	DryRun      bool          `json:"dry_run"`
	ArchiveRoot string        `json:"archive_root"`
	Files       int           `json:"files"`
	Bytes       int64         `json:"bytes"`
	CopiedBytes int64         `json:"copied_bytes"`
	Moves       []ArchiveMove `json:"moves"`
	Archived    int           `json:"archived"`
	RemovedDirs []string      `json:"removed_dirs"`
	Warnings    []string      `json:"warnings"`
	Error       string        `json:"error,omitempty"`
}

// ArchiveDst is where src goes in the archive: its path within its share,
// rooted at root. The share is MEDIA_ROOT's immediate child and is dropped, so
// <box>/<category>/X/Y.mkv archives to <root>/<category>/X/Y.mkv. A category
// lives on exactly one box, so the box name carries nothing the archive needs,
// and leaving it out means moving a category between boxes does not fork its
// archive into two trees.
//
// The share is taken from the path, not from a mount lookup, deliberately: the
// lookup answers a question about how the NAS exports a category, and that
// must not decide where a file lands.
func (d Deps) ArchiveDst(src, root string) (string, error) {
	rel, err := filepath.Rel(d.Paths.MediaRoot, src)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%s is outside MEDIA_ROOT, so it has no archive path", src)
	}
	parts := strings.Split(rel, string(filepath.Separator))
	if len(parts) < 2 {
		return "", fmt.Errorf("%s has no share component", rel)
	}
	return filepath.Join(append([]string{root}, parts[1:]...)...), nil
}

// PlanArchive runs every check that needs nothing written: each source is a
// file, each destination is free and unique, and the bytes that must be copied
// fit.
func (d Deps) PlanArchive(entries []ArchiveEntry) (ArchiveReport, error) {
	root, err := d.Paths.ArchiveRoot()
	if err != nil {
		return ArchiveReport{}, cli.Refused(err)
	}

	rep := ArchiveReport{ArchiveRoot: root, Moves: []ArchiveMove{}, RemovedDirs: []string{}, Warnings: []string{}}
	seen := map[string]bool{}
	var items []copyItem
	for i, e := range entries {
		src, err := d.mediaPath(e.Path)
		if err != nil {
			return rep, cli.Usage(fmt.Errorf("entry %d: %w", i, err))
		}
		info, ok := regularFile(src)
		if !ok {
			return rep, cli.Usage(fmt.Errorf("entry %d: not a file: %s", i, src))
		}
		dst, err := d.ArchiveDst(src, root)
		if err != nil {
			return rep, cli.Usage(fmt.Errorf("entry %d: %w", i, err))
		}
		if exists(dst) {
			return rep, cli.Refused(fmt.Errorf("entry %d: archive destination occupied: %s", i, dst))
		}
		if seen[dst] {
			return rep, cli.Usage(fmt.Errorf("entry %d: archive destination collision: %s", i, dst))
		}
		seen[dst] = true

		rep.Moves = append(rep.Moves, ArchiveMove{Src: d.display(src), Dst: dst, Bytes: info.Size(), src: src, dst: dst})
		items = append(items, copyItem{src: src, dir: existingDir(dst), bytes: info.Size()})
		rep.Bytes += info.Size()
	}

	copied, copies, err := d.copiedBytes(items)
	if err != nil {
		return rep, err
	}
	if err := d.checkSpace(items, copies); err != nil {
		return rep, err
	}
	for i := range rep.Moves {
		rep.Moves[i].Copy = copies[rep.Moves[i].src]
	}
	rep.Files, rep.CopiedBytes = len(rep.Moves), copied
	return rep, nil
}

// Archive plans the manifest and, when run is true, performs it: each file is
// moved, then proven to be in the archive at its size and gone from its
// source, and the directories the moves emptied are swept. With run false it
// touches nothing. A failure part-way returns the report with Error set and
// the moves done so far marked, because the files already moved are in the
// archive and the operator needs to know which.
func (d Deps) Archive(ctx context.Context, entries []ArchiveEntry, run bool) (ArchiveReport, error) {
	rep, err := d.PlanArchive(entries)
	if err != nil {
		return rep, err
	}
	rep.DryRun = !run
	if !run {
		return rep, nil
	}

	fail := func(err error) (ArchiveReport, error) {
		rep.Error = err.Error()
		return rep, err
	}
	for i := range rep.Moves {
		m := &rep.Moves[i]
		if err := ctx.Err(); err != nil {
			return fail(fmt.Errorf("stopped after %d of %d files: %w", rep.Archived, rep.Files, err))
		}
		if err := os.MkdirAll(filepath.Dir(m.dst), fsutil.DirMode); err != nil {
			return fail(fmt.Errorf("cannot create the archive directory for %s: %w", m.dst, err))
		}
		if err := d.Mover.Move(ctx, m.src, m.dst); err != nil {
			return fail(fmt.Errorf("archiving %s: %w", m.Src, err))
		}
		if err := verifyMoved(m.src, m.dst, m.Bytes); err != nil {
			return fail(err)
		}
		m.Moved = true
		rep.Archived++
	}

	srcs := make([]string, len(rep.Moves))
	for i, m := range rep.Moves {
		srcs[i] = m.src
	}
	removed, warnings := d.sweep(srcs)
	rep.RemovedDirs, rep.Warnings = append(rep.RemovedDirs, removed...), append(rep.Warnings, warnings...)
	return rep, nil
}

// verifyMoved checks what a move promised: the file is at dst with the bytes
// it started with, and is no longer at src.
func verifyMoved(src, dst string, want int64) error {
	info, ok := regularFile(dst)
	switch {
	case !ok:
		return fmt.Errorf("destination missing after the move: %s", dst)
	case info.Size() != want:
		return fmt.Errorf("size mismatch after the move: %s is %d bytes, want %d", dst, info.Size(), want)
	case exists(src):
		return fmt.Errorf("source still present after the move: %s", src)
	}
	return nil
}
