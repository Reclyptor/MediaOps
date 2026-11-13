package rip

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/reclyptor/mediaops/internal/cli"
	"github.com/reclyptor/mediaops/internal/drives"
	"github.com/reclyptor/mediaops/internal/makemkv"
	"github.com/reclyptor/mediaops/internal/plan"
	"github.com/reclyptor/mediaops/internal/runstore"
)

// finalizeReport is what finalize would do, and what it leaves alone.
type finalizeReport struct {
	WouldFinalize []discRef     `json:"would_finalize"`
	Skipped       []skippedDisc `json:"skipped"`
}

type discRef struct {
	Index  int    `json:"index"`
	Serial string `json:"serial"`
}

type skippedDisc struct {
	Index  int    `json:"index"`
	Serial string `json:"serial"`
	Reason string `json:"reason"`
}

// finalizePlan sorts a finished run's failed discs into those a finalize can
// re-verify and those it leaves alone, with the reason for each of the rest. It
// reads only, so nothing is spun up for a disc that cannot be saved.
func finalizePlan(st *runstore.State, runDir string, found []drives.Drive) (finalizeReport, error) {
	byDrive := bySerial(found)
	report := finalizeReport{WouldFinalize: []discRef{}, Skipped: []skippedDisc{}}
	for _, disc := range st.Discs {
		if disc.State != runstore.DiscFailed {
			continue
		}
		ripLog := filepath.Join(discDir(runDir, disc.Index), ripLogName)
		why, err := whyNotFinalized(disc, ripLog, byDrive)
		if err != nil {
			return finalizeReport{}, fmt.Errorf("checking disc %d (%s): %w", disc.Index, disc.Serial, err)
		}
		if why != "" {
			report.Skipped = append(report.Skipped, skippedDisc{disc.Index, disc.Serial, why})
			continue
		}
		report.WouldFinalize = append(report.WouldFinalize, discRef{disc.Index, disc.Serial})
	}
	return report, nil
}

// whyNotFinalized is the first reason disc cannot be re-verified, or "". The
// error is for a path that could not be checked at all.
func whyNotFinalized(disc runstore.Disc, ripLog string, byDrive map[string]drives.Drive) (string, error) {
	if ok, err := isPresent(disc.Dest+partialSuffix, true); err != nil || !ok {
		return fmt.Sprintf("no %s%s to verify", disc.Dest, partialSuffix), err
	}
	if ok, err := isPresent(ripLog, false); err != nil || !ok {
		return fmt.Sprintf("the rip log %s is missing", ripLog), err
	}
	drive, ok := byDrive[disc.Serial]
	if !ok {
		return notOnBus(disc.Serial), nil
	}
	// A drive still becoming ready is not a reason; the run waits for it.
	if reason, _ := inspect(drive); reason != "" {
		return reason, nil
	}
	switch {
	case disc.MakemkvExit == nil:
		return "no exit status was recorded for the rip, so it cannot be told whether makemkvcon finished cleanly", nil
	case *disc.MakemkvExit != 0:
		return fmt.Sprintf("makemkvcon exited %d", *disc.MakemkvExit), nil
	}
	return "", nil
}

// isPresent reports whether path exists and is a directory (dir) or a file.
func isPresent(path string, dir bool) (bool, error) {
	info, err := os.Stat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return info.IsDir() == dir, nil
}

// planFinalize loads the run a finalize was asked for and works out what it
// would do with the drives as they are now.
func (d Deps) planFinalize(ctx context.Context, runID string) (*runstore.State, finalizeReport, error) {
	st, err := d.loadRun(runID)
	if err != nil {
		return nil, finalizeReport{}, err
	}
	if st.Kind != runstore.KindRip {
		return nil, finalizeReport{}, cli.Usage(fmt.Errorf("run %s is an %s run; finalize re-verifies rip runs", runID, st.Kind))
	}
	found, err := d.Drives.List(ctx)
	if err != nil {
		return nil, finalizeReport{}, fmt.Errorf("listing the drives: %w", err)
	}
	report, err := finalizePlan(st, d.Store.Dir(runID), found)
	return st, report, err
}

