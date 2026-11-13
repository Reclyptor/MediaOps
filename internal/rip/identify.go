package rip

import (
	"context"
	"fmt"
	"strings"

	"github.com/reclyptor/mediaops/internal/makemkv"
	"github.com/reclyptor/mediaops/internal/plan"
	"github.com/reclyptor/mediaops/internal/runstore"
)

// identifyHealthPauseSeconds is plan's default pause after a USB event; an
// identify run has no plan to carry one.
const identifyHealthPauseSeconds = 120

// identifyRequest is what `identify` asks for, and the plan.json of its run.
type identifyRequest struct {
	Serials            []string `json:"serials"`
	MinTitleSeconds    int      `json:"min_title_seconds"`
	SpinupGapSeconds   int      `json:"spinup_gap_seconds"`
	ScanTimeoutSeconds int      `json:"scan_timeout_seconds"`
}

// newIdentifyRun creates an identify run over the requested drives, or over
// every drive on the bus when none are named. Drives that cannot be scanned
// become blocked discs once it executes. Drives are read one at a time.
func newIdentifyRun(ctx context.Context, d Deps, req identifyRequest, runID string) (*Run, error) {
	if err := d.validate(); err != nil {
		return nil, err
	}
	d = d.withDefaults()
	if req.Serials == nil {
		req.Serials = []string{}
	}
	serials := req.Serials
	if len(serials) == 0 {
		found, err := d.Drives.List(ctx)
		if err != nil {
			return nil, fmt.Errorf("listing the drives: %w", err)
		}
		for _, drive := range found {
			serials = append(serials, drive.Serial)
		}
	}

	st := newState(d, runID, runstore.KindIdentify)
	p := plan.Plan{
		MinTitleSeconds:    req.MinTitleSeconds,
		MaxConcurrent:      1,
		SpinupGapSeconds:   req.SpinupGapSeconds,
		HealthPauseSeconds: identifyHealthPauseSeconds,
		ScanTimeoutSeconds: req.ScanTimeoutSeconds,
	}
	for i, serial := range serials {
		st.Discs = append(st.Discs, runstore.Disc{Index: i, Serial: serial, Label: serial, State: runstore.DiscPending})
		p.Discs = append(p.Discs, plan.Entry{Serial: serial})
	}
	if err := createRun(d, st, req); err != nil {
		return nil, err
	}
	return newRun(d, p, st, identifyMode{}), nil
}

// identify is identifyMode's work: the title info pass, recorded as what the
// disc is. It never copies and never ejects.
func (w *worker) identify() result {
	if err := mkdirAll(w.dir); err != nil {
		return w.failed(fmt.Sprintf("cannot create the disc directory: %v", err))
	}
	info, exit, err := w.infoPass(infoLogName)
	if w.stopped() {
		return w.failed("")
	}
	if err != nil {
		return w.failed(err.Error())
	}
	if problems := w.infoProblems(info, exit); len(problems) > 0 {
		w.snap.errors = problems
		return w.failed(strings.Join(problems, "; "))
	}
	res := w.result(runstore.DiscDone, "")
	res.identified = identified(info)
	return res
}

func identified(info *makemkv.Parser) *runstore.Identified {
	disc := info.Disc()
	id := &runstore.Identified{Name: disc.Name, VolumeLabel: disc.VolumeLabel, Type: disc.Type}
	for _, t := range info.Titles() {
		row := runstore.IdentifiedTitle{
			ID:             t.ID,
			Duration:       t.Duration,
			Chapters:       positive(t.Chapters),
			SourcePlaylist: t.SourcePlaylist,
			SizeBytes:      positive(t.SizeBytes),
		}
		if secs, ok := t.Seconds(); ok {
			row.DurationSeconds = &secs
		}
		id.Titles = append(id.Titles, row)
	}
	return id
}
