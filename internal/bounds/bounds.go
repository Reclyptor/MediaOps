// Package bounds finds the episode boundaries inside a MakeMKV Play-All track
// from its chapter marks. It is a port of the Python mkvlib detector, number
// for number, because the splits it recommends were checked against real discs
// and a "cleaner" scan would silently move them.
//
// Two earlier models failed and are worth recording:
//
//  1. Uniform k*D/N spacing broke on discs whose episodes differ in length.
//  2. A fixed "every M-th chapter" stride broke because chapters per episode is
//     not constant: Fate Apocrypha Disc 01 runs 5,4,4,4,4 chapters per episode.
//     That model also preferred 2-boundary solutions, since a single gap has
//     zero spread.
//
// The current model walks the chapter list greedily, hopping about E minutes at
// a time and snapping to the nearest chapter mark, and scores a scan of
// candidate episode lengths E by fit error. Every time is in minutes.
package bounds

import (
	"math"
)

// Result is the best episode layout Scan or Detect found.
type Result struct {
	// Boundaries are the start of each episode, the first being the first chapter.
	Boundaries []float64
	// EpisodeLen is the mean gap between boundaries.
	EpisodeLen float64
	// FitError is the mean distance of a snapped boundary from its target.
	FitError float64
	// Tail is the length of the final episode, which may carry credits or a bonus.
	Tail float64
}

// Episodes is the number of episodes the layout holds.
func (r Result) Episodes() int { return len(r.Boundaries) }

const (
	leadIn   = 3.0  // a longer lead-in means the track does not start at episode 1
	minStep  = 1.0  // a snapped boundary must clear the previous one by this much
	tailStop = 0.55 // a target this close to the end belongs to the final episode
	tailMax  = 1.9  // the final episode may run this many E long
	scanStep = 0.05

	defaultLo  = 9.0
	defaultHi  = 36.0
	defaultTol = 1.5
	hintLo     = 0.82
	hintHi     = 1.22
)

// walk hops from each boundary by about e and snaps to the nearest chapter,
// returning the layout and "" or nil and the rule that rejected e.
func walk(d float64, st []float64, e, tol float64) (Result, string) {
	if len(st) == 0 {
		return Result{}, "no chapters"
	}
	b := []float64{st[0]}
	if b[0] > leadIn {
		return Result{}, "lead-in"
	}
	errSum := 0.0
	for {
		target := b[len(b)-1] + e
		if target > d-tailStop*e {
			break // the remaining tail is the final episode
		}
		best := nearest(st, target)
		if math.Abs(best-target) > tol {
			return Result{}, "no chapter near target"
		}
		if best <= b[len(b)-1]+minStep {
			return Result{}, "boundary too close to the last"
		}
		errSum += math.Abs(best - target)
		b = append(b, best)
	}
	if len(b) < 2 {
		return Result{}, "fewer than two episodes"
	}
	gaps := make([]float64, len(b)-1)
	for i := range gaps {
		gaps[i] = b[i+1] - b[i]
	}
	tail := d - b[len(b)-1]
	for _, g := range gaps {
		if g < e-tol || g > e+tol {
			return Result{}, "gap off the stride"
		}
	}
	// the last segment may carry end credits or a bonus, so it gets a looser bound
	if tail < tailStop*e || tail > tailMax*e {
		return Result{}, "tail out of bounds"
	}
	sum := 0.0
	for _, g := range gaps {
		sum += g
	}
	return Result{
		Boundaries: b,
		EpisodeLen: sum / float64(len(gaps)),
		FitError:   errSum / float64(len(b)-1),
		Tail:       tail,
	}, ""
}

// nearest is the chapter closest to target; on a tie the earlier one, as
// Python's min does.
func nearest(st []float64, target float64) float64 {
	best := st[0]
	bestDist := math.Abs(best - target)
	for _, c := range st[1:] {
		if dist := math.Abs(c - target); dist < bestDist {
			best, bestDist = c, dist
		}
	}
	return best
}

