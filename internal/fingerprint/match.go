package fingerprint

import (
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/reclyptor/mediaops/internal/cli"
	"github.com/reclyptor/mediaops/internal/dhash"
)

// A preview's best match is confident at MinScore, and when it leads the
// runner-up by MinGap.
const (
	MinScore = 0.30
	MinGap   = 0.15
)

// Verdicts a match gets.
const (
	Confident = "confident"
	Weak      = "weak"
)

// Track is a record loaded from a fingerprint directory under the name of its
// file without the .json.
type Track struct {
	Name    string
	Record  Record
	Body    []uint64
	Preview []uint64
}

// LoadDir reads every regular *.json in dir that does not start with a dot,
// in name order. A file that is not a schema-1 record is a usage error naming
// it, which includes every record the Python tool wrote.
func LoadDir(dir string) ([]Track, error) {
	info, err := os.Stat(dir)
	switch {
	case err != nil:
		return nil, cli.Usage(fmt.Errorf("cannot read %s: %w", dir, err))
	case !info.IsDir():
		return nil, cli.Usage(fmt.Errorf("%s is not a directory", dir))
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("listing %s: %w", dir, err)
	}
	var tracks []Track
	for _, e := range entries {
		name, ok := strings.CutSuffix(e.Name(), ".json")
		if !ok || strings.HasPrefix(e.Name(), ".") || !e.Type().IsRegular() {
			continue
		}
		rec, err := Read(filepath.Join(dir, e.Name()))
		if errors.Is(err, ErrInvalid) {
			return nil, cli.Usage(err)
		}
		if err != nil {
			return nil, err
		}
		body, preview, err := rec.Hashes()
		if err != nil {
			return nil, cli.Usage(fmt.Errorf("%s: %w", e.Name(), err))
		}
		tracks = append(tracks, Track{Name: name, Record: rec, Body: body, Preview: preview})
	}
	sort.Slice(tracks, func(i, j int) bool { return tracks[i].Name < tracks[j].Name })
	return tracks, nil
}

// Match is one previewed track's verdict. The runner-up fields are null when
// the track had nothing to be compared with.
type Match struct {
	Track         string   `json:"track"`
	Successor     string   `json:"successor"`
	Score         float64  `json:"score"`
	RunnerUp      *string  `json:"runner_up"`
	RunnerUpScore *float64 `json:"runner_up_score"`
	Gap           *float64 `json:"gap"`
	Verdict       string   `json:"verdict"`
}

// Finding is something the caller must resolve before trusting the order.
type Finding struct {
	Track   string `json:"track"`
	Problem string `json:"problem"`
}

// MatchReport is mkv-fingerprint match's one JSON value. Scores are rounded
// to four places for display; every verdict is decided on the exact values.
type MatchReport struct {
	Dir      string    `json:"dir"`
	Tracks   int       `json:"tracks"`
	Matches  []Match   `json:"matches"`
	Terminal []string  `json:"terminal"`
	Findings []Finding `json:"findings"`
	Clean    bool      `json:"clean"`
}

type scored struct {
	name  string
	score float64
}

// rank orders candidates best first: score descending, then name descending,
// which is how the Python's reverse-sorted (score, name) tuples fell out.
func rank(s []scored) {
	sort.Slice(s, func(i, j int) bool {
		if s[i].score != s[j].score {
			return s[i].score > s[j].score
		}
		return s[i].name > s[j].name
	})
}

// decision is a previewed track's result at full precision.
type decision struct {
	track   string
	best    scored
	second  *scored
	gap     float64
	verdict string
}

func decide(tracks []Track) (decisions []decision, terminal []string) {
	for _, a := range tracks {
		if len(a.Preview) == 0 {
			terminal = append(terminal, a.Name)
			continue
		}
		var cands []scored
		for _, b := range tracks {
			if b.Name != a.Name {
				cands = append(cands, scored{b.Name, dhash.Overlap(a.Preview, b.Body, dhash.Threshold)})
			}
		}
		rank(cands)
		d := decision{track: a.Name, best: cands[0], verdict: Weak}
		if len(cands) > 1 {
			d.second = &cands[1]
			d.gap = d.best.score - d.second.score
			if d.best.score >= MinScore && d.gap >= MinGap {
				d.verdict = Confident
			}
		}
		decisions = append(decisions, d)
	}
	return decisions, terminal
}

// Analyze scores the preview of every track against every other track's body
// and names each one's successor. dir is only echoed into the report.
func Analyze(dir string, tracks []Track) MatchReport {
	decisions, terminal := decide(tracks)
	rep := MatchReport{
		Dir:      dir,
		Tracks:   len(tracks),
		Matches:  []Match{},
		Terminal: append([]string{}, terminal...),
		Findings: []Finding{},
	}
	confidentBy := map[string][]string{}
	for _, d := range decisions {
		m := Match{Track: d.track, Successor: d.best.name, Score: round4(d.best.score), Verdict: d.verdict}
		if d.second != nil {
			m.RunnerUp = &d.second.name
			m.RunnerUpScore = ptr(round4(d.second.score))
			m.Gap = ptr(round4(d.gap))
		}
		rep.Matches = append(rep.Matches, m)
		if d.verdict == Confident {
			confidentBy[d.best.name] = append(confidentBy[d.best.name], d.track)
		}
	}
	for _, d := range decisions {
		if d.verdict == Weak {
			rep.Findings = append(rep.Findings, Finding{Track: d.track, Problem: "weak: " + describe(d)})
		}
		if sharers := confidentBy[d.best.name]; d.verdict == Confident && len(sharers) > 1 {
			rep.Findings = append(rep.Findings, Finding{
				Track:   d.track,
				Problem: fmt.Sprintf("confident, but %s all name %s as their successor", strings.Join(sharers, ", "), d.best.name),
			})
		}
	}
	rep.Clean = len(rep.Findings) == 0
	return rep
}

func describe(d decision) string {
	if d.second == nil {
		return fmt.Sprintf("%s -> %s (match %.2f, no runner-up to compare it with)", d.track, d.best.name, d.best.score)
	}
	return fmt.Sprintf("%s -> %s (match %.2f, runner-up %s %.2f, gap %.2f)", d.track, d.best.name, d.best.score, d.second.name, d.second.score, d.gap)
}

func round4(f float64) float64 { return math.Round(f*1e4) / 1e4 }

func ptr[T any](v T) *T { return &v }
