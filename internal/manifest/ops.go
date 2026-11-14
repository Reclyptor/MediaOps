package manifest

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"

	"github.com/reclyptor/mediaops/internal/library"
	"github.com/reclyptor/mediaops/kit/fsutil"
	"github.com/reclyptor/mediaops/kit/mkvtool"
)

// executor performs ops and journals them. Every move goes through
// internal/library and every child through the Config's Runner; nothing here
// removes or renames anything itself.
type executor struct {
	c Config
	w *JournalWriter
}

// step is what a performed op reports.
type step struct {
	status string
	bytes  int64
	detail string
}

// perform runs one op to its read-back and journals it done. A Redo op is
// checked against the tree first, so a precondition that no longer holds
// fails before anything is begun, and then begun with the state settling it
// would need. A Settled or Resume op keeps the state its original begin
// recorded. st is that state, for ops that were in doubt.
func (x *executor) perform(ctx context.Context, op Op, a Assessment, st BeginState) (step, error) {
	if a.Outcome == Conflict {
		return step{}, fmt.Errorf("interrupted earlier and cannot be resumed: %s", a.Reason)
	}
	if a.Outcome == Redo {
		var err error
		if st, err = x.prepare(op); err != nil {
			return step{}, err
		}
		if err := x.w.Begin(op, st); err != nil {
			return step{}, &journalError{err}
		}
	}

	var (
		s   step
		err error
	)
	switch op.Kind {
	case KindRename, KindArchiveFile:
		s, err = x.moveFile(ctx, op, a, st)
	case KindRenameDir, KindMoveDir, KindArchiveDir:
		s, err = x.moveDir(ctx, op, a, st)
	case KindRemux:
		s, err = x.remux(ctx, op, a, st)
	case KindTitle:
		s, err = step{status: StatusDone}, mkvtool.SetTitle(ctx, x.c.Runner, op.Path, op.Title)
	case KindFlags:
		s, err = step{status: StatusDone}, mkvtool.SetDefaultFlags(ctx, x.c.Runner, op.Path, op.AudioDefault, op.SubDefault)
	case KindAudioLang:
		s, err = step{status: StatusDone}, mkvtool.SetAudioLanguages(ctx, x.c.Runner, op.Path, op.Langs)
	case KindRmdirIfEmpty:
		s, err = x.rmdir(op)
	default:
		err = fmt.Errorf("unknown op kind %q", op.Kind)
	}
	if err != nil {
		return step{}, err
	}

	switch a.Outcome {
	case Settled:
		s.status, s.detail = StatusSettled, joinDetail(a.Reason, s.detail)
	case Resume:
		s.detail = joinDetail("resumed: "+a.Reason, s.detail)
	}
	if err := x.w.Done(op.N, s.bytes, s.detail); err != nil {
		return step{}, &journalError{err}
	}
	return s, nil
}

func joinDetail(parts ...string) string {
	out := ""
	for _, p := range parts {
		switch {
		case p == "":
		case out == "":
			out = p
		default:
			out += "; " + p
		}
	}
	return out
}

// prepare holds a not-yet-begun op to what its kind needs of the tree and
// returns the state that settling it later needs: the source's size for a file
// move or remux, its tree for a directory move. Check ran moments ago; this is
// the same question asked again right before acting.
func (x *executor) prepare(op Op) (BeginState, error) {
	switch op.Kind {
	case KindRename, KindArchiveFile, KindRemux:
		size, err := requireFile(op.Src, "source")
		if err != nil {
			return BeginState{}, err
		}
		if op.Size != nil && *op.Size != size {
			return BeginState{}, fmt.Errorf("source %s is %d bytes, not the recorded %d", op.Src, size, *op.Size)
		}
		if err := requireAbsent(op.Dst, "destination"); err != nil {
			return BeginState{}, err
		}
		if op.Kind == KindRemux {
			if err := requireAbsent(op.ArchiveDst, "archive destination"); err != nil {
				return BeginState{}, err
			}
		}
		return BeginState{Size: &size}, nil
	case KindRenameDir, KindMoveDir, KindArchiveDir:
		if !realDir(op.Src) {
			return BeginState{}, fmt.Errorf("source %s is not a directory", op.Src)
		}
		if err := requireAbsent(op.Dst, "destination"); err != nil {
			return BeginState{}, err
		}
		tree, err := library.TreeManifest(op.Src)
		if err != nil {
			return BeginState{}, err
		}
		return BeginState{Tree: tree}, nil
	case KindTitle, KindFlags, KindAudioLang:
		if _, err := requireFile(op.Path, "target"); err != nil {
			return BeginState{}, err
		}
	}
	return BeginState{}, nil
}

