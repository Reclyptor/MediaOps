package rip

import (
	"fmt"
	"time"

	"github.com/reclyptor/mediaops/internal/drives"
	"github.com/reclyptor/mediaops/internal/runstore"
)

const (
	requiredLinkMbps = 480
	// settleWindow is how long a drive may report not_ready, as it does for a few
	// seconds after a disc goes in, before it is judged unable to read the disc.
	settleWindow = 20 * time.Second
)

// inspect judges a drive on its own: a reason it cannot be used, or settling
// when it is still becoming ready and should be looked at again.
func inspect(d drives.Drive) (reason string, settling bool) {
	switch {
	case d.Status == drives.StatusNotReady:
		return "", true
	case d.Status == drives.StatusUnknown:
		return "cannot read the drive state: " + orUnknown(d.Error), false
	case !d.Loaded():
		return fmt.Sprintf("no disc in the drive (tray %s, state %s)", orUnknown(d.Tray), d.Status), false
	case d.LinkMbps == nil:
		return "the link speed of the drive cannot be read, so it is not known to be 480 Mbit/s: " + resetRunbook, false
	case *d.LinkMbps != requiredLinkMbps:
		return fmt.Sprintf("link is %d Mbit/s, not %d: %s", *d.LinkMbps, requiredLinkMbps, resetRunbook), false
	}
	return "", false
}

const resetRunbook = "the hub tree needs the reset runbook, which needs root on the node"

func orUnknown(s *string) string {
	if s == nil {
		return "unknown"
	}
	return *s
}

// preflight decides whether disc i may start, against a listing of the drives.
// A drive still not_ready is neither: the disc stays pending and the next tick
// asks again, until settleWindow has passed and it is blocked instead. Pending
// is how a disc waits without stopping the loop's health polling.
func (r *Run) preflight(i int, found map[string]drives.Drive) (drives.Drive, bool) {
	d, slot := r.disc(i), r.slots[i]
	drive, ok := found[d.Serial]
	if !ok {
		r.block(i, notOnBus(d.Serial))
		return drives.Drive{}, false
	}
	d.Drive = driveRef(drive)

	reason, settling := inspect(drive)
	switch {
	case !settling:
		slot.settleUntil = time.Time{}
	case slot.settleUntil.IsZero():
		slot.settleUntil = r.now().Add(settleWindow)
		return drive, false
	case r.now().Before(slot.settleUntil):
		return drive, false
	default:
		reason = fmt.Sprintf("the drive is still not ready after %s (tray %s, state %s)", settleWindow, orUnknown(drive.Tray), drive.Status)
	}
	if reason == "" {
		reason = r.mode.preflightPaths(d)
	}
	if reason != "" {
		r.block(i, reason)
		return drive, false
	}
	return drive, true
}

func (r *Run) block(i int, reason string) { r.abandon(i, runstore.DiscBlocked, reason) }

func notOnBus(serial string) string {
	return fmt.Sprintf("Drive %s is not on the USB bus; it has likely dropped off the hub and needs the hub reset (see the makemkv skill).", serial)
}

func bySerial(found []drives.Drive) map[string]drives.Drive {
	m := make(map[string]drives.Drive, len(found))
	for _, d := range found {
		m[d.Serial] = d
	}
	return m
}

func driveRef(d drives.Drive) *runstore.DriveRef {
	ref := runstore.DriveRef{Dev: d.Dev, USBPort: d.USBPort}
	if d.SG != nil {
		ref.SG = *d.SG
	}
	if d.LinkMbps != nil {
		ref.LinkMbps = *d.LinkMbps
	}
	return &ref
}
