package fingerprint

import (
	"fmt"

	"github.com/reclyptor/mediaops/internal/cli"
	"github.com/reclyptor/mediaops/internal/dhash"
)

// DupScore is the fraction of the target's body frames found in Track's body.
type DupScore struct {
	Track   string  `json:"track"`
	Overlap float64 `json:"overlap"`
}

// Control is two other tracks compared with each other: the figure a
// censored/uncensored pair or a director's cut has to beat.
type Control struct {
	A       string  `json:"a"`
	B       string  `json:"b"`
	Overlap float64 `json:"overlap"`
}

// DupReport is mkv-dupcheck's one JSON value. A track that is an alternate
// cut of another shares most of its frames; two different episodes share
// almost none. Control is null with fewer than three tracks.
type DupReport struct {
	Dir     string     `json:"dir"`
	Track   string     `json:"track"`
	Scores  []DupScore `json:"scores"`
	Control *Control   `json:"control"`
}

type rawDup struct {
	ranked  []scored
	control *Control
}

// dupcheck compares target's body with every other track's, ranked as match
// ranks its candidates. The control pair is the first two other tracks by
// name, tracks being in name order.
func dupcheck(tracks []Track, target string) (rawDup, error) {
	var t *Track
	for i := range tracks {
		if tracks[i].Name == target {
			t = &tracks[i]
		}
	}
	if t == nil {
		return rawDup{}, fmt.Errorf("no track %q among the fingerprints", target)
	}
	var out rawDup
	var others []*Track
	for i := range tracks {
		if tracks[i].Name == target {
			continue
		}
		others = append(others, &tracks[i])
		out.ranked = append(out.ranked, scored{tracks[i].Name, dhash.Overlap(t.Body, tracks[i].Body, dhash.Threshold)})
	}
	rank(out.ranked)
	if len(others) >= 2 {
		a, b := others[0], others[1]
		out.control = &Control{A: a.Name, B: b.Name, Overlap: dhash.Overlap(a.Body, b.Body, dhash.Threshold)}
	}
	return out, nil
}

// Dupcheck builds the report for target. Overlaps are rounded to four places.
func Dupcheck(dir string, tracks []Track, target string) (DupReport, error) {
	raw, err := dupcheck(tracks, target)
	if err != nil {
		return DupReport{}, cli.Usage(err)
	}
	rep := DupReport{Dir: dir, Track: target, Scores: make([]DupScore, len(raw.ranked)), Control: raw.control}
	for i, s := range raw.ranked {
		rep.Scores[i] = DupScore{Track: s.name, Overlap: round4(s.score)}
	}
	if rep.Control != nil {
		c := *rep.Control
		c.Overlap = round4(c.Overlap)
		rep.Control = &c
	}
	return rep, nil
}