// requireFile returns the size of the regular file at path, which must not be
// a symlink.
func requireFile(path, what string) (int64, error) {
	info, ok := regularFile(path)
	if !ok {
		return 0, fmt.Errorf("%s %s is not a regular file", what, path)
	}
	return info.Size(), nil
}

func requireAbsent(path, what string) error {
	if exists(path) {
		return fmt.Errorf("%s %s already exists", what, path)
	}
	return nil
}

func (x *executor) moveFile(ctx context.Context, op Op, a Assessment, st BeginState) (step, error) {
	if st.Size == nil {
		return step{}, errors.New("the journal recorded no source size for this op")
	}
	if a.Outcome == Redo {
		if err := x.move(ctx, op.Src, op.Dst); err != nil {
			return step{}, err
		}
	}
	if err := movedWhole(op.Src, op.Dst, *st.Size); err != nil {
		return step{}, err
	}
	if op.Title != "" {
		if err := mkvtool.SetTitle(ctx, x.c.Runner, op.Dst, op.Title); err != nil {
			return step{}, err
		}
	}
	return step{status: StatusDone, bytes: *st.Size}, nil
}

// move makes dst's directory and moves the file into it.
func (x *executor) move(ctx context.Context, src, dst string) error {
	if err := mkdirFor(dst); err != nil {
		return err
	}
	return x.c.Mover.Move(ctx, src, dst)
}

// mkdirFor creates the directory dst will live in.
func mkdirFor(dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), fsutil.DirMode); err != nil {
		return fmt.Errorf("creating the directory of %s: %w", dst, err)
	}
	return nil
}

// movedWhole is the read-back of a file move: dst is a regular file of the
// size the source had and the source is gone.
func movedWhole(src, dst string, size int64) error {
	got, err := requireFile(dst, "destination")
	if err != nil {
		return err
	}
	if got != size {
		return fmt.Errorf("destination %s is %d bytes after the move, the source was %d", dst, got, size)
	}
	if exists(src) {
		return fmt.Errorf("source %s still exists after the move", src)
	}
	return nil
}

func (x *executor) moveDir(ctx context.Context, op Op, a Assessment, st BeginState) (step, error) {
	switch a.Outcome {
	case Redo:
		if err := mkdirFor(op.Dst); err != nil {
			return step{}, err
		}
		if err := x.c.Mover.MoveTree(ctx, op.Src, op.Dst); err != nil {
			return step{}, err
		}
	case Resume:
		if err := x.c.Mover.ResumeTree(ctx, op.Src, op.Dst); err != nil {
			return step{}, err
		}
	}
	got, err := library.TreeManifest(op.Dst)
	if err != nil {
		return step{}, fmt.Errorf("reading back %s: %w", op.Dst, err)
	}
	if !maps.Equal(got, st.Tree) {
		diff := firstTreeDifference(st.Tree, got)
		return step{}, fmt.Errorf("%s does not hold the tree %s held: %s", op.Dst, op.Src, diff)
	}
	if exists(op.Src) {
		return step{}, fmt.Errorf("source %s still exists after the move", op.Src)
	}
	return step{status: StatusDone, bytes: sum(st.Tree)}, nil
}

func (x *executor) rmdir(op Op) (step, error) {
	removed, err := library.RemoveDirIfEmpty(op.Path)
	switch {
	case err != nil:
		return step{}, err
	case removed:
		return step{status: StatusDone}, nil
	case exists(op.Path):
		return step{status: StatusSkipped, detail: "not empty, left in place"}, nil
	}
	return step{status: StatusSkipped, detail: "already absent"}, nil
}
