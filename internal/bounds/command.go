package bounds

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"

	"github.com/reclyptor/mediaops/internal/cli"
	"github.com/reclyptor/mediaops/kit/ffprobe"
	"github.com/reclyptor/mediaops/kit/proc"
)

const usage = "usage: mkv-bounds FILE EPISODE_MINUTES | --version"

// limitNote is printed with every pattern, because the limit it states is the
// one the detector cannot remove.
const limitNote = "A constant offset is invisible to every statistic here: cuts shifted by the " +
	"same amount have the same stride, spread, fit error and episode length as the correct ones. " +
	"Verify one interior boundary against the frames before splitting. Roughly a third of " +
	"checked tracks were anchored wrong."

// Report is mkv-bounds' one JSON value. Durations and boundaries are seconds;
// the detector's own figures are minutes.
type Report struct {
	File        string  `json:"file"`
	Duration    float64 `json:"duration"`
	Chapters    int     `json:"chapters"`
	HintMinutes float64 `json:"hint_minutes"`
	Pattern     bool    `json:"pattern"`
	Reason      string  `json:"reason,omitempty"`
	*Found
}

// Found is the part of a Report that exists only when a pattern was found.
type Found struct {
	Episodes        int       `json:"episodes"`
	EpisodeMinutes  float64   `json:"episode_minutes"`
	FitErrorMinutes float64   `json:"fit_error_minutes"`
	LastMinutes     float64   `json:"last_minutes"`
	Grade           string    `json:"grade"`
	Boundaries      []float64 `json:"boundaries"`
	SplitChapters   []int     `json:"split_chapters"`
	Split           string    `json:"split"`
	Limit           string    `json:"limit"`
}

// Command is the mkv-bounds body: args are what follows the program name. It
// prints one JSON report and exits 1 when there is no chapter pattern.
func Command(ctx context.Context, r proc.Runner, args []string, streams cli.IO) error {
	rest, err := cli.ParseFlags(cli.NewFlagSet("mkv-bounds"), args)
	if err != nil || len(rest) != 2 {
		return cli.Usage(errors.New(usage))
	}
	path := rest[0]
	hint, err := strconv.ParseFloat(rest[1], 64)
	if err != nil || math.IsNaN(hint) || math.IsInf(hint, 0) || hint <= 0 {
		return cli.Usage(fmt.Errorf("EPISODE_MINUTES must be a positive number, got %q", rest[1]))
	}
	info, err := os.Stat(path)
	switch {
	case err != nil:
		return cli.Usage(fmt.Errorf("cannot read %s: %w", path, err))
	case !info.Mode().IsRegular():
		return cli.Usage(fmt.Errorf("%s is not a regular file", path))
	}

	probed, err := ffprobe.Probe(ctx, r, path)
	if err != nil {
		return err
	}
	st := make([]float64, len(probed.Chapters))
	for i, c := range probed.Chapters {
		st[i] = c.Start / 60
	}
	d := probed.Format.Duration / 60

	rep := Report{File: path, Duration: probed.Format.Duration, Chapters: len(st), HintMinutes: hint}
	var res Result
	switch {
	case len(st) == 0:
		rep.Reason = "no chapters, so there is nothing to split on"
	default:
		var found bool
		if res, found = Detect(d, st, hint); found {
			rep.Pattern = true
		} else {
			rep.Reason = fmt.Sprintf("no clean pattern in %.2f minutes and %d chapters at an episode length near %g minutes", d, len(st), hint)
		}
	}
	if !rep.Pattern {
		if err := cli.PrintJSON(streams.Stdout, rep); err != nil {
			return err
		}
		return cli.Exit(cli.ExitIncomplete)
	}

	indices := SplitChapterIndices(st, res.Boundaries)
	spec := make([]string, len(indices))
	for i, n := range indices {
		spec[i] = strconv.Itoa(n)
	}
	seconds := make([]float64, len(res.Boundaries))
	for i, b := range res.Boundaries {
		seconds[i] = round(b*60, 3)
	}
	rep.Found = &Found{
		Episodes:        res.Episodes(),
		EpisodeMinutes:  round(res.EpisodeLen, 4),
		FitErrorMinutes: round(res.FitError, 4),
		LastMinutes:     round(res.Tail, 4),
		Grade:           Grade(res.EpisodeLen, res.FitError, hint),
		Boundaries:      seconds,
		SplitChapters:   indices,
		Split:           "chapters:" + strings.Join(spec, ","),
		Limit:           limitNote,
	}
	return cli.PrintJSON(streams.Stdout, rep)
}

func round(v float64, places int) float64 {
	p := math.Pow10(places)
	return math.Round(v*p) / p
}
