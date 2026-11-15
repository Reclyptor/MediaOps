package cards

import (
	"context"

	"github.com/reclyptor/mediaops/kit/proc"
)

// matches reports whether a sample is inside the Y, U and V bounds.
func (p FindParams) matches(s Sample) bool {
	return p.MinY <= s.YAvg && s.YAvg <= p.MaxY &&
		p.MinU <= s.UAvg && s.UAvg <= p.MaxU &&
		p.MinV <= s.VAvg && s.VAvg <= p.MaxV
}

// Runs splits samples into the runs that qualify as a held card. A matching
// sample extends the current run, and with Static only while its Y is within
// Tol of the previous sample's. Anything else closes the run, which counts when
// it is at least MinRun long, and a matching sample starts the next one.
//
// Every qualifying run is returned, in time order: on a show whose palette is
// largely the card's own colour, scenery that matches the thresholds comes
// before the card, so the caller ranks the runs instead of taking the first.
func Runs(samples []Sample, p FindParams) [][]Sample {
	var runs [][]Sample
	var run []Sample
	for _, s := range samples {
		ok := p.matches(s)
		if ok && (len(run) == 0 || !p.Static || abs(s.YAvg-run[len(run)-1].YAvg) < p.Tol) {
			run = append(run, s)
			continue
		}
		if len(run) >= p.MinRun {
			runs = append(runs, run)
		}
		run = nil
		if ok {
			run = []Sample{s}
		}
	}
	if len(run) >= p.MinRun {
		runs = append(runs, run)
	}
	return runs
}

func abs(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}

// summarize turns qualifying runs into candidates and picks the best: the
// highest mean V, the first of equals.
func summarize(runs [][]Sample) (cands []FindCandidate, best *float64) {
	cands = []FindCandidate{}
	bestV, bestAt := 0.0, 0.0
	for _, run := range runs {
		var y, u, v float64
		for _, s := range run {
			y, u, v = y+s.YAvg, u+s.UAvg, v+s.VAvg
		}
		n := float64(len(run))
		c := FindCandidate{
			Start: run[0].T, End: run[len(run)-1].T, At: run[len(run)/2].T, Frames: len(run),
			YAvg: round3(y / n), UAvg: round3(u / n), VAvg: round3(v / n),
		}
		if best == nil || v/n > bestV {
			bestV, bestAt = v/n, c.At
			best = &bestAt
		}
		cands = append(cands, c)
	}
	return cands, best
}

// findFile decodes one file's window and reports its candidates. A failure that
// belongs to the file is in the result's Error; any other error is returned.
func findFile(ctx context.Context, r proc.Runner, path string, p FindParams) (FindFile, error) {
	rep := FindFile{Path: path, Window: [2]float64{p.Lo, p.Hi}}
	samples, err := sampleStats(ctx, r, path, p.Lo, p.Hi-p.Lo)
	if err == nil && len(samples) == 0 {
		err = failf("ffmpeg decoded no frames between %ss and %ss; the window may lie past the end of the file", fmtNum(p.Lo), fmtNum(p.Hi))
	}
	if err != nil {
		msg, fatal := failure(err)
		return FindFile{Path: path, Error: msg}, fatal
	}
	rep.Samples = len(samples)
	rep.Candidates, rep.Best = summarize(Runs(samples, p))
	return rep, nil
}
