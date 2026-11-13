package runstore

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/reclyptor/mediaops/kit/fsutil"
)

const stateFile = "state.json"

// ErrNoRun means there is no run with that id: unknown, malformed, or not a
// schema-2 state.
var ErrNoRun = errors.New("no such run")

// Dead-controller verdict, persisted by the first reader that finds one.
const (
	controllerDiedReason     = "controller died"
	controllerDiedDiscReason = "The controller died before this disc finished."
)

var idPattern = regexp.MustCompile(`^\d{8}-\d{6}-[0-9a-f]{4}$`)

// Liveness says whether the controller of a run is still alive. The rip
// package backs it with the drive lease: held, and naming that run.
type Liveness interface {
	Alive(runID string) (bool, error)
}

// Store is the directory of runs under Root (the .rips directory).
type Store struct {
	Root string
	Live Liveness
	// Now stamps the verdict on a dead run; time.Now when nil.
	Now func() time.Time
}

// NewID makes a run id, YYYYMMDD-HHMMSS-xxxx, where xxxx is two random bytes
// in hex so runs started in the same second do not collide.
func NewID(now time.Time, rnd io.Reader) (string, error) {
	var b [2]byte
	if _, err := io.ReadFull(rnd, b[:]); err != nil {
		return "", fmt.Errorf("generating a run id: %w", err)
	}
	return now.Format("20060102-150405") + "-" + hex.EncodeToString(b[:]), nil
}

// Dir is the run's directory. The id is not checked here; Create, Save and
// Load refuse malformed ids before touching the disk.
func (s *Store) Dir(id string) string { return filepath.Join(s.Root, id) }

func checkID(id string) error {
	if !idPattern.MatchString(id) {
		return fmt.Errorf("%w: %q is not a run id", ErrNoRun, id)
	}
	return nil
}

// Create makes the run's directory and writes its first state. It refuses to
// reuse a directory, and a failed first write leaves nothing behind.
func (s *Store) Create(st *State) error {
	if err := checkID(st.RunID); err != nil {
		return err
	}
	dir := s.Dir(st.RunID)
	if err := os.Mkdir(dir, fsutil.DirMode); err != nil {
		return fmt.Errorf("creating run directory %s: %w", dir, err)
	}
	// Mkdir is subject to the umask; the mode is part of the contract.
	if err := os.Chmod(dir, fsutil.DirMode); err != nil {
		os.Remove(dir)
		return fmt.Errorf("setting the mode of %s: %w", dir, err)
	}
	if err := s.Save(st); err != nil {
		os.RemoveAll(dir)
		return err
	}
	return nil
}

// Save writes state.json atomically. It stamps the schema and turns nil slices
// into empty ones, so the file says [] where a reader expects a list, and keeps
// only the newest MaxEvents health events. st is updated to match what was
// written.
func (s *Store) Save(st *State) error {
	if err := checkID(st.RunID); err != nil {
		return err
	}
	st.Schema = Schema
	normalise(st)
	return fsutil.WriteJSON(filepath.Join(s.Dir(st.RunID), stateFile), st)
}

func normalise(st *State) {
	st.Problems = orEmpty(st.Problems)
	st.Discs = orEmpty(st.Discs)
	st.Health.Events = orEmpty(st.Health.Events)
	if n := len(st.Health.Events); n > MaxEvents {
		st.Health.Events = slices.Clone(st.Health.Events[n-MaxEvents:])
	}
	for i := range st.Discs {
		d := &st.Discs[i]
		d.Errors = orEmpty(d.Errors)
		d.SuspectEvents = orEmpty(d.SuspectEvents)
		if d.Identified != nil {
			d.Identified.Titles = orEmpty(d.Identified.Titles)
		}
	}
}

