package library

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"github.com/reclyptor/mediaops/internal/cli"
	"github.com/reclyptor/mediaops/kit/ffprobe"
	"github.com/reclyptor/mediaops/kit/fsutil"
	"github.com/reclyptor/mediaops/kit/mkvtool"
)

// maxUnaccounted is how many unaccounted videos an error lists.
const maxUnaccounted = 20

// PromoteMove is one file's journey into the library.
type PromoteMove struct {
	// Src and Dst are as the manifest wrote them, relative to MEDIA_ROOT.
	Src   string `json:"src"`
	Dst   string `json:"dst"`
	Title string `json:"title,omitempty"`
	Bytes int64  `json:"bytes"`
	// Moved is set once the file is at Dst with its original size.
	Moved bool `json:"moved"`

	src, dst string
}

// DecodeFailure is an .mkv that did not read back as a video after its move.
type DecodeFailure struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

// PromoteReport is what mkv-promote prints.
type PromoteReport struct {
	DryRun         bool            `json:"dry_run"`
	Files          int             `json:"files"`
	Bytes          int64           `json:"bytes"`
	CopiedBytes    int64           `json:"copied_bytes"`
	Moves          []PromoteMove   `json:"moves"`
	Kept           []string        `json:"kept"`
	Moved          int             `json:"moved"`
	DecodeChecked  int             `json:"decode_checked"`
	DecodeFailures []DecodeFailure `json:"decode_failures"`
	Titled         int             `json:"titled"`
	RemovedDirs    []string        `json:"removed_dirs"`
	Warnings       []string        `json:"warnings"`
	Error          string          `json:"error,omitempty"`

	sources []string
}

// PlanPromote runs accounting steps 1 to 3 of SPEC.md section 5, none of which
// writes anything:
//
//  1. no source is mapped twice, and no two entries share a destination;
//  2. every video file in every source directory is mapped or kept;
//  3. no destination is already occupied.
func (d Deps) PlanPromote(entries []PromoteEntry) (PromoteReport, error) {
	rep := PromoteReport{Moves: []PromoteMove{}, Kept: []string{}, DecodeFailures: []DecodeFailure{}, RemovedDirs: []string{}, Warnings: []string{}}

	kept := map[string]bool{}
	srcDirs := map[string]bool{}
	srcs := map[string]int{}
	dsts := map[string]int{}
	var items []copyItem
	for i, e := range entries {
		if e.Keep != "" {
			abs, err := d.mediaPath(e.Keep)
			if err != nil {
				return rep, cli.Usage(fmt.Errorf("entry %d: %w", i, err))
			}
			kept[abs] = true
			rep.Kept = append(rep.Kept, d.display(abs))
			continue
		}
		src, err := d.mediaPath(e.Src)
		if err != nil {
			return rep, cli.Usage(fmt.Errorf("entry %d: src: %w", i, err))
		}
		dst, err := d.mediaPath(e.Dst)
		if err != nil {
			return rep, cli.Usage(fmt.Errorf("entry %d: dst: %w", i, err))
		}
		info, ok := regularFile(src)
		if !ok {
			return rep, cli.Usage(fmt.Errorf("entry %d: source missing: %s", i, src))
		}
		if first, dup := srcs[src]; dup {
			return rep, cli.Usage(fmt.Errorf("entry %d: source mapped more than once (entry %d too): %s", i, first, src))
		}
		if first, dup := dsts[dst]; dup {
			return rep, cli.Usage(fmt.Errorf("entry %d: destination collision with entry %d: %s", i, first, dst))
		}
		srcs[src], dsts[dst] = i, i
		srcDirs[filepath.Dir(src)] = true

		rep.Moves = append(rep.Moves, PromoteMove{Src: d.display(src), Dst: d.display(dst), Title: e.Title, Bytes: info.Size(), src: src, dst: dst})
		rep.sources = append(rep.sources, src)
		items = append(items, copyItem{src: src, dir: existingDir(dst), bytes: info.Size()})
		rep.Bytes += info.Size()
	}

	for _, m := range rep.Moves {
		if kept[m.src] {
			return rep, cli.Usage(fmt.Errorf("entry %d: %s is both promoted and kept", srcs[m.src], m.src))
		}
	}
	for _, m := range rep.Moves {
		if exists(m.dst) {
			return rep, cli.Refused(fmt.Errorf("entry %d: destination already occupied: %s", dsts[m.dst], m.dst))
		}
	}
	if err := d.checkComplete(srcDirs, srcs, kept); err != nil {
		return rep, err
	}

	copied, copies, err := d.copiedBytes(items)
	if err != nil {
		return rep, err
	}
	if err := d.checkSpace(items, copies); err != nil {
		return rep, err
	}
	rep.Files, rep.CopiedBytes = len(rep.Moves), copied
	return rep, nil
}

