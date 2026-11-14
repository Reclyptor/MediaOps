package inventory

import (
	"context"
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"sort"
	"strings"

	"github.com/reclyptor/mediaops/internal/cli"
)

// Disc statuses of an order report.
const (
	Confirmed  = "confirmed"
	Mismatch   = "mismatch"
	NoPlayAll  = "no_playall"
	Unreadable = "unreadable"
)

// chapterTolerance is how far a chapter mark may sit from where the episode
// files predict it, in seconds. Marks are exact; this absorbs rounding.
const chapterTolerance = 3.0

// DiscReport is one disc's verdict.
type DiscReport struct {
	Dir     string `json:"dir"`
	Status  string `json:"status"`
	PlayAll string `json:"playall,omitempty"`
	// Episodes are the episode tracks in the order they were laid end to end.
	Episodes []string `json:"episodes"`
	Detail   string   `json:"detail"`
	// EpisodeMinutesSum and PlayAllMinutes are the lengths compared when a disc
	// fails to confirm, rounded to hundredths.
	EpisodeMinutesSum float64 `json:"episode_minutes_sum,omitempty"`
	PlayAllMinutes    float64 `json:"playall_minutes,omitempty"`
}

// OrderReport is mkv-order's one JSON value.
type OrderReport struct {
	Inventory string       `json:"inventory"`
	Dir       string       `json:"dir,omitempty"`
	Confirmed int          `json:"confirmed"`
	Total     int          `json:"total"`
	Discs     []DiscReport `json:"discs"`
}

// OrderOptions are the episode length window, in minutes.
type OrderOptions struct {
	MinEpisode, MaxEpisode float64
}

// Order checks every disc in inv under dir ("" for all of them) against its own
// Play-All. The Play-All is the disc authoring's own statement of episode order,
// so if the episode files laid end to end reproduce its chapter marks, track
// order is episode order, with no frames needed. That does NOT say which
// episode NUMBER the first track is, only that the tracks are sequential.
//
// Discs are the records' parent directories, selected by the record's absolute
// path. The Python selected the series by a component of the relative path,
// which stopped naming the series when the box segment joined MEDIA_ROOT and
// silently matched nothing; the path a record carries is the one thing that
// cannot drift that way.
func Order(inv Inventory, dir string, opt OrderOptions) OrderReport {
	byDisc := map[string][]Record{}
	for _, rec := range inv.Files {
		if dir == "" || within(dir, rec.Path) {
			disc := filepath.Dir(rec.Path)
			byDisc[disc] = append(byDisc[disc], rec)
		}
	}
	dirs := make([]string, 0, len(byDisc))
	for d := range byDisc {
		dirs = append(dirs, d)
	}
	sort.Strings(dirs)

	rep := OrderReport{Dir: dir, Total: len(dirs), Discs: make([]DiscReport, 0, len(dirs))}
	for _, d := range dirs {
		disc := orderDisc(d, byDisc[d], opt)
		if disc.Status == Confirmed {
			rep.Confirmed++
		}
		rep.Discs = append(rep.Discs, disc)
	}
	return rep
}

