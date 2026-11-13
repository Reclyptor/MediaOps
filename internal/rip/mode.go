package rip

import (
	"cmp"
	"fmt"
	"os"

	"github.com/reclyptor/mediaops/internal/runstore"
)

// mode is what differs between the kinds of run that share the controller: how
// a disc is checked against the disk before it starts, and what its worker does
// once it has. The controller, gate, health watch and persistence are the same
// for all of them.
type mode interface {
	// preflightPaths returns why disc cannot start given what is already on
	// disk, or "" when nothing stands in its way.
	preflightPaths(disc *runstore.Disc) string
	// work runs the disc on its worker goroutine, from the scanning slot to its
	// verdict.
	work(w *worker) result
}

// ripMode copies each disc into <dest>.partial and promotes it once verified.
type ripMode struct{}

func (ripMode) preflightPaths(disc *runstore.Disc) string {
	return cmp.Or(
		occupied(disc.Dest, "destination already exists: %s"),
		occupied(disc.Dest+partialSuffix, "a .partial from an earlier run exists: %s"),
	)
}

func (ripMode) work(w *worker) result { return w.rip() }

// occupied is sentence, filled with path, when something is at path, and a
// sentence of its own when that cannot be told. "" means the path is free.
func occupied(path, sentence string) string {
	_, err := os.Lstat(path)
	switch {
	case err == nil:
		return fmt.Sprintf(sentence, path)
	case !os.IsNotExist(err):
		return fmt.Sprintf("cannot check %s: %v", path, err)
	}
	return ""
}

// identifyMode reads each drive's title table and nothing else: no destination,
// no copy, no eject.
type identifyMode struct{}

func (identifyMode) preflightPaths(*runstore.Disc) string { return "" }

func (identifyMode) work(w *worker) result { return w.identify() }

// finalizeMode re-verifies the copies of an earlier run's failed discs.
type finalizeMode struct{}

func (finalizeMode) preflightPaths(disc *runstore.Disc) string {
	return occupied(disc.Dest, "destination already exists: %s")
}

func (finalizeMode) work(w *worker) result { return w.finalize() }