func orEmpty[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

// Load reads a run's state. Controller.Alive is filled in from the liveness
// source, and a run that is still running or finalizing but whose controller is
// gone is concluded: incomplete, "controller died", its unfinished discs failed.
// That verdict is persisted, so it outlives this call.
func (s *Store) Load(id string) (*State, error) {
	if err := checkID(id); err != nil {
		return nil, err
	}
	// Probe before reading. A controller that finishes between the two writes its
	// verdict before it lets go of the lease, so a dead probe followed by a read
	// sees either that verdict or a genuinely dead run, never a stale "running".
	alive, err := s.Live.Alive(id)
	if err != nil {
		return nil, fmt.Errorf("checking whether run %s is still alive: %w", id, err)
	}
	st, err := s.read(id)
	if err != nil {
		return nil, err
	}
	st.Controller.Alive = alive
	if !st.State.Terminal() && !alive {
		s.concludeDead(st)
		if err := s.Save(st); err != nil {
			return nil, fmt.Errorf("recording that the controller of run %s died: %w", id, err)
		}
	}
	return st, nil
}

func (s *Store) read(id string) (*State, error) {
	path := filepath.Join(s.Dir(id), stateFile)
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("%w: %s", ErrNoRun, id)
	}
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	// The schema is checked on its own first, so a state in any other shape is
	// "not a run" instead of a type error about whichever field differs.
	var head struct{ Schema int }
	if err := json.Unmarshal(data, &head); err != nil || head.Schema != Schema {
		return nil, fmt.Errorf("%w: %s has no schema %d state", ErrNoRun, id, Schema)
	}
	var st State
	if err := json.Unmarshal(data, &st); err != nil {
		return nil, fmt.Errorf("%w: %s is unreadable: %v", ErrNoRun, path, err)
	}
	if st.RunID != id {
		return nil, fmt.Errorf("%s names run %q, not %q", path, st.RunID, id)
	}
	return &st, nil
}

func (s *Store) concludeDead(st *State) {
	now := time.Now()
	if s.Now != nil {
		now = s.Now()
	}
	reason := controllerDiedReason
	st.State = RunIncomplete
	st.Reason = &reason
	if st.FinishedAt == nil {
		st.FinishedAt = &now
	}
	for i := range st.Discs {
		d := &st.Discs[i]
		if d.State.Terminal() {
			continue
		}
		why := controllerDiedDiscReason
		d.State = DiscFailed
		d.Reason = &why
		if d.FinishedAt == nil {
			d.FinishedAt = &now
		}
	}
	st.Problems = Problems(st.Discs)
}

// List summarises every run, newest first. Only directories holding a schema-2
// state count: Python-era runs, aborted-*, discinfo and stray files are
// ignored.
func (s *Store) List() ([]Summary, error) {
	entries, err := os.ReadDir(s.Root)
	if errors.Is(err, os.ErrNotExist) {
		return []Summary{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("listing runs in %s: %w", s.Root, err)
	}
	runs := []Summary{}
	for _, e := range entries {
		if !e.IsDir() || checkID(e.Name()) != nil {
			continue
		}
		st, err := s.Load(e.Name())
		if errors.Is(err, ErrNoRun) {
			continue
		}
		if err != nil {
			return nil, err
		}
		runs = append(runs, Summary{
			RunID: st.RunID, Kind: st.Kind, State: st.State,
			StartedAt: st.StartedAt, FinishedAt: st.FinishedAt,
		})
	}
	slices.SortFunc(runs, func(a, b Summary) int {
		if c := b.StartedAt.Compare(a.StartedAt); c != 0 {
			return c
		}
		return strings.Compare(b.RunID, a.RunID)
	})
	return runs, nil
}

// Problems lists every disc that did not end done.
func Problems(discs []Disc) []Problem {
	problems := []Problem{}
	for _, d := range discs {
		if d.State == DiscDone {
			continue
		}
		p := Problem{Index: d.Index, Serial: d.Serial, Label: d.Label, State: d.State}
		if d.Reason != nil {
			p.Reason = *d.Reason
		}
		problems = append(problems, p)
	}
	return problems
}
