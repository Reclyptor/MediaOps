package cards

import (
	"context"
	"fmt"
	"strings"

	"github.com/reclyptor/mediaops/kit/proc"
)

func (p FreezeParams) freezeFilter() string {
	return "freezedetect=n=" + fmtNum(p.DB) + "dB:d=" + fmtNum(p.MinDur)
}

// misses lists, in words, every bound the measured colour falls outside of.
// An empty result means the freeze is a candidate.
func (p FreezeParams) misses(ymax, u, v float64) []string {
	var out []string
	if v < p.MinV || v > p.MaxV {
		out = append(out, fmt.Sprintf("V %s is outside %s..%s", fmtNum(round3(v)), fmtNum(p.MinV), fmtNum(p.MaxV)))
	}
	if u < p.MinU || u > p.MaxU {
		out = append(out, fmt.Sprintf("U %s is outside %s..%s", fmtNum(round3(u)), fmtNum(p.MinU), fmtNum(p.MaxU)))
	}
	if ymax < p.MinYMax {
		out = append(out, fmt.Sprintf("YMAX %s is below %s", fmtNum(round3(ymax)), fmtNum(p.MinYMax)))
	}
	return out
}

// freezeFile finds the freezes in one file's window and judges the colour of
// each at its midpoint. A failure that belongs to the file is in the result's
// Error; any other error is returned.
func freezeFile(ctx context.Context, r proc.Runner, path string, p FreezeParams) (FreezeFile, error) {
	rep := FreezeFile{Path: path, Window: [2]float64{p.Lo, p.Hi}, Candidates: []FreezeCandidate{}, Rejected: []FreezeRejected{}}
	fail := func(err error) (FreezeFile, error) {
		msg, fatal := failure(err)
		return FreezeFile{Path: path, Error: msg}, fatal
	}

	var fp FreezeParser
	if err := stream(ctx, r, decodeArgv(path, p.Lo, p.Hi-p.Lo, p.freezeFilter()), fp.Feed); err != nil {
		return fail(err)
	}
	freezes, open, err := fp.Freezes()
	if err != nil {
		return fail(failf("reading freezedetect output: %v", err))
	}
	rep.Freezes = len(freezes)

	var bestV, bestAt float64
	for _, f := range freezes {
		start := round3(f.Start + p.Lo)
		at := round3(start + f.Duration/2)
		samples, err := sampleStats(ctx, r, path, at, 1)
		if err != nil {
			return fail(err)
		}
		if len(samples) == 0 {
			rep.Rejected = append(rep.Rejected, FreezeRejected{Start: start, Duration: round3(f.Duration), At: at, Reason: "no frame sampled"})
			continue
		}
		last := samples[len(samples)-1]
		if why := p.misses(last.YMax, last.UAvg, last.VAvg); len(why) > 0 {
			rep.Rejected = append(rep.Rejected, FreezeRejected{
				Start: start, Duration: round3(f.Duration), At: at,
				YMax: &last.YMax, UAvg: &last.UAvg, VAvg: &last.VAvg, Reason: strings.Join(why, "; "),
			})
			continue
		}
		rep.Candidates = append(rep.Candidates, FreezeCandidate{
			Start: start, Duration: round3(f.Duration), At: at,
			YMax: last.YMax, UAvg: last.UAvg, VAvg: last.VAvg,
		})
		if rep.Best == nil || last.VAvg > bestV {
			bestV, bestAt = last.VAvg, at
			rep.Best = &bestAt
		}
	}
	if open != nil {
		rep.Notes = append(rep.Notes, fmt.Sprintf(
			"a freeze starting at %ss was still running when the window ended at %ss, so it has no duration and was not measured; widen --hi to cover it",
			fmtNum(round3(*open+p.Lo)), fmtNum(p.Hi)))
	}
	return rep, nil
}
