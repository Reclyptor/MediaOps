// Package schedule decides when the controller may spin up another drive.
package schedule

import "time"

// Counts is how many discs are in each active state right now.
type Counts struct{ Scanning, Ripping int }

// Gate holds the brakes on starting a disc. All of them must be off:
//   - concurrency: at most MaxConcurrent discs active;
//   - scanning: one disc in spin-up and analysis at a time, since that phase
//     draws the power surge;
//   - the gap: Gap after the previous disc left scanning, so its copy ramp has
//     settled before the next surge;
//   - the health pause, after any USB error.
type Gate struct {
	MaxConcurrent int
	Gap           time.Duration

	openAt      time.Time
	pausedUntil time.Time
}

// MayStart reports whether another disc may start at now.
func (g Gate) MayStart(now time.Time, c Counts) bool {
	if c.Scanning+c.Ripping >= g.MaxConcurrent {
		return false
	}
	if c.Scanning > 0 {
		return false
	}
	if now.Before(g.openAt) {
		return false
	}
	return !now.Before(g.pausedUntil)
}

// ScanEnded starts the spin-up gap; call it when a disc leaves scanning.
func (g *Gate) ScanEnded(now time.Time) { g.openAt = now.Add(g.Gap) }

// Pause blocks new starts for d. A pause never shortens one already running.
func (g *Gate) Pause(now time.Time, d time.Duration) {
	if until := now.Add(d); until.After(g.pausedUntil) {
		g.pausedUntil = until
	}
}

// PausedUntil returns when the current pause ends, and false when none is in
// force at now.
func (g Gate) PausedUntil(now time.Time) (time.Time, bool) {
	if now.Before(g.pausedUntil) {
		return g.pausedUntil, true
	}
	return time.Time{}, false
}