// newFinalizeRun reopens a finished run to retry its failed discs, one at a
// time. The run is `finalizing` from here on, and each retried disc is pending
// again; the discs that do not qualify are left exactly as they are.
func newFinalizeRun(ctx context.Context, d Deps, runID string) (*Run, error) {
	if err := d.validate(); err != nil {
		return nil, err
	}
	d = d.withDefaults()
	st, report, err := d.planFinalize(ctx, runID)
	if err != nil {
		return nil, err
	}
	if !st.State.Terminal() {
		return nil, cli.Refused(fmt.Errorf("run %s is still %s; finalize re-verifies finished runs only", runID, st.State))
	}
	if len(report.WouldFinalize) == 0 {
		return nil, &cli.Error{Code: cli.ExitIncomplete, Err: fmt.Errorf("no failed disc of run %s qualifies for finalize", runID)}
	}
	data, err := os.ReadFile(filepath.Join(d.Store.Dir(runID), planFile))
	if err != nil {
		return nil, fmt.Errorf("reading the plan of run %s: %w", runID, err)
	}
	var p plan.Plan
	if err := json.Unmarshal(data, &p); err != nil {
		return nil, fmt.Errorf("the plan of run %s is unreadable: %w", runID, err)
	}
	p.MaxConcurrent = 1

	st.State, st.Reason, st.FinishedAt = runstore.RunFinalizing, nil, nil
	st.Controller = runstore.Controller{PID: os.Getpid(), Alive: true}
	st.Health.PausedUntil, st.Health.PauseReason = nil, nil
	previous := make(map[int]runstore.Disc, len(report.WouldFinalize))
	for _, ref := range report.WouldFinalize {
		disc := &st.Discs[ref.Index]
		previous[ref.Index] = *disc
		disc.State, disc.Reason, disc.FinishedAt, disc.Progress = runstore.DiscPending, nil, nil, nil
	}
	if err := d.Store.Save(st); err != nil {
		return nil, fmt.Errorf("reopening run %s: %w", runID, err)
	}
	r := newRun(d, p, st, finalizeMode{})
	r.previous = previous
	return r, nil
}

// finalize is finalizeMode's work: the rip is already on disk, so its recorded
// log and a fresh info pass are all that is read again, and the same finish
// tail judges them.
func (w *worker) finalize() result {
	rip, err := parseLog(filepath.Join(w.dir, ripLogName))
	if err != nil {
		return w.failed(fmt.Sprintf("cannot read the rip log: %v", err))
	}
	logName, err := nextInfoLog(w.dir)
	if err != nil {
		return w.failed(err.Error())
	}
	info, infoExit, err := w.infoPass(logName)
	if w.stopped() {
		return w.failed("")
	}
	if err != nil {
		return w.failed(err.Error())
	}
	ripExit := *w.earlier.MakemkvExit
	suspects := w.reportExit(ripExit)
	return w.finish(finishInput{info: info, infoExit: infoExit, rip: rip, ripExit: ripExit, forced: w.scanTimeout(), suspects: suspects})
}

// parseLog replays a recorded makemkvcon log through a parser.
func parseLog(path string) (*makemkv.Parser, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	p := makemkv.NewParser()
	r := bufio.NewReader(f)
	for {
		line, err := r.ReadString('\n')
		if line != "" {
			p.Feed(strings.TrimRight(line, "\r\n"))
		}
		if errors.Is(err, io.EOF) {
			return p, nil
		}
		if err != nil {
			return nil, err
		}
	}
}

var finalizeInfoLog = regexp.MustCompile(`^finalize-(\d+)\.info\.log$`)

// nextInfoLog names this attempt's info log. Each attempt gets a file of its
// own, because the logs of the attempts before it are evidence.
func nextInfoLog(dir string) (string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", fmt.Errorf("cannot list %s: %w", dir, err)
	}
	last := 0
	for _, e := range entries {
		if m := finalizeInfoLog.FindStringSubmatch(e.Name()); m != nil {
			if n, err := strconv.Atoi(m[1]); err == nil {
				last = max(last, n)
			}
		}
	}
	return fmt.Sprintf("finalize-%d.info.log", last+1), nil
}
