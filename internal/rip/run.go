package rip

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"github.com/reclyptor/mediaops/internal/drives"
	"github.com/reclyptor/mediaops/internal/health"
	"github.com/reclyptor/mediaops/internal/plan"
	"github.com/reclyptor/mediaops/internal/runstore"
	"github.com/reclyptor/mediaops/internal/schedule"
	"github.com/reclyptor/mediaops/kit/fsutil"
)

const (
	planFile = "plan.json"
	// saveEvery bounds how often progress alone is written to state.json.
	saveEvery = time.Second

	stoppedBeforeStart = "run was stopped before this disc started"
	stoppedReason      = "stopped"
)

// Run is one rip run. Execute is the controller: its goroutine owns the state,
// the start gate and every timer, and is the only writer of state.json. Disc
// workers never touch the state; they talk to the loop through events.
type Run struct {
	id    string
	deps  Deps
	plan  plan.Plan
	mode  mode
	state *runstore.State
	gate  schedule.Gate
	slots []*slot

	events  chan discEvent
	workers sync.WaitGroup

	// previous holds, in a finalize run, each disc that was offered another
	// try as it was before. A disc that ends without a verdict on its copy goes
	// back to that record.
	previous map[int]runstore.Disc

	pauseReason string
	dirty       bool
	lastSave    time.Time
}

// slot is the loop's private bookkeeping for one disc.
type slot struct {
	cancel       context.CancelCauseFunc // stops the disc's worker while it runs
	scanStart    time.Time
	scanKilled   bool
	settleUntil  time.Time
	parserErrors []string // the worker's errors; the disc's also carry its suspect events
}

// NewRun creates the run directory with its immutable plan.json and a first
// state.json that says running. No drive is touched.
func NewRun(d Deps, p plan.Plan, runID string) (*Run, error) {
	if err := d.validate(); err != nil {
		return nil, err
	}
	d = d.withDefaults()
	staging := d.Paths.Staging()
	st := newState(d, runID, runstore.KindRip)
	for i, e := range p.Discs {
		st.Discs = append(st.Discs, runstore.Disc{
			Index: i, Serial: e.Serial, Label: e.Label(), Dest: e.Destination(staging), State: runstore.DiscPending,
		})
	}
	if err := createRun(d, st, p); err != nil {
		return nil, err
	}
	return newRun(d, p, st, ripMode{}), nil
}

// newState is the first state of a run this process controls.
func newState(d Deps, runID string, kind runstore.Kind) *runstore.State {
	return &runstore.State{
		RunID:       runID,
		Kind:        kind,
		State:       runstore.RunRunning,
		StartedAt:   stamp(d.Clock.Now()),
		ToolVersion: d.ToolVersion,
		Controller:  runstore.Controller{PID: os.Getpid(), Alive: true},
	}
}

// createRun makes the run directory with its first state and its immutable
// plan.json, which holds request. A failure leaves no directory behind.
func createRun(d Deps, st *runstore.State, request any) error {
	if err := d.Store.Create(st); err != nil {
		return fmt.Errorf("creating run %s: %w", st.RunID, err)
	}
	if err := fsutil.WriteJSON(filepath.Join(d.Store.Dir(st.RunID), planFile), request); err != nil {
		os.RemoveAll(d.Store.Dir(st.RunID))
		return fmt.Errorf("writing the plan of run %s: %w", st.RunID, err)
	}
	return nil
}

func newRun(d Deps, p plan.Plan, st *runstore.State, m mode) *Run {
	r := &Run{
		id:     st.RunID,
		deps:   d,
		plan:   p,
		mode:   m,
		state:  st,
		gate:   schedule.Gate{MaxConcurrent: p.MaxConcurrent, Gap: seconds(p.SpinupGapSeconds)},
		events: make(chan discEvent),
	}
	for range st.Discs {
		r.slots = append(r.slots, &slot{})
	}
	return r
}

// ID is the run's id.
func (r *Run) ID() string { return r.id }