// better orders candidates by (-episodes, fit error, boundaries, mean gap,
// tail), the boundaries compared element by element like a Python list.
func better(a, b Result) bool {
	if a.Episodes() != b.Episodes() {
		return a.Episodes() > b.Episodes()
	}
	if a.FitError != b.FitError {
		return a.FitError < b.FitError
	}
	for i := 0; i < len(a.Boundaries) && i < len(b.Boundaries); i++ {
		if a.Boundaries[i] != b.Boundaries[i] {
			return a.Boundaries[i] < b.Boundaries[i]
		}
	}
	if len(a.Boundaries) != len(b.Boundaries) {
		return len(a.Boundaries) < len(b.Boundaries)
	}
	if a.EpisodeLen != b.EpisodeLen {
		return a.EpisodeLen < b.EpisodeLen
	}
	return a.Tail < b.Tail
}

// Scan tries every episode length from lo to hi in steps of 0.05 minutes, the
// length accumulated by repeated addition from lo exactly as the Python did,
// and returns the best fit. d is the track's duration and st its chapter starts.
func Scan(d float64, st []float64, lo, hi, tol float64) (Result, bool) {
	var best Result
	found := false
	for e := lo; e <= hi; e += scanStep {
		r, reason := walk(d, st, e, tol)
		if reason != "" {
			continue
		}
		if !found || better(r, best) {
			best, found = r, true
		}
	}
	return best, found
}

// Detect finds the episode boundaries of a Play-All of d minutes whose chapters
// start at st. hint is the series' real per-episode runtime, and it matters:
// without one the scan happily locks onto HALF an episode. Anime discs put a
// chapter at the mid-episode eyecatch, so E=12 fits a 24-minute show just as
// tightly as E=24, and Sailor Moon Disc 01 came back as 16 episodes instead of
// 8 that way. A hint of zero scans 9 to 36 minutes and is only for tests that
// document the trap.
func Detect(d float64, st []float64, hint float64) (Result, bool) {
	lo, hi := defaultLo, defaultHi
	if hint != 0 {
		lo, hi = hint*hintLo, hint*hintHi
	}
	return Scan(d, st, lo, hi, defaultTol)
}

// Thresholds of Grade. Across 121 real tracks the fit error of genuine
// Play-Alls fell between 0.002 and 0.31 minutes, while bonus features that
// merely look periodic fell between 0.48 and 1.13.
const (
	AutoMaxErr      = 0.35
	AutoMaxLenDrift = 0.12
)

// Grade is "auto" or "review". "review" still yields a split, but it must be
// content-verified before it is run.
//
// A bonus track will still produce a confident-looking split: the Sailor Moon
// SuperS Special, one 3-segment featurette, came back as "2 episodes of 29.88m"
// and would have been cut into a 29.88m file plus a 17.68m orphan. Two
// independent tells catch it, the fit error and the detected episode length
// drifting from the published one.
//
// LIMIT: a set of cuts shifted by a CONSTANT is exactly as regular as the
// correct set, with the same stride, spread, fit error and episode length, so
// no statistic here can tell them apart. Demon Slayer S02 Disc 02 grades "auto"
// at fit error 0.226 and yet cuts 1.57 minutes inside an episode. Grade screens
// out tracks that are not Play-Alls; it cannot verify that a real Play-All is
// being cut in the right place. Only frames can.
func Grade(epLen, fitErr, hint float64) string {
	if fitErr > AutoMaxErr {
		return "review"
	}
	if hint != 0 && math.Abs(epLen-hint)/hint > AutoMaxLenDrift {
		return "review"
	}
	return "auto"
}

// SplitChapterIndices turns boundaries into the 1-based chapter numbers at
// which mkvmerge --split chapters: starts a new file, skipping the first. A
// chapter-number split is keyframe-exact; a timecode split is not. Each
// boundary must be one of st.
func SplitChapterIndices(st, b []float64) []int {
	out := make([]int, 0, len(b))
	for _, x := range b[1:] {
		for i, c := range st {
			if c == x {
				out = append(out, i+1)
				break
			}
		}
	}
	return out
}
