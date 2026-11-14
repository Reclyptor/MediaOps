package manifest

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"sort"
)

// Assess decides what an op with a journal `begin` record but no `done` needs,
// from the real tree. It only looks: nothing is created, moved or removed, and
// a symlink is judged as the link it is, never the file it points at. It cannot
// fail; a tree it cannot read is a Conflict for the operator to see.
func Assess(op Op, st BeginState) Assessment {
	switch op.Kind {
	case KindRename, KindArchiveFile:
		return assessFileMove(op, st)
	case KindRenameDir, KindMoveDir, KindArchiveDir:
		return assessDirMove(op, st)
	case KindRemux:
		return assessRemux(op)
	case KindTitle, KindFlags, KindAudioLang, KindRmdirIfEmpty:
		return Assessment{Redo, fmt.Sprintf("%s is idempotent, so it runs again whatever state the tree is in", op.Kind)}
	}
	return conflictf("unknown op kind %q", op.Kind)
}

func conflictf(format string, args ...any) Assessment {
	return Assessment{Conflict, fmt.Sprintf(format, args...)}
}

// pathState is what is at a path, judged without following a symlink.
type pathState int

const (
	pathAbsent pathState = iota
	pathRegular
	pathDir
	pathOther
)

func (s pathState) present() bool { return s != pathAbsent }

func (s pathState) noun() string {
	switch s {
	case pathRegular:
		return "a file"
	case pathDir:
		return "a directory"
	case pathAbsent:
		return "nothing"
	}
	return "neither a file nor a directory"
}

func stat(path string) (pathState, os.FileInfo, error) {
	info, err := os.Lstat(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return pathAbsent, nil, nil
	case err != nil:
		return pathAbsent, nil, fmt.Errorf("reading %s: %w", path, err)
	case info.Mode().IsRegular():
		return pathRegular, info, nil
	case info.IsDir():
		return pathDir, info, nil
	}
	return pathOther, info, nil
}

func assessFileMove(op Op, st BeginState) Assessment {
	src, _, err := stat(op.Src)
	if err != nil {
		return conflictf("%v", err)
	}
	dst, dstInfo, err := stat(op.Dst)
	if err != nil {
		return conflictf("%v", err)
	}
	switch {
	case src == pathOther || src == pathDir:
		return conflictf("source %s is %s, not a regular file", op.Src, src.noun())
	case src == pathAbsent && dst == pathAbsent:
		return conflictf("neither source %s nor destination %s exists", op.Src, op.Dst)
	case src == pathAbsent && dst != pathRegular:
		return conflictf("source %s is gone and destination %s is %s, not a regular file", op.Src, op.Dst, dst.noun())
	case src == pathAbsent:
		if st.Size != nil && dstInfo.Size() != *st.Size {
			return conflictf("source %s is gone but destination %s is not whole: it is %d bytes, the source was %d", op.Src, op.Dst, dstInfo.Size(), *st.Size)
		}
		return Assessment{Settled, fmt.Sprintf("source %s is gone and destination %s is whole", op.Src, op.Dst)}
	case dst.present():
		return conflictf("source %s and destination %s both exist", op.Src, op.Dst)
	}
	return Assessment{Redo, fmt.Sprintf("source %s is whole and destination %s does not exist", op.Src, op.Dst)}
}

func assessDirMove(op Op, st BeginState) Assessment {
	src, _, err := stat(op.Src)
	if err != nil {
		return conflictf("%v", err)
	}
	dst, _, err := stat(op.Dst)
	if err != nil {
		return conflictf("%v", err)
	}
	switch {
	case src.present() && src != pathDir:
		return conflictf("source %s is %s, not a directory", op.Src, src.noun())
	case dst.present() && dst != pathDir:
		return conflictf("destination %s is %s, not a directory", op.Dst, dst.noun())
	case src == pathAbsent && dst == pathAbsent:
		return conflictf("neither source %s nor destination %s exists", op.Src, op.Dst)
	case src == pathAbsent:
		return assessSettledDir(op, st)
	case dst == pathAbsent:
		return Assessment{Redo, fmt.Sprintf("source %s is in place and destination %s does not exist", op.Src, op.Dst)}
	}
	return assessResumeDir(op)
}