// Execute runs the plan to a verdict and saves it before returning, so a caller
// that releases the drive lease afterwards never leaves a running state behind.
// Cancelling ctx stops the run: nothing new starts, makemkvcon is terminated,
// and the run concludes incomplete, "stopped". The error is for what the
// controller itself could not do, not for discs that failed.
func (r *Run) Execute(ctx context.Context) error {
	known, err := r.deps.Drives.List(ctx)
	if err != nil {
		err = fmt.Errorf("listing the drives: %w", err)
		reason := "the controller could not start: " + err.Error()
		r.failPending(reason)
		return errors.Join(err, r.conclude(reason))
	}

	healthCtx, stopHealth := context.WithCancel(ctx)
	updates := make(chan health.Update)
	watchDone := make(chan struct{})
	watch := health.New(known, r.deps.Runner, r.deps.SysBlock)
	go func() {
		defer close(watchDone)
		watch.Run(healthCtx, r.deps.HealthSysfsEvery, r.deps.HealthDmesgEvery, updates)
	}()

	r.logf("run %s started with %d disc(s)", r.id, len(r.state.Discs))
	found := bySerial(known)
	for i := range r.state.Discs {
		if r.disc(i).State == runstore.DiscPending {
			r.preflight(i, found)
		}
	}
	r.saveNow()

	ticker := time.NewTicker(r.deps.Tick)
	defer ticker.Stop()
	stopping := ctx.Done()
	for !r.finished() {
		select {
		case <-stopping:
			stopping = nil
			r.logf("stop requested; no further disc will start")
			r.failPending(stoppedBeforeStart)
		case <-ticker.C:
			r.tick(ctx)
		case u := <-updates:
			r.onHealth(u)
		case ev := <-r.events:
			r.onEvent(ev)
		}
	}
	stopHealth()
	<-watchDone
	r.workers.Wait()

	reason := ""
	if ctx.Err() != nil {
		reason = stoppedReason
	}
	return r.conclude(reason)
}

func (r *Run) disc(i int) *runstore.Disc { return &r.state.Discs[i] }

func (r *Run) finished() bool {
	return !slices.ContainsFunc(r.state.Discs, func(d runstore.Disc) bool { return !d.State.Terminal() })
}

// now is the clock's time; stamp is what gets recorded, to the second.
func (r *Run) now() time.Time { return r.deps.Clock.Now() }

func stamp(t time.Time) time.Time { return t.Truncate(time.Second) }

func seconds(n int) time.Duration { return time.Duration(n) * time.Second }

func (r *Run) tick(ctx context.Context) {
	now := r.now()
	if r.syncPause(now) {
		r.dirty = true
	}
	if ctx.Err() == nil {
		r.settlePending(ctx)
		r.killStuckScans(now)
		r.startNext(ctx)
	}
	r.saveIfDue(now)
}

// settlePending re-checks the pending discs that are waiting for their drive to
// become ready, so one whose drive never does is blocked at its deadline rather
// than when its turn comes.
func (r *Run) settlePending(ctx context.Context) {
	var waiting []int
	for i := range r.state.Discs {
		if r.disc(i).State == runstore.DiscPending && !r.slots[i].settleUntil.IsZero() {
			waiting = append(waiting, i)
		}
	}
	if len(waiting) == 0 {
		return
	}
	found, err := r.deps.Drives.List(ctx)
	if err != nil {
		return // startNext reports a listing failure against the disc it affects
	}
	byDrive := bySerial(found)
	for _, i := range waiting {
		r.preflight(i, byDrive)
	}
}

// startNext starts the first pending disc when the gate allows it and its drive
// is still fit, checked again because a tray can open between planning and
// spin-up.
func (r *Run) startNext(ctx context.Context) {
	i := slices.IndexFunc(r.state.Discs, func(d runstore.Disc) bool { return d.State == runstore.DiscPending })
	if i < 0 || !r.gate.MayStart(r.now(), r.counts()) {
		return
	}
	found, err := r.deps.Drives.List(ctx)
	if err != nil {
		r.block(i, fmt.Sprintf("cannot list the drives: %v", err))
		return
	}
	if drive, ok := r.preflight(i, bySerial(found)); ok {
		r.start(ctx, i, drive)
	}
}

func (r *Run) counts() schedule.Counts {
	var c schedule.Counts
	for _, d := range r.state.Discs {
		switch d.State {
		case runstore.DiscScanning:
			c.Scanning++
		case runstore.DiscRipping:
			c.Ripping++
		}
	}
	return c
}

