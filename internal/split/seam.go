package split

import (
	"errors"
	"fmt"
	"slices"
)

// The outgoing part's last frames and the incoming part's first ones are
// looked up in the source's own decode by MD5. A short run can repeat (a held
// frame, a still title card), so an ambiguous match is retried with a longer
// one before it counts as a failure.
var (
	tailSizes = []int{48, 96, 120}
	headSizes = []int{24, 48}
)

// occurrences lists where seq appears as a contiguous run in window.
func occurrences(window, seq []string) []int {
	var at []int
	for i := 0; i+len(seq) <= len(window); i++ {
		if slices.Equal(window[i:i+len(seq)], seq) {
			at = append(at, i)
		}
	}
	return at
}

// alignEdge finds where the outgoing part's tail or the incoming part's head
// sits in window, trying each size in turn until exactly one place matches.
// It returns the window index of the first frame of the matched run and the
// run's length. A run found nowhere cannot be found by a longer one, so that
// ends the search.
func alignEdge(window, edge []string, sizes []int, tail bool) (start, n int, err error) {
	if len(edge) == 0 {
		return 0, 0, errors.New("the part has no decoded frames to align")
	}
	last := 0
	for _, size := range sizes {
		n = min(size, len(edge))
		if n == last {
			break
		}
		last = n
		run := edge[:n]
		if tail {
			run = edge[len(edge)-n:]
		}
		switch at := occurrences(window, run); len(at) {
		case 0:
			return 0, 0, fmt.Errorf("its %d frames are not in the source's decode around the cut", n)
		case 1:
			return at[0], n, nil
		}
	}
	return 0, 0, fmt.Errorf("its %d frames match the source's decode in more than one place", last)
}

// alignSeam locates the outgoing part's tail and the incoming part's head in
// the source's decode window and returns the source frames strictly between
// them: the frames the cut lost. An edge that cannot be placed uniquely, or a
// head that starts before the tail ends, is an error.
func alignSeam(window, tail, head []string) (lost []string, err error) {
	ts, tn, err := alignEdge(window, tail, tailSizes, true)
	if err != nil {
		return nil, fmt.Errorf("the outgoing part's tail cannot be aligned: %w", err)
	}
	hs, _, err := alignEdge(window, head, headSizes, false)
	if err != nil {
		return nil, fmt.Errorf("the incoming part's head cannot be aligned: %w", err)
	}
	tailEnd := ts + tn - 1
	if hs <= tailEnd {
		return nil, fmt.Errorf("the incoming part starts %d frames before the outgoing part ends: the cut repeats frames", tailEnd-hs+1)
	}
	return window[tailEnd+1 : hs], nil
}

// lostIdentical reports whether every lost frame equals held, the outgoing
// part's last frame. A held frame is invisible; a lost picture is not.
func lostIdentical(lost []string, held string) bool {
	return !slices.ContainsFunc(lost, func(m string) bool { return m != held })
}
