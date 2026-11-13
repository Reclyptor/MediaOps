// Package paths knows where mediaops keeps things on the NAS and refuses to
// work when the mount that should hold them is not there.
package paths

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// DefaultMediaRoot is where the NAS is mounted in the pod.
const DefaultMediaRoot = "/mnt/media"

// Config locates the media tree. The zero value is not useful; use FromEnv or
// set MediaRoot.
type Config struct {
	MediaRoot string

	// Archive is ARCHIVE_ROOT as configured. It has no default, and it is not
	// to be used before ArchiveRoot has checked it.
	Archive string

	// StatDev reports a path's st_dev. Tests replace it, because a temp dir
	// shares a device with "/" on most CI machines; nil means the real one.
	StatDev func(path string) (uint64, error)
}

// FromEnv reads MEDIA_ROOT, defaulting to DefaultMediaRoot, and ARCHIVE_ROOT,
// which has no default.
func FromEnv() Config {
	root := os.Getenv("MEDIA_ROOT")
	if root == "" {
		root = DefaultMediaRoot
	}
	return Config{MediaRoot: root, Archive: os.Getenv("ARCHIVE_ROOT")}
}

// Staging is the tree rips land in before they are promoted.
func (c Config) Staging() string {
	return filepath.Join(c.MediaRoot, "dxp6800", "_pending")
}

// RipsRoot holds one directory per rip run.
func (c Config) RipsRoot() string {
	return filepath.Join(c.Staging(), ".rips")
}

// CheckStaging refuses to go on unless the staging tree is the NAS: it must
// exist, be a directory on a different filesystem from "/", and be writable and
// searchable. It never creates the tree, because creating it from the pod would
// land on the overlay and be discarded at the next image roll.
func (c Config) CheckStaging() error {
	staging := c.Staging()
	info, err := os.Stat(staging)
	switch {
	case os.IsNotExist(err):
		return fmt.Errorf("the staging tree does not exist: %s; create it on the NAS, because creating it from the pod would land on the overlay", staging)
	case err != nil:
		return fmt.Errorf("cannot stat the staging tree %s: %w", staging, err)
	case !info.IsDir():
		return fmt.Errorf("the staging path is not a directory: %s", staging)
	}

	stagingDev, err := c.Dev(staging)
	if err != nil {
		return fmt.Errorf("cannot read the device of the staging tree %s: %w", staging, err)
	}
	rootDev, err := c.Dev("/")
	if err != nil {
		return fmt.Errorf("cannot read the device of the root filesystem: %w", err)
	}
	if stagingDev == rootDev {
		return fmt.Errorf("%s is on the pod's root filesystem, so the NAS mount is missing", staging)
	}

	if err := unix.Access(staging, unix.W_OK|unix.X_OK); err != nil {
		return fmt.Errorf("the staging tree is not writable: %s: %w", staging, err)
	}
	return nil
}

// EnsureRipsRoot checks staging, then creates the rips directory under it.
func (c Config) EnsureRipsRoot() error {
	if err := c.CheckStaging(); err != nil {
		return err
	}
	if err := os.MkdirAll(c.RipsRoot(), 0o770); err != nil {
		return fmt.Errorf("cannot create the rips directory: %w", err)
	}
	return nil
}

// ArchiveRoot is the configured archive tree, checked to be real storage before
// anything is written to it. A missing or wrong value would land on the pod's
// overlay, which accepts the write, reports success and discards it at the next
// image roll, so the variable must be set, name a directory that already exists,
// sit on a different filesystem from "/" and be writable. It never creates the
// directory, because a mkdir would itself succeed on the overlay and look
// exactly like the correct outcome.
func (c Config) ArchiveRoot() (string, error) {
	if c.Archive == "" {
		return "", errors.New("ARCHIVE_ROOT is not set; it has no default on purpose, because a guessed archive path lands on the pod's overlay and is lost at the next redeploy")
	}
	root, err := filepath.Abs(c.Archive)
	if err != nil {
		return "", fmt.Errorf("cannot resolve ARCHIVE_ROOT %s: %w", c.Archive, err)
	}
	info, err := os.Stat(root)
	switch {
	case os.IsNotExist(err):
		return "", fmt.Errorf("ARCHIVE_ROOT does not exist: %s; create it on the NAS, because creating it from the pod would succeed on the overlay and look correct", root)
	case err != nil:
		return "", fmt.Errorf("cannot stat ARCHIVE_ROOT %s: %w", root, err)
	case !info.IsDir():
		return "", fmt.Errorf("ARCHIVE_ROOT is not a directory: %s", root)
	}

	rootDev, err := c.Dev(root)
	if err != nil {
		return "", fmt.Errorf("cannot read the device of ARCHIVE_ROOT %s: %w", root, err)
	}
	fsDev, err := c.Dev("/")
	if err != nil {
		return "", fmt.Errorf("cannot read the device of the root filesystem: %w", err)
	}
	if rootDev == fsDev {
		return "", fmt.Errorf("ARCHIVE_ROOT is on the pod's root filesystem: %s; that is the ephemeral overlay, so the mount is missing and anything written there is discarded at the next redeploy", root)
	}

	// An unexported child dataset is the other way this goes wrong, and it does
	// not announce itself: the NAS serves the parent's empty mountpoint stub, so
	// the path exists, sits on the right device, and is the wrong directory,
	// root-owned and not writable. A mount-point test would not catch it, since
	// the child exports that do work are not mount points on the client either.
	if err := unix.Access(root, unix.W_OK|unix.X_OK); err != nil {
		return "", fmt.Errorf("ARCHIVE_ROOT is not writable: %s: %w; if it is a child dataset on the NAS, check it is listed in /etc/exports, because an unexported child shows through the parent export as an empty, root-owned directory", root, err)
	}
	return root, nil
}

// ShareRoot is the share path lives on: its nearest enclosing mount point,
// or "/" when none is found. It exists to bound the sweeps that remove
// directories a move emptied, so they can never climb out of the share.
//
// MEDIA_ROOT is a plain directory with one mount per NAS beneath it, so the
// share has to be found from the path itself; that works whether MEDIA_ROOT
// holds one mount or twenty and needs no list of share names. The path need not
// exist any more, since a moved file's directory is the usual question.
func (c Config) ShareRoot(path string) string {
	p := filepath.Clean(path)
	if !filepath.IsAbs(p) {
		if abs, err := filepath.Abs(p); err == nil {
			p = abs
		}
	}
	for !c.isMount(p) {
		p = filepath.Dir(p)
	}
	return p
}

// isMount reports whether p is a mount point: "/", or a path whose device
// differs from its parent's. A path that cannot be statted is not one.
func (c Config) isMount(p string) bool {
	parent := filepath.Dir(p)
	if parent == p {
		return true
	}
	dev, err := c.Dev(p)
	if err != nil {
		return false
	}
	parentDev, err := c.Dev(parent)
	if err != nil {
		return false
	}
	return dev != parentDev
}

// Dev is the st_dev of path, or what StatDev says it is.
func (c Config) Dev(path string) (uint64, error) {
	if c.StatDev != nil {
		return c.StatDev(path)
	}
	var st unix.Stat_t
	if err := unix.Stat(path, &st); err != nil {
		return 0, err
	}
	return uint64(st.Dev), nil
}
