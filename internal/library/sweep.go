package library

import (
	"errors"
	"path/filepath"
	"strings"
	"syscall"
)

// sweep removes the directories a move emptied, walking up from the directory
// each source lived in. It stops at the first directory that is not empty, and
// it never removes:
//   - the share the directory lives on, or anything above it. The bound is the
//     nearest mount point, so the sweep cannot climb out of the share, and a
//     sibling named like an extension of the share is not mistaken for it;
//   - the staging tree, which the rip tools require to exist and refuse to
//     create;
//   - a file. It uses rmdir only, so a path that turns out to name a file is
//     left alone.
//
// It returns the directories removed, and a warning for each directory it could
// not remove for a reason other than not being empty.
func (d Deps) sweep(sources []string) (removed, warnings []string) {
	for _, src := range sources {
		dir := filepath.Dir(src)
		bound := d.sweepBound(dir)
		for bound != "" && strictlyWithin(bound, dir) && dir != d.Paths.Staging() {
			err := rmdirEmpty(dir)
			switch {
			case err == nil:
				removed = append(removed, dir)
			case errors.Is(err, syscall.ENOENT):
				// already gone, so look at its parent
			case errors.Is(err, syscall.ENOTEMPTY), errors.Is(err, syscall.EEXIST):
				dir = bound
			default:
				warnings = append(warnings, "could not remove "+dir+": "+err.Error())
				dir = bound
			}
			dir = filepath.Dir(dir)
		}
	}
	return removed, warnings
}

// sweepBound is the directory a sweep starting at dir must stay strictly
// below: the share dir lives on. When no mount point lies between dir and "/",
// or the share found is not under MEDIA_ROOT, the share is taken to be
// MEDIA_ROOT's immediate child, as the archive destination does. It is empty
// when dir is not under MEDIA_ROOT at all, and then nothing is swept.
func (d Deps) sweepBound(dir string) string {
	media := d.Paths.MediaRoot
	if !strictlyWithin(media, dir) {
		return ""
	}
	if share := d.Paths.ShareRoot(dir); within(media, share) {
		return share
	}
	rel, _ := filepath.Rel(media, dir)
	first, _, _ := strings.Cut(rel, string(filepath.Separator))
	return filepath.Join(media, first)
}
