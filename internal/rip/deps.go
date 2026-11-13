// Package rip runs a rip: a controller that starts the planned discs on a
// stagger, a worker per disc that scans, copies, verifies and promotes it, and
// the durable record of both in the run directory.
package rip

import (
	"context"
	"errors"
	"io"
	"time"

	"github.com/reclyptor/mediaops/internal/drives"
	"github.com/reclyptor/mediaops/internal/runstore"
	"github.com/reclyptor/mediaops/kit/paths"
	"github.com/reclyptor/mediaops/kit/proc"
)

// Drives is the machine's optical drives. *drives.Host implements it.
type Drives interface {
	List(ctx context.Context) ([]drives.Drive, error)
	Eject(ctx context.Context, d drives.Drive) error
}

// Clock is the only source of time in a run: stagger, pauses, timeouts and
// every timestamp read it, so tests can run hours in milliseconds.
type Clock interface{ Now() time.Time }

// Deps is everything a run touches outside itself.
type Deps struct {
	Paths  paths.Config
	Drives Drives
	Runner proc.Runner
	Store  *runstore.Store
	Clock  Clock

	// LeasePath is the drive lease the run is alive by. A Run never takes it:
	// the controller process that drives it does, and holds it until the
	// terminal state is saved.
	LeasePath string
	// SysBlock is where the health watch looks for sr nodes.
	SysBlock string

	// Tick is how often the loop looks at the start gate, the scan watchdog and
	// the not_ready re-checks. The health intervals are how often each source is
	// polled. Zero means 1s, 1s and 5s. All are real time.
	Tick, HealthSysfsEvery, HealthDmesgEvery time.Duration

	ToolVersion string
	// Log receives controller.log lines. Nil discards them.
	Log io.Writer

	// Exe is the program re-executed as `__controller` to detach a run. It is
	// required by start, identify and finalize and has no default: a test binary
	// pointed at itself would run its own tests again.
	Exe string
	// LeaseWait is how long a controller retries for the lease, HandshakeTimeout
	// how long the foreground waits to hear from it, and PollEvery how often wait
	// looks at a run. All are real time. Zero means 5s, 30s and 5s.
	LeaseWait, HandshakeTimeout, PollEvery time.Duration
}

const (
	defaultTick             = time.Second
	defaultHealthSysfsEvery = time.Second
	defaultHealthDmesgEvery = 5 * time.Second
	defaultLeaseWait        = 5 * time.Second
	defaultHandshakeTimeout = 30 * time.Second
	defaultPollEvery        = 5 * time.Second
)

func (d Deps) withDefaults() Deps {
	if d.Tick == 0 {
		d.Tick = defaultTick
	}
	if d.HealthSysfsEvery == 0 {
		d.HealthSysfsEvery = defaultHealthSysfsEvery
	}
	if d.HealthDmesgEvery == 0 {
		d.HealthDmesgEvery = defaultHealthDmesgEvery
	}
	if d.LeaseWait == 0 {
		d.LeaseWait = defaultLeaseWait
	}
	if d.HandshakeTimeout == 0 {
		d.HandshakeTimeout = defaultHandshakeTimeout
	}
	if d.PollEvery == 0 {
		d.PollEvery = defaultPollEvery
	}
	if d.Log == nil {
		d.Log = io.Discard
	}
	return d
}

func (d Deps) validate() error {
	switch {
	case d.Drives == nil:
		return errors.New("rip: Deps.Drives is required")
	case d.Runner == nil:
		return errors.New("rip: Deps.Runner is required")
	case d.Store == nil:
		return errors.New("rip: Deps.Store is required")
	case d.Clock == nil:
		return errors.New("rip: Deps.Clock is required")
	}
	return nil
}