// checkComplete is step 2: every video in every source directory must be
// accounted for, as a move or as a keep. An unmapped video is a hard error,
// never a silent skip, because a promotion that quietly leaves three episodes
// behind looks exactly like a successful one.
//
// Non-video is deliberately out of scope. A series' Extras legitimately carries
// soundtrack FLACs, booklet scans and cover art, and one real series holds
// 1,297 of them; demanding they be mapped turns a correct plan into a hard
// failure and pushes the operator toward a raw mv with no accounting at all,
// which is strictly worse. Video is what this tool moves, so video is what it
// accounts for.
func (d Deps) checkComplete(srcDirs map[string]bool, srcs map[string]int, kept map[string]bool) error {
	var missing []string
	for dir := range srcDirs {
		err := filepath.WalkDir(dir, func(path string, e fs.DirEntry, err error) error {
			switch {
			case err != nil:
				return err
			case e.IsDir() || !IsVideo(e.Name()):
				return nil
			}
			if _, mapped := srcs[path]; !mapped && !kept[path] {
				missing = append(missing, path)
			}
			return nil
		})
		if err != nil {
			return fmt.Errorf("cannot list the source directory %s: %w", dir, err)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	slices.Sort(missing)
	missing = slices.Compact(missing)
	shown := missing[:min(len(missing), maxUnaccounted)]
	for i, p := range shown {
		shown[i] = d.display(p)
	}
	return cli.Usage(fmt.Errorf("%d source VIDEO files unaccounted for:\n  %s", len(missing), strings.Join(shown, "\n  ")))
}

// Promote plans the manifest and, when run is true, performs steps 4 to 7:
//
//  4. every destination is proven creatable before the first move;
//  5. each file moves, and its destination is proven to have the source's size;
//  6. every moved .mkv is decode-checked with ffprobe;
//  7. the directories the moves emptied are swept.
//
// Container titles are set between 6 and 7. With run false nothing is touched.
// A failure after the first move returns the report with Error set, because the
// files already moved are in the library and the operator needs to know which.
func (d Deps) Promote(ctx context.Context, entries []PromoteEntry, run bool) (PromoteReport, error) {
	rep, err := d.PlanPromote(entries)
	if err != nil {
		return rep, err
	}
	rep.DryRun = !run
	if !run {
		return rep, nil
	}
	if err := d.touchValidate(rep.Moves); err != nil {
		return rep, cli.Refused(err)
	}

	fail := func(err error) (PromoteReport, error) {
		rep.Error = err.Error()
		return rep, err
	}
	for i := range rep.Moves {
		m := &rep.Moves[i]
		if err := ctx.Err(); err != nil {
			return fail(fmt.Errorf("stopped after %d of %d files: %w", rep.Moved, rep.Files, err))
		}
		if err := os.MkdirAll(filepath.Dir(m.dst), fsutil.DirMode); err != nil {
			return fail(fmt.Errorf("cannot create the directory for %s: %w", m.dst, err))
		}
		if err := d.Mover.Move(ctx, m.src, m.dst); err != nil {
			return fail(fmt.Errorf("promoting %s: %w", m.Src, err))
		}
		if err := verifyMoved(m.src, m.dst, m.Bytes); err != nil {
			return fail(err)
		}
		m.Moved = true
		rep.Moved++
	}

	var mkvs []*PromoteMove
	for i := range rep.Moves {
		if strings.EqualFold(filepath.Ext(rep.Moves[i].dst), ".mkv") {
			mkvs = append(mkvs, &rep.Moves[i])
		}
	}
	rep.DecodeChecked = len(mkvs)
	rep.DecodeFailures = d.decodeCheck(ctx, mkvs)
	if err := ctx.Err(); err != nil {
		return fail(fmt.Errorf("stopped during the decode check, after %d files moved: %w", rep.Moved, err))
	}
	if n := len(rep.DecodeFailures); n > 0 {
		err := &cli.Error{Code: cli.ExitIncomplete, Err: fmt.Errorf("%d of %d files failed the decode check", n, len(mkvs))}
		rep.Error = err.Error()
		return rep, err
	}

	for _, m := range mkvs {
		if m.Title == "" {
			continue
		}
		if err := mkvtool.SetTitle(ctx, d.Runner, m.dst, m.Title); err != nil {
			return fail(err)
		}
		rep.Titled++
	}

	removed, warnings := d.sweep(rep.sources)
	rep.RemovedDirs, rep.Warnings = append(rep.RemovedDirs, removed...), append(rep.Warnings, warnings...)
	return rep, nil
}

// touchValidate is step 4: it proves every destination can be created before
// anything moves. Unicode, length limits and permissions all fail here rather
// than halfway through a batch with the library in a split state. The empty
// files it makes are removed again; the directories stay, because the moves
// need them.
func (d Deps) touchValidate(moves []PromoteMove) error {
	var made []string
	defer func() {
		for _, p := range made {
			_ = removeProbe(p)
		}
	}()
	for _, m := range moves {
		if err := os.MkdirAll(filepath.Dir(m.dst), fsutil.DirMode); err != nil {
			return fmt.Errorf("destination not creatable: %w", err)
		}
		f, err := os.OpenFile(m.dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, fsutil.FileMode)
		if err != nil {
			return fmt.Errorf("destination not creatable: %w", err)
		}
		made = append(made, m.dst)
		if err := f.Close(); err != nil {
			return fmt.Errorf("destination not creatable: %w", err)
		}
	}
	return nil
}

// decodeCheck is step 6. A readable, positive duration is the evidence: this
// was once written as `ffprobe -f null -`, but -f null is an ffmpeg flag, so
// ffprobe rejected it on every file, which read like a warning and meant the
// check never ran. A checker that fails open is worse than no checker, so a
// non-zero exit or an unreadable duration is a failure. It returns the
// failures in manifest order.
func (d Deps) decodeCheck(ctx context.Context, mkvs []*PromoteMove) []DecodeFailure {
	reasons := make([]string, len(mkvs))
	sem := make(chan struct{}, d.workers())
	var wg sync.WaitGroup
	for i, m := range mkvs {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			if _, err := ffprobe.Duration(ctx, d.Runner, m.dst); err != nil {
				reasons[i] = err.Error()
			}
		}()
	}
	wg.Wait()

	failures := []DecodeFailure{}
	for i, r := range reasons {
		if r != "" {
			failures = append(failures, DecodeFailure{Path: mkvs[i].Dst, Reason: r})
		}
	}
	return failures
}