func (r *Run) start(ctx context.Context, i int, drive drives.Drive) {
	d, slot := r.disc(i), r.slots[i]
	now := r.now()
	// A disc keeps the start of its first attempt, which a finalize retries.
	if d.StartedAt == nil {
		started := stamp(now)
		d.StartedAt = &started
	}
	workerCtx, cancel := context.WithCancelCause(ctx)
	slot.cancel, slot.scanStart = cancel, now
	r.transition(i, runstore.DiscScanning, "")

	j := job{
		index:   i,
		runID:   r.id,
		entry:   r.plan.Discs[i],
		dest:    d.Dest,
		drive:   drive,
		started: *d.StartedAt,
		dir:     discDir(r.deps.Store.Dir(r.id), i),
	}
	if prev, ok := r.previous[i]; ok {
		j.earlier = &prev
	}
	r.workers.Add(1)
	go r.runWorker(workerCtx, j)
}

// discDir is where a disc's logs live in its run directory.
func discDir(runDir string, index int) string {
	return filepath.Join(runDir, fmt.Sprintf("disc-%02d", index))
}

// killStuckScans cancels a disc that has been scanning too long. Left alone it
// would hold the single scanning slot and starve the rest of the run.
func (r *Run) killStuckScans(now time.Time) {
	limit := seconds(r.plan.ScanTimeoutSeconds)
	for i, slot := range r.slots {
		if r.disc(i).State != runstore.DiscScanning || slot.scanKilled || now.Sub(slot.scanStart) <= limit {
			continue
		}
		slot.scanKilled = true
		r.logf("disc %d (%s): still scanning after %ds; killing makemkvcon", i, r.disc(i).Serial, r.plan.ScanTimeoutSeconds)
		slot.cancel(scanTimeoutError(r.plan.ScanTimeoutSeconds))
	}
}

// transition moves a disc to a new state and persists it at once.
func (r *Run) transition(i int, to runstore.DiscState, reason string) {
	d, slot := r.disc(i), r.slots[i]
	now := r.now()
	from := d.State
	d.State = to
	d.Reason = nil
	if reason != "" {
		d.Reason = &reason
	}
	if to.Terminal() {
		finished := stamp(now)
		d.FinishedAt = &finished
		if slot.cancel != nil {
			slot.cancel(nil)
			slot.cancel = nil
		}
	}
	if from == runstore.DiscScanning && to != runstore.DiscScanning {
		r.gate.ScanEnded(now)
	}
	line := fmt.Sprintf("disc %d (%s): %s -> %s", i, d.Serial, from, to)
	if reason != "" {
		line += " -- " + reason
	}
	r.logf("%s", line)
	r.saveNow()
}

// abandon ends a disc with no verdict on its copy: it could not start, or the
// run was stopped under it. A finalize run puts the disc back as it was, since
// the failure it was offered another try for still stands; the reason is in the
// log.
func (r *Run) abandon(i int, to runstore.DiscState, reason string) {
	r.transition(i, to, reason)
	prev, ok := r.previous[i]
	if !ok {
		return
	}
	*r.disc(i) = prev
	r.logf("disc %d (%s): not finalised, so it keeps its earlier state", i, prev.Serial)
	r.saveNow()
}

// failPending fails every disc that has not started.
func (r *Run) failPending(reason string) {
	for i := range r.state.Discs {
		if r.disc(i).State == runstore.DiscPending {
			r.abandon(i, runstore.DiscFailed, reason)
		}
	}
}

func (r *Run) onHealth(u health.Update) {
	now := r.now()
	h := &r.state.Health
	h.Dmesg = u.Dmesg
	for _, ev := range u.Events {
		ev.Time = stamp(now)
		r.logf("health: %s on %s: %s", ev.Kind, ev.Port, ev.Detail)
		h.Events = append(h.Events, ev)
		for i := range r.state.Discs {
			d := r.disc(i)
			if d.State.Active() && d.Drive != nil && health.PortCovers(ev.Port, d.Drive.USBPort) {
				d.SuspectEvents = append(d.SuspectEvents, ev)
				r.refreshErrors(i)
			}
		}
		r.gate.Pause(now, seconds(r.plan.HealthPauseSeconds))
		r.pauseReason = ev.Detail
	}
	if len(u.Events) > 0 {
		r.saveNow()
		return
	}
	r.dirty = true
}