func within(dir, path string) bool {
	rel, err := filepath.Rel(dir, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func orderDisc(dir string, recs []Record, opt OrderOptions) DiscReport {
	out := DiscReport{Dir: dir, Episodes: []string{}}
	sort.Slice(recs, func(i, j int) bool { return recs[i].Path < recs[j].Path })

	var failed []string
	for _, r := range recs {
		if r.Error != "" {
			failed = append(failed, filepath.Base(r.Path))
		}
	}
	if len(failed) > 0 {
		out.Status = Unreadable
		out.Detail = "could not be probed: " + strings.Join(failed, ", ")
		return out
	}

	playall := recs[0]
	for _, r := range recs[1:] {
		if r.Duration > playall.Duration {
			playall = r
		}
	}
	var eps []Record
	for _, r := range recs {
		if m := r.Duration / 60; r.Path != playall.Path && opt.MinEpisode <= m && m <= opt.MaxEpisode {
			eps = append(eps, r)
			out.Episodes = append(out.Episodes, r.Path)
		}
	}
	out.PlayAll = playall.Path
	out.PlayAllMinutes = roundHundredths(playall.Duration / 60)
	if playall.Duration/60 < opt.MaxEpisode || len(eps) == 0 {
		out.Status = NoPlayAll
		out.Detail = fmt.Sprintf("no Play-All to check against: the longest track is %.2f minutes and %d tracks are episode length",
			playall.Duration/60, len(eps))
		return out
	}

	sum := 0.0
	for _, e := range eps {
		sum += e.Duration
	}
	out.EpisodeMinutesSum = roundHundredths(sum / 60)
	ok, detail := check(playall, eps)
	out.Status, out.Detail = Mismatch, detail
	if ok {
		out.Status = Confirmed
	}
	return out
}

func roundHundredths(v float64) float64 { return math.Round(v*100) / 100 }

// check walks the episodes in track order against the Play-All's chapter marks:
// each episode's own chapters, shifted by the time before it, must be the next
// marks, and the mark after its last chapter must sit at its end. Everything is
// in seconds. Chapter 0 of the Play-All is episode 1's chapter 0.
func check(playall Record, eps []Record) (bool, string) {
	marks := playall.Chapters
	cursor := 0.0
	consumed := 1
	for i, e := range eps {
		for _, c := range tailOf(e.Chapters) {
			target := cursor + c
			if consumed >= len(marks) {
				return false, fmt.Sprintf("ran out of Play-All chapters at episode %d", i+1)
			}
			if math.Abs(marks[consumed]-target) > chapterTolerance {
				return false, fmt.Sprintf("episode %d (%s) chapter at %.1f s -> expected %.1f s, Play-All has %.1f s",
					i+1, filepath.Base(e.Path), c, target, marks[consumed])
			}
			consumed++
		}
		cursor += e.Duration
		if i+1 < len(eps) {
			if consumed >= len(marks) {
				return false, fmt.Sprintf("boundary after episode %d expected at %.1f s, Play-All has no chapter left", i+1, cursor)
			}
			if math.Abs(marks[consumed]-cursor) > chapterTolerance {
				return false, fmt.Sprintf("boundary after episode %d expected at %.1f s, Play-All has %.1f s", i+1, cursor, marks[consumed])
			}
			consumed++
		}
	}
	return true, fmt.Sprintf("%d episodes, %d/%d chapters consumed", len(eps), consumed, len(marks))
}

func tailOf(c []float64) []float64 {
	if len(c) == 0 {
		return nil
	}
	return c[1:]
}

// OrderCommand is the mkv-order body: args are what follows the program name.
// It prints one JSON report and exits 1 unless every disc is confirmed.
func OrderCommand(_ context.Context, args []string, streams cli.IO) error {
	const usage = "usage: mkv-order INVENTORY.json [DIR] [--min-episode-minutes 15] [--max-episode-minutes 40] | --version"
	opt := OrderOptions{MinEpisode: 15, MaxEpisode: 40}
	fs := cli.NewFlagSet("mkv-order")
	fs.Float64Var(&opt.MinEpisode, "min-episode-minutes", opt.MinEpisode, "")
	fs.Float64Var(&opt.MaxEpisode, "max-episode-minutes", opt.MaxEpisode, "")
	rest, err := cli.ParseFlags(fs, args)
	if err != nil || len(rest) < 1 || len(rest) > 2 {
		return cli.Usage(errors.New(usage))
	}
	if !(opt.MinEpisode > 0) || !(opt.MaxEpisode >= opt.MinEpisode) || math.IsInf(opt.MaxEpisode, 0) {
		return cli.Usage(fmt.Errorf("--min-episode-minutes must be positive and no more than --max-episode-minutes, got %g and %g",
			opt.MinEpisode, opt.MaxEpisode))
	}

	inv, err := Read(rest[0])
	if err != nil {
		return err
	}
	invPath, err := filepath.Abs(rest[0])
	if err != nil {
		return cli.Usage(fmt.Errorf("cannot resolve %s: %w", rest[0], err))
	}
	dir := ""
	if len(rest) == 2 {
		if dir, err = filepath.Abs(rest[1]); err != nil {
			return cli.Usage(fmt.Errorf("cannot resolve %s: %w", rest[1], err))
		}
	}

	rep := Order(inv, dir, opt)
	rep.Inventory = invPath
	if rep.Total == 0 {
		if dir != "" {
			return cli.Usage(fmt.Errorf("no record of %s lies under %s", rest[0], dir))
		}
		return cli.Usage(fmt.Errorf("%s holds no records", rest[0]))
	}
	if err := cli.PrintJSON(streams.Stdout, rep); err != nil {
		return err
	}
	if rep.Confirmed != rep.Total {
		return cli.Exit(cli.ExitIncomplete)
	}
	return nil
}
