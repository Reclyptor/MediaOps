package rip

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime/debug"
	"slices"
	"strings"
	"time"

	"github.com/reclyptor/mediaops/internal/drives"
	"github.com/reclyptor/mediaops/internal/health"
	"github.com/reclyptor/mediaops/internal/makemkv"
	"github.com/reclyptor/mediaops/internal/plan"
	"github.com/reclyptor/mediaops/internal/runstore"
	"github.com/reclyptor/mediaops/internal/verify"
	"github.com/reclyptor/mediaops/kit/fsutil"
	"github.com/reclyptor/mediaops/kit/proc"
)

const (
	partialSuffix = ".partial"
	infoLogName   = "info.log"
	ripLogName    = "rip.log"
)

// scanTimeoutError is the cause the watchdog cancels a worker with. Its text is
// the disc's failure reason.
type scanTimeoutError int

func (e scanTimeoutError) Error() string {
	return fmt.Sprintf("still scanning after %ds; killed", int(e))
}

// job is the controller's instructions to a worker: copies of what it needs,
// never the state itself.
type job struct {
	index   int
	runID   string
	entry   plan.Entry
	dest    string
	drive   drives.Drive
	started time.Time
	dir     string // the disc's directory in the run directory, disc-NN
	// earlier is the disc as the run it is being finalized from left it. Nil
	// for every other kind of run.
	earlier *runstore.Disc
}

func (j job) partial() string { return j.dest + partialSuffix }

// snapshot is what a worker has learned that the state should show.
type snapshot struct {
	progress    *runstore.Progress
	titles      runstore.TitleCount
	errors      []string
	copyStarted time.Time // zero until data starts flowing
}

// discEvent is a message from a worker to the loop. body is a snapshot, a
// ripExited or a result.
type discEvent struct {
	index int
	body  any
}

// ripExited tells the loop makemkvcon is done and asks for the suspect events
// recorded against the disc, which only the loop may read.
type ripExited struct {
	exit     int
	suspects chan<- []health.Event
}

// result is a worker's last word on its disc. interrupted means the run was
// stopped under it; the loop words the reason. stack is set when it panicked.
// identified is what an identify scan read off the disc.
type result struct {
	state       runstore.DiscState
	reason      string
	eject       *runstore.Eject
	identified  *runstore.Identified
	snap        snapshot
	interrupted bool
	stack       string
}

// progressKey is the part of makemkvcon's output a snapshot is sent for, so the
// tens of thousands of PRGV lines do not each cost an event.
type progressKey struct {
	phase   string
	percent float64
	saves   int
	errors  int
	copying bool
}

// worker takes one disc from the scanning slot to its verdict.
type worker struct {
	job
	ctx             context.Context
	runner          proc.Runner
	drives          Drives
	clock           Clock
	events          chan<- discEvent
	minTitleSeconds int

	snap snapshot
	sent progressKey
}

func (r *Run) runWorker(ctx context.Context, j job) {
	defer r.workers.Done()
	w := &worker{
		job: j, ctx: ctx, runner: r.deps.Runner, drives: r.deps.Drives, clock: r.deps.Clock,
		events: r.events, minTitleSeconds: r.plan.MinTitleSeconds,
	}
	r.events <- discEvent{j.index, w.guarded(r.mode)}
}

// guarded runs the mode's work, turning a panic into a failed disc so the slot
// is never stranded.
func (w *worker) guarded(m mode) (res result) {
	defer func() {
		if p := recover(); p != nil {
			res = result{
				state:  runstore.DiscFailed,
				reason: fmt.Sprintf("internal error: %v", p),
				snap:   w.snap,
				stack:  string(debug.Stack()),
			}
		}
	}()
	return m.work(w)
}

// rip is ripMode's work: the info pass gates the copy, the copy is verified,
// and a verified disc is promoted.
func (w *worker) rip() result {
	if err := mkdirAll(w.dir); err != nil {
		return w.failed(fmt.Sprintf("cannot create the disc directory: %v", err))
	}

	info, infoExit, err := w.infoPass(infoLogName)
	if w.stopped() {
		return w.failed("")
	}
	if err != nil {
		return w.failed(err.Error())
	}
	if problems := w.infoProblems(info, infoExit); len(problems) > 0 {
		// Copying hours of a disc that cannot be verified afterwards is the one
		// thing not worth doing.
		w.snap.errors = problems
		return w.failed(strings.Join(problems, "; ") + " (no rip was attempted)")
	}
	w.publish()

	partial := w.partial()
	if err := makePartial(partial); err != nil {
		return w.failed(err.Error())
	}
	rip := makemkv.NewParser()
	ripExit, err := w.stream(ripLogName, makemkv.RipArgv(w.drive.Dev, w.minTitleSeconds, partial), rip, func() { w.onRipLine(rip) })
	if w.stopped() {
		w.reportExit(ripExit)
		return w.failed("")
	}
	if err != nil && w.ctx.Err() == nil {
		return w.failed(fmt.Sprintf("makemkvcon could not be run to completion: %v (kept at %s)", err, partial))
	}
	suspects := w.reportExit(ripExit)
	return w.finish(finishInput{info: info, infoExit: infoExit, rip: rip, ripExit: ripExit, forced: w.scanTimeout(), suspects: suspects})
}