// syncPause mirrors the gate's pause into the state, which clears it once it
// has expired. It reports whether paused-ness changed.
func (r *Run) syncPause(now time.Time) bool {
	h := &r.state.Health
	wasPaused := h.PausedUntil != nil
	until, paused := r.gate.PausedUntil(now)
	if !paused {
		h.PausedUntil, h.PauseReason = nil, nil
		return wasPaused
	}
	until = stamp(until)
	reason := r.pauseReason
	h.PausedUntil, h.PauseReason = &until, &reason
	return !wasPaused
}

func (r *Run) onEvent(ev discEvent) {
	i := ev.index
	switch body := ev.body.(type) {
	case snapshot:
		r.applySnapshot(i, body)
		if !body.copyStarted.IsZero() && r.disc(i).State == runstore.DiscScanning {
			r.transition(i, runstore.DiscRipping, "")
		}
		r.dirty = true
	case ripExited:
		exit := body.exit
		r.disc(i).MakemkvExit = &exit
		body.suspects <- slices.Clone(r.disc(i).SuspectEvents)
		r.dirty = true
	case result:
		r.onResult(i, body)
	}
}

// applySnapshot copies what the worker reports onto the disc.
func (r *Run) applySnapshot(i int, s snapshot) {
	d := r.disc(i)
	d.Progress = s.progress
	d.Titles = s.titles
	r.slots[i].parserErrors = s.errors
	r.refreshErrors(i)
	if !s.copyStarted.IsZero() && d.CopyStartedAt == nil {
		d.CopyStartedAt = &s.copyStarted
	}
}

// refreshErrors sets a disc's errors to what its worker saw plus what the USB
// tree reported under its drive.
func (r *Run) refreshErrors(i int) {
	d := r.disc(i)
	errs := slices.Clone(r.slots[i].parserErrors)
	for _, ev := range d.SuspectEvents {
		errs = append(errs, ev.Detail)
	}
	d.Errors = errs
}

func (r *Run) onResult(i int, res result) {
	d := r.disc(i)
	if res.stack != "" {
		r.logf("disc %d (%s): worker panicked:\n%s", i, d.Serial, res.stack)
	}
	r.applySnapshot(i, res.snap)
	d.Eject = res.eject
	if id := res.identified; id != nil {
		d.Identified = id
		d.Label = cmp.Or(id.Name, id.VolumeLabel, d.Label)
	}

	if res.interrupted {
		r.abandon(i, runstore.DiscFailed, interruptedReason(d))
		return
	}
	r.transition(i, res.state, res.reason)
}

func interruptedReason(d *runstore.Disc) string {
	if d.State == runstore.DiscRipping {
		return fmt.Sprintf("run was stopped mid-rip (kept at %s%s)", d.Dest, partialSuffix)
	}
	return "run was stopped while this disc was still scanning"
}

// conclude derives the run's verdict from its discs and saves it. The terminal
// state is on disk when it returns.
func (r *Run) conclude(reason string) error {
	st := r.state
	st.Problems = runstore.Problems(st.Discs)
	if len(st.Problems) == 0 {
		st.State, st.Reason = runstore.RunComplete, nil
	} else {
		if reason == "" {
			reason = fmt.Sprintf("%d of %d disc(s) did not finish ok; see problems", len(st.Problems), len(st.Discs))
		}
		st.State, st.Reason = runstore.RunIncomplete, &reason
	}
	finished := stamp(r.now())
	st.FinishedAt = &finished
	st.Controller.Alive = false
	r.syncPause(r.now())
	r.logf("run %s %s", r.id, st.State)
	if err := r.deps.Store.Save(st); err != nil {
		return fmt.Errorf("saving the verdict of run %s: %w", r.id, err)
	}
	return nil
}

// saveNow writes state.json. A failed write is logged and retried by the next
// save: losing the NAS for a moment must not abandon a disc mid-copy.
func (r *Run) saveNow() {
	now := r.now()
	r.syncPause(now)
	if err := r.deps.Store.Save(r.state); err != nil {
		r.logf("could not save state.json: %v", err)
		return
	}
	r.dirty, r.lastSave = false, now
}

func (r *Run) saveIfDue(now time.Time) {
	if r.dirty && now.Sub(r.lastSave) >= saveEvery {
		r.saveNow()
	}
}