func assessSettledDir(op Op, st BeginState) Assessment {
	got, err := treeManifest(op.Dst)
	if err != nil {
		return conflictf("%v", err)
	}
	if diff := firstTreeDifference(st.Tree, got); diff != "" {
		return conflictf("source %s is gone but destination %s is not the tree that was recorded: %s", op.Src, op.Dst, diff)
	}
	return Assessment{Settled, fmt.Sprintf("source %s is gone and destination %s holds the recorded tree", op.Src, op.Dst)}
}

func assessResumeDir(op Op) Assessment {
	srcTree, err := treeManifest(op.Src)
	if err != nil {
		return conflictf("%v", err)
	}
	dstTree, err := treeManifest(op.Dst)
	if err != nil {
		return conflictf("%v", err)
	}
	rels := make([]string, 0, len(srcTree))
	for rel := range srcTree {
		if _, both := dstTree[rel]; both {
			rels = append(rels, rel)
		}
	}
	if len(rels) > 0 {
		sort.Strings(rels)
		return conflictf("%s exists at both %s and %s", rels[0], op.Src, op.Dst)
	}
	return Assessment{Resume, fmt.Sprintf("part of %s has moved to %s; the %d file(s) left in the source move next", op.Src, op.Dst, len(srcTree))}
}

// firstTreeDifference describes the first relpath, in sorted order, where got
// departs from want, or "" when they are equal.
func firstTreeDifference(want, got map[string]int64) string {
	rels := make([]string, 0, len(want)+len(got))
	for rel := range want {
		rels = append(rels, rel)
	}
	for rel := range got {
		if _, dup := want[rel]; !dup {
			rels = append(rels, rel)
		}
	}
	sort.Strings(rels)
	for _, rel := range rels {
		w, inWant := want[rel]
		g, inGot := got[rel]
		switch {
		case inWant && !inGot:
			return fmt.Sprintf("%s is missing", rel)
		case !inWant:
			return fmt.Sprintf("%s was not recorded", rel)
		case w != g:
			return fmt.Sprintf("%s is %d bytes, recorded %d", rel, g, w)
		}
	}
	return ""
}

func assessRemux(op Op) Assessment {
	src, _, err := stat(op.Src)
	if err != nil {
		return conflictf("%v", err)
	}
	dst, _, err := stat(op.Dst)
	if err != nil {
		return conflictf("%v", err)
	}
	arc, _, err := stat(op.ArchiveDst)
	if err != nil {
		return conflictf("%v", err)
	}
	switch {
	case src.present() && src != pathRegular:
		return conflictf("source %s is %s, not a regular file", op.Src, src.noun())
	case src == pathAbsent && dst.present() && arc.present():
		return Assessment{Settled, fmt.Sprintf("source %s is gone and both %s and %s exist", op.Src, op.Dst, op.ArchiveDst)}
	case src == pathAbsent:
		return conflictf("source %s is gone but %s", op.Src, remuxMissing(op, dst, arc))
	case !dst.present() && !arc.present():
		return Assessment{Redo, fmt.Sprintf("source %s is in place and neither %s nor %s exists", op.Src, op.Dst, op.ArchiveDst)}
	case dst == pathRegular && !arc.present():
		return Assessment{Resume, fmt.Sprintf("%s is written and source %s is not archived to %s yet", op.Dst, op.Src, op.ArchiveDst)}
	case dst.present() && dst != pathRegular:
		return conflictf("destination %s is %s, not a regular file", op.Dst, dst.noun())
	}
	return conflictf("source %s, destination %s and archive %s are in a mix the tool will not resolve: %s", op.Src, op.Dst, op.ArchiveDst, remuxPresent(op, dst))
}

func remuxMissing(op Op, dst, arc pathState) string {
	switch {
	case !dst.present() && !arc.present():
		return fmt.Sprintf("neither destination %s nor archive %s exists", op.Dst, op.ArchiveDst)
	case !dst.present():
		return fmt.Sprintf("destination %s does not exist", op.Dst)
	}
	return fmt.Sprintf("archive %s does not exist", op.ArchiveDst)
}

func remuxPresent(op Op, dst pathState) string {
	if dst.present() {
		return fmt.Sprintf("%s and %s both exist while the source does", op.Dst, op.ArchiveDst)
	}
	return fmt.Sprintf("%s exists while the source does and %s does not", op.ArchiveDst, op.Dst)
}
