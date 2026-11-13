package library

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/reclyptor/mediaops/internal/cli"
)

// copyItem is a file that will be moved, and where its bytes would land if the
// move has to copy them.
type copyItem struct {
	src   string
	dir   string // an existing directory on the destination's filesystem
	bytes int64
}

// copiedBytes is how many bytes of items a move would copy across
// filesystems. A rename costs no space, so only a source on a different device
// from its destination counts.
func (d Deps) copiedBytes(items []copyItem) (int64, map[string]bool, error) {
	var total int64
	copies := map[string]bool{}
	for _, it := range items {
		srcDev, err := d.Paths.Dev(it.src)
		if err != nil {
			return 0, nil, fmt.Errorf("cannot read the device of %s: %w", it.src, err)
		}
		dstDev, err := d.Paths.Dev(it.dir)
		if err != nil {
			return 0, nil, fmt.Errorf("cannot read the device of %s: %w", it.dir, err)
		}
		if srcDev != dstDev {
			total += it.bytes
			copies[it.src] = true
		}
	}
	return total, copies, nil
}

// checkSpace refuses a batch whose cross-device copies would not fit. A rename
// consumed nothing, but a copy can fill the destination halfway through, so
// checking up front turns "the destination filled up on file 40 of 60" into a
// refusal before anything has moved. Copies are totalled per destination
// filesystem, each against the space free on it.
func (d Deps) checkSpace(items []copyItem, copies map[string]bool) error {
	type need struct {
		dir   string
		bytes int64
	}
	byDev := map[uint64]*need{}
	for _, it := range items {
		if !copies[it.src] {
			continue
		}
		dev, err := d.Paths.Dev(it.dir)
		if err != nil {
			return fmt.Errorf("cannot read the device of %s: %w", it.dir, err)
		}
		if byDev[dev] == nil {
			byDev[dev] = &need{dir: it.dir}
		}
		byDev[dev].bytes += it.bytes
	}
	for _, n := range byDev {
		free, err := d.freeBytes(n.dir)
		if err != nil {
			return fmt.Errorf("cannot read the free space of %s: %w", n.dir, err)
		}
		if uint64(n.bytes) > free {
			return cli.Refused(fmt.Errorf("%.2f GiB to copy into %s, %.2f GiB free", gib(n.bytes), n.dir, gib(int64(free))))
		}
	}
	return nil
}

// existingDir is the nearest ancestor of path that exists, which is where a
// file that does not exist yet will be written.
func existingDir(path string) string {
	p := filepath.Dir(path)
	for {
		if info, err := os.Stat(p); err == nil && info.IsDir() {
			return p
		}
		parent := filepath.Dir(p)
		if parent == p {
			return p
		}
		p = parent
	}
}

func gib(n int64) float64 { return float64(n) / (1 << 30) }
