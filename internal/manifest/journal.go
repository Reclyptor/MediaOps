package manifest

// Journal is a manifest's progress as its journal file records it. A nil
// *Journal is a manifest that has never run.
type Journal struct {
	done  map[int]bool
	doubt map[int]BeginState
}

// Done reports whether op n was journaled done.
func (j *Journal) Done(n int) bool {
	return j != nil && j.done[n]
}

// InDoubt returns the state recorded when op n began, and whether op n began
// without ever finishing.
func (j *Journal) InDoubt(n int) (BeginState, bool) {
	if j == nil {
		return BeginState{}, false
	}
	st, ok := j.doubt[n]
	return st, ok
}

// DoneCount is how many ops are journaled done.
func (j *Journal) DoneCount() int {
	if j == nil {
		return 0
	}
	return len(j.done)
}
