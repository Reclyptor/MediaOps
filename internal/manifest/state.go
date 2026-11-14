package manifest

import "fmt"

// BeginState is what settling an interrupted op needs, recorded in its journal
// `begin` record: the source's size for a file move, the source tree's
// relpath-to-size manifest (regular files only) for a directory move.
type BeginState struct {
	Size *int64           `json:"size,omitempty"`
	Tree map[string]int64 `json:"tree,omitempty"`
}

// Outcome is what an interrupted op needs next.
type Outcome int

const (
	// Redo: nothing of the op happened; run it from the start.
	Redo Outcome = iota
	// Settled: the op's effect is complete; journal it done without running it.
	Settled
	// Resume: the op is part-way; finish it without redoing what is done.
	Resume
	// Conflict: the tree is in a state the tool will not resolve by itself.
	Conflict
)

func (o Outcome) String() string {
	switch o {
	case Redo:
		return "redo"
	case Settled:
		return "settled"
	case Resume:
		return "resume"
	case Conflict:
		return "conflict"
	}
	return fmt.Sprintf("outcome(%d)", int(o))
}

// MarshalText makes an Outcome print as its name in reports.
func (o Outcome) MarshalText() ([]byte, error) { return []byte(o.String()), nil }

// Assessment is an Outcome with the reason it was reached; a Conflict's reason
// names the paths involved.
type Assessment struct {
	Outcome Outcome
	Reason  string
}
