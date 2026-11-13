package rip

import (
	"time"

	"github.com/reclyptor/mediaops/internal/cli"
	"github.com/reclyptor/mediaops/internal/drives"
	"github.com/reclyptor/mediaops/internal/lease"
	"github.com/reclyptor/mediaops/internal/runstore"
	"github.com/reclyptor/mediaops/kit/paths"
	"github.com/reclyptor/mediaops/kit/proc"
)

// DefaultDeps is the real machine: the NAS under MEDIA_ROOT, the optical drives
// in /sys/class/block, and the drive lease in the pod's /tmp.
func DefaultDeps() Deps {
	cfg := paths.FromEnv()
	runner := &proc.Exec{}
	return Deps{
		Paths:       cfg,
		Drives:      drives.NewHost(runner),
		Runner:      runner,
		Store:       &runstore.Store{Root: cfg.RipsRoot(), Live: leaseLiveness{path: lease.DefaultPath}},
		Clock:       systemClock{},
		LeasePath:   lease.DefaultPath,
		SysBlock:    "/sys/class/block",
		ToolVersion: cli.Version,
		Exe:         "/proc/self/exe",
	}
}

type systemClock struct{}

func (systemClock) Now() time.Time { return time.Now() }

// leaseLiveness says a run is alive while the drive lease is held and names it.
// The pid inside the lease is never consulted: a pid can be reused, a held lock
// cannot.
type leaseLiveness struct{ path string }

func (l leaseLiveness) Alive(runID string) (bool, error) {
	st, err := lease.Probe(l.path)
	if err != nil {
		return false, err
	}
	return st.Held && st.Holder.RunID == runID, nil
}
