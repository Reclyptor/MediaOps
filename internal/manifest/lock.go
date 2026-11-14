package manifest

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"golang.org/x/sys/unix"

	"github.com/reclyptor/mediaops/internal/cli"
	"github.com/reclyptor/mediaops/kit/fsutil"
)

// lockPath is the lock file for m: one per absolute manifest path.
func lockPath(c Config, m *Manifest) string {
	dir := c.LockDir
	if dir == "" {
		dir = "/tmp"
	}
	sum := sha256.Sum256([]byte(m.Path))
	return filepath.Join(dir, "mkv-manifest-"+hex.EncodeToString(sum[:])[:16]+".lock")
}

// Lock takes m's exclusive lock without waiting, so two runs cannot apply the
// same manifest at once. The lock lives as long as release is not called or the
// process stays alive; the lock file itself is never removed.
func (c Config) Lock(m *Manifest) (release func() error, err error) {
	path := lockPath(c, m)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|unix.O_NOFOLLOW|unix.O_NONBLOCK, fsutil.FileMode)
	if err != nil {
		return nil, cli.Refused(fmt.Errorf("cannot create the lock file %s for %s: %w", path, m.Path, err))
	}
	if info, err := f.Stat(); err != nil || !info.Mode().IsRegular() {
		f.Close()
		if err != nil {
			return nil, cli.Refused(fmt.Errorf("cannot create the lock file %s for %s: %w", path, m.Path, err))
		}
		return nil, cli.Refused(fmt.Errorf("the lock file %s for %s is not a regular file (%s)", path, m.Path, kindOf(info.Mode())))
	}
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, unix.EWOULDBLOCK) {
			return nil, cli.Refused(fmt.Errorf("another exec --go holds %s (lock file %s)", m.Path, path))
		}
		return nil, cli.Refused(fmt.Errorf("cannot lock %s for %s: %w", path, m.Path, err))
	}
	var once sync.Once
	return func() error {
		var rerr error
		once.Do(func() {
			uerr := unix.Flock(int(f.Fd()), unix.LOCK_UN)
			cerr := f.Close()
			rerr = errors.Join(uerr, cerr)
		})
		return rerr
	}, nil
}