// infoPass runs the title info pass into the log named logName and reads the
// title count off it. A run that was stopped, or a scan the watchdog killed, is
// not an error here: the caller finds that out from the context.
func (w *worker) infoPass(logName string) (*makemkv.Parser, int, error) {
	info := makemkv.NewParser()
	exit, err := w.stream(logName, makemkv.InfoArgv(w.drive.Dev, w.minTitleSeconds), info, nil)
	w.snap.titles.Total = countTitles(info, w.minTitleSeconds)
	if err != nil && w.ctx.Err() == nil {
		return info, exit, fmt.Errorf("the title info pass could not run: %v", err)
	}
	return info, exit, nil
}

// infoProblems is everything wrong with an info pass, the watchdog's kill first.
func (w *worker) infoProblems(info *makemkv.Parser, exit int) []string {
	problems := verify.InfoProblems(info, exit)
	if forced := w.scanTimeout(); forced != "" {
		problems = append([]string{forced}, problems...)
	}
	return problems
}

// onRipLine runs after every makemkvcon line has been fed to the parser and
// sends a snapshot when anything worth showing changed.
func (w *worker) onRipLine(rip *makemkv.Parser) {
	if rip.Copying() && w.snap.copyStarted.IsZero() {
		w.snap.copyStarted = stamp(w.clock.Now())
	}
	pr := rip.Progress()
	key := progressKey{pr.Phase, pr.Percent, rip.SavesStarted(), len(rip.Errors()), rip.Copying()}
	if key == w.sent {
		return
	}
	w.sent = key
	w.snap.progress = &runstore.Progress{Phase: pr.Phase, Current: pr.Current, Total: pr.Max}
	// The step in flight is not finished, so it does not count as done yet.
	w.snap.titles.Done = max(rip.SavesStarted()-1, 0)
	w.snap.errors = rip.Errors()
	w.publish()
}

func (w *worker) publish() {
	s := w.snap
	s.errors = slices.Clone(s.errors)
	w.events <- discEvent{w.index, s}
}

// reportExit records makemkvcon's exit on the disc and returns the suspect
// events the loop holds for it.
func (w *worker) reportExit(exit int) []health.Event {
	reply := make(chan []health.Event, 1)
	w.events <- discEvent{w.index, ripExited{exit: exit, suspects: reply}}
	return <-reply
}

// stream runs argv, teeing every line into the log file as well as the parser.
// A log that could not be written is an error, whatever else the run did,
// because the evidence behind its verdict is incomplete.
func (w *worker) stream(logName string, argv []string, p *makemkv.Parser, after func()) (int, error) {
	f, err := openLog(filepath.Join(w.dir, logName))
	if err != nil {
		return -1, err
	}
	var writeErr error
	exit, runErr := w.runner.Stream(w.ctx, argv, func(line string) {
		if _, err := fmt.Fprintln(f, line); err != nil && writeErr == nil {
			writeErr = err
		}
		p.Feed(line)
		if after != nil {
			after()
		}
	})
	closeErr := f.Close()
	var logErr error
	if err := errors.Join(writeErr, closeErr); err != nil {
		logErr = fmt.Errorf("writing %s: %w", logName, err)
	}
	return exit, errors.Join(runErr, logErr)
}

// scanTimeout is why the watchdog killed this worker's scan, or "".
func (w *worker) scanTimeout() string {
	var t scanTimeoutError
	if errors.As(context.Cause(w.ctx), &t) {
		return t.Error()
	}
	return ""
}

// stopped reports whether the run was stopped under this worker, as opposed to
// the watchdog killing its scan.
func (w *worker) stopped() bool { return w.ctx.Err() != nil && w.scanTimeout() == "" }

// failed ends the disc with reason, or as interrupted when the run was stopped,
// whatever the proximate error was.
func (w *worker) failed(reason string) result {
	res := w.result(runstore.DiscFailed, reason)
	res.interrupted = w.stopped()
	return res
}

func (w *worker) result(state runstore.DiscState, reason string) result {
	snap := w.snap
	snap.errors = slices.Clone(snap.errors)
	return result{state: state, reason: reason, snap: snap}
}

func countTitles(info *makemkv.Parser, minSeconds int) int {
	n := 0
	for _, t := range info.Titles() {
		if secs, ok := t.Seconds(); ok && secs >= minSeconds {
			n++
		}
	}
	return n
}

func mkdirAll(dir string) error {
	if err := os.MkdirAll(dir, fsutil.DirMode); err != nil {
		return err
	}
	return os.Chmod(dir, fsutil.DirMode)
}

// makePartial creates the partial folder, which must not exist: a second
// writer in it would mix two rips.
func makePartial(partial string) error {
	if err := os.MkdirAll(filepath.Dir(partial), fsutil.DirMode); err != nil {
		return fmt.Errorf("cannot create the folder for %s: %w", partial, err)
	}
	if err := os.Mkdir(partial, fsutil.DirMode); err != nil {
		return fmt.Errorf("cannot create %s: %w", partial, err)
	}
	if err := os.Chmod(partial, fsutil.DirMode); err != nil {
		return fmt.Errorf("cannot set the mode of %s: %w", partial, err)
	}
	return nil
}

// openLog creates a log file. It never reuses one: a log is evidence.
func openLog(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, fsutil.FileMode)
	if err != nil {
		return nil, fmt.Errorf("creating %s: %w", path, err)
	}
	if err := f.Chmod(fsutil.FileMode); err != nil {
		f.Close()
		return nil, fmt.Errorf("setting the mode of %s: %w", path, err)
	}
	return f, nil
}
