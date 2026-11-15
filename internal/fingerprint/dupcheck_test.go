package fingerprint

import (
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/reclyptor/mediaops/internal/cli"
	"github.com/reclyptor/mediaops/internal/dhash"
)

var (
	pyScoreLine   = regexp.MustCompile(`^   (\S+)  (\d\.\d{3})$`)
	pyControlLine = regexp.MustCompile(`\((\S+) vs (\S+)\): (\d\.\d{3})$`)
)

func TestDupcheckReproducesPython(t *testing.T) {
	f := loadPython(t)
	tracks := mustLoad(t, writeDir(t, f.records()))
	raw, err := dupcheck(tracks, f.DupcheckTarget)
	if err != nil {
		t.Fatal(err)
	}

	var want, got []string
	for _, line := range strings.Split(f.DupcheckStdout, "\n") {
		if m := pyScoreLine.FindStringSubmatch(line); m != nil {
			want = append(want, m[1]+" "+m[2])
		}
	}
	// The Python printed the top six of its ranking; with six others that is all.
	for _, s := range raw.ranked {
		got = append(got, fmt.Sprintf("%s %.3f", s.name, s.score))
	}
	if len(want) != 6 || strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("scores\n got  %v\n want %v", got, want)
	}
	if want[0] != "t05b 0.750" {
		t.Errorf("fixture's first line is %q, expected the alternate cut", want[0])
	}

	// The Python's control pair was whichever two files os.listdir gave first;
	// the figure it printed is for that pair, which this port must reproduce
	// for the same pair even though it picks its own.
	var ctrl []string
	for _, line := range strings.Split(f.DupcheckStdout, "\n") {
		if m := pyControlLine.FindStringSubmatch(line); m != nil {
			ctrl = m[1:]
		}
	}
	if ctrl == nil {
		t.Fatal("no control line in the Python output")
	}
	byName := map[string]Track{}
	for _, tr := range tracks {
		byName[tr.Name] = tr
	}
	if got := fmt.Sprintf("%.3f", dhash.Overlap(byName[ctrl[0]].Body, byName[ctrl[1]].Body, dhash.Threshold)); got != ctrl[2] {
		t.Errorf("control %s vs %s = %s, Python %s", ctrl[0], ctrl[1], got, ctrl[2])
	}
	if raw.control == nil || raw.control.A != "t00" || raw.control.B != "t01" {
		t.Errorf("control = %+v, want the first two other tracks by name", raw.control)
	}
}

func TestDupcheckReport(t *testing.T) {
	f := loadPython(t)
	rep, err := Dupcheck("/fp", mustLoad(t, writeDir(t, f.records())), "t05")
	if err != nil {
		t.Fatal(err)
	}
	if rep.Dir != "/fp" || rep.Track != "t05" || len(rep.Scores) != 6 {
		t.Fatalf("report = %+v", rep)
	}
	if rep.Scores[0] != (DupScore{"t05b", 0.75}) || rep.Scores[1].Track != "t04" || rep.Scores[5].Track != "t00" {
		t.Errorf("scores = %+v", rep.Scores)
	}
	if rep.Control == nil || *rep.Control != (Control{A: "t00", B: "t01", Overlap: 0}) {
		t.Errorf("control = %+v", rep.Control)
	}
}

func TestDupcheckControlNeedsThreeTracks(t *testing.T) {
	body := func(i int) []uint64 { return []uint64{farHash(i)} }
	two := []Track{track("a", nil, body(1)), track("b", nil, body(1))}
	rep, err := Dupcheck("/fp", two, "a")
	if err != nil || rep.Control != nil || len(rep.Scores) != 1 || rep.Scores[0] != (DupScore{"b", 1}) {
		t.Errorf("two tracks: %+v, %v", rep, err)
	}
	three := append(two, track("c", nil, body(2)))
	rep, err = Dupcheck("/fp", three, "c")
	if err != nil || rep.Control == nil || rep.Control.A != "a" || rep.Control.B != "b" || rep.Control.Overlap != 1 {
		t.Errorf("three tracks: %+v, %v", rep, err)
	}
	rep, err = Dupcheck("/fp", three[:1], "a")
	if err != nil || rep.Control != nil || rep.Scores == nil || len(rep.Scores) != 0 {
		t.Errorf("one track: %+v, %v", rep, err)
	}
}

func TestDupcheckTiesRankByNameDescending(t *testing.T) {
	body := []uint64{farHash(1)}
	tracks := []Track{track("a", nil, body), track("b", nil, body), track("c", nil, body), track("t", nil, body)}
	rep, err := Dupcheck("/fp", tracks, "t")
	if err != nil {
		t.Fatal(err)
	}
	if rep.Scores[0].Track != "c" || rep.Scores[1].Track != "b" || rep.Scores[2].Track != "a" {
		t.Errorf("scores = %+v", rep.Scores)
	}
}

func TestDupcheckUnknownTrackIsUsage(t *testing.T) {
	_, err := Dupcheck("/fp", []Track{track("a", nil, []uint64{1})}, "zzz")
	if cli.Code(err) != cli.ExitUsage || !strings.Contains(err.Error(), "zzz") {
		t.Errorf("err = %v (exit %d)", err, cli.Code(err))
	}
}
