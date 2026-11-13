// Package paths knows where mediaops keeps things on the NAS and refuses to
// work when the mount that should hold them is not there.
package paths

import (
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

	// StatDev reports a path's st_dev. Tests replace it, because a temp dir
	// shares a device with "/" on most CI machines; nil means the real one.
	StatDev func(path string) (uint64, error)
}

// FromEnv reads MEDIA_ROOT, defaulting to DefaultMediaRoot.
func FromEnv() Config {
	root := os.Getenv("MEDIA_ROOT")
	if root == "" {
		root = DefaultMediaRoot
	}
	return Config{MediaRoot: root}
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

	stagingDev, err := c.dev(staging)
	if err != nil {
		return fmt.Errorf("cannot read the device of the staging tree %s: %w", staging, err)
	}
	rootDev, err := c.dev("/")
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

func (c Config) dev(path string) (uint64, error) {
	if c.StatDev != nil {
		return c.StatDev(path)
	}
	var st unix.Stat_t
	if err := unix.Stat(path, &st); err != nil {
		return 0, err
	}
	return uint64(st.Dev), nil
}
