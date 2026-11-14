package bounds

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/reclyptor/mediaops/internal/cli"
	"github.com/reclyptor/mediaops/kit/proc"
)

// exitRunner is an ffprobe that fails.
type exitRunner struct{ fixtureRunner }

func (exitRunner) Run(context.Context, ...string) (proc.Result, error) {
	return proc.Result{ExitCode: 1, Stderr: []byte("Invalid data found")}, nil
}

func run(t *testing.T, r proc.Runner, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	var out, errOut bytes.Buffer
	code = cli.Run(context.Background(), "mkv-bounds", args, cli.IO{Stdout: &out, Stderr: &errOut},
		func(ctx context.Context, args []string, s cli.IO) error { return Command(ctx, r, args, s) })
	return code, out.String(), errOut.String()
}

func regularFile(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "x_t00.mkv")
	if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestCommandReportsAPattern(t *testing.T) {
	file := regularFile(t)
	code, out, errOut := run(t, fixtureRunner{readFixture(t, "fate-apocrypha-s01d01.json")}, file, "24")
	if code != 0 || errOut != "" {
		t.Fatalf("code %d stderr %q", code, errOut)
	}
	var rep struct {
		File            string
		Duration        float64
		Chapters        int
		HintMinutes     float64 `json:"hint_minutes"`
		Pattern         bool
		Episodes        int
		EpisodeMinutes  float64 `json:"episode_minutes"`
		FitErrorMinutes float64 `json:"fit_error_minutes"`
		LastMinutes     float64 `json:"last_minutes"`
		Grade           string
		Boundaries      []float64
		SplitChapters   []int `json:"split_chapters"`
		Split           string
		Limit           string
	}
	if err := json.Unmarshal([]byte(out), &rep); err != nil {
		t.Fatal(err)
	}
	if rep.File != file || rep.Duration != 8550.046 || rep.Chapters != 26 || rep.HintMinutes != 24 || !rep.Pattern {
		t.Errorf("header = %+v", rep)
	}
	if rep.Episodes != 6 || rep.EpisodeMinutes != 23.7503 || rep.FitErrorMinutes != 0.0203 || rep.LastMinutes != 23.7495 || rep.Grade != "auto" {
		t.Errorf("figures = %+v", rep)
	}
	if !reflect.DeepEqual(rep.SplitChapters, []int{6, 10, 14, 18, 23}) || rep.Split != "chapters:6,10,14,18,23" {
		t.Errorf("split = %v %q", rep.SplitChapters, rep.Split)
	}
	if len(rep.Boundaries) != 6 || rep.Boundaries[0] != 0 || !near(rep.Boundaries[1], 1425.0, 0.05) {
		t.Errorf("boundaries = %v", rep.Boundaries)
	}
	if !strings.Contains(rep.Limit, "constant offset is invisible") {
		t.Errorf("limit = %q", rep.Limit)
	}
	if strings.Contains(out, `"reason"`) {
		t.Errorf("a pattern carries no reason: %s", out)
	}
}

func TestCommandGradesReviewWhenTheHintDisagrees(t *testing.T) {
	// a hint of 28 still lets the scan find the 23.75 minute episodes, which
	// drift 15% from it: a confident split that must be content-verified
	code, out, _ := run(t, fixtureRunner{readFixture(t, "fate-apocrypha-s01d01.json")}, regularFile(t), "28")
	if code != 0 || !strings.Contains(out, `"grade": "review"`) || !strings.Contains(out, `"episodes": 6`) {
		t.Fatalf("code %d out %s", code, out)
	}
}

func TestCommandWithAHalfEpisodeHintFindsHalfEpisodes(t *testing.T) {
	code, out, _ := run(t, fixtureRunner{readFixture(t, "sailor-moon-s01d01.json")}, regularFile(t), "12")
	if code != 0 || !strings.Contains(out, `"episodes": 16`) {
		t.Fatalf("code %d out %s", code, out)
	}
}

func TestCommandNoPattern(t *testing.T) {
	code, out, errOut := run(t, fixtureRunner{readFixture(t, "fate-apocrypha-s01d01.json")}, regularFile(t), "7")
	if code != 1 || errOut != "" {
		t.Fatalf("code %d stderr %q", code, errOut)
	}
	var rep map[string]any
	if err := json.Unmarshal([]byte(out), &rep); err != nil {
		t.Fatal(err)
	}
	if rep["pattern"] != false || !strings.Contains(rep["reason"].(string), "no clean pattern") || rep["chapters"] != float64(26) {
		t.Errorf("report = %v", rep)
	}
	if _, has := rep["episodes"]; has {
		t.Errorf("a miss must not carry a split: %v", rep)
	}
}

func TestCommandNoChapters(t *testing.T) {
	probe := `{"streams": [], "chapters": [], "format": {"size": "10", "duration": "1500.0"}}`
	code, out, _ := run(t, fixtureRunner{[]byte(probe)}, regularFile(t), "24")
	if code != 1 || !strings.Contains(out, `"pattern": false`) || !strings.Contains(out, "no chapters") {
		t.Fatalf("code %d out %s", code, out)
	}
}

func TestCommandUsage(t *testing.T) {
	file := regularFile(t)
	r := fixtureRunner{readFixture(t, "fate-apocrypha-s01d01.json")}
	cases := map[string][]string{
		"no arguments":    {},
		"no hint":         {file},
		"extra argument":  {file, "24", "25"},
		"hint zero":       {file, "0"},
		"hint negative":   {file, "-24"},
		"hint NaN":        {file, "NaN"},
		"hint infinite":   {file, "Inf"},
		"hint not number": {file, "long"},
		"unknown flag":    {file, "24", "--nope"},
		"missing file":    {filepath.Join(t.TempDir(), "absent.mkv"), "24"},
		"a directory":     {t.TempDir(), "24"},
	}
	for name, args := range cases {
		code, out, errOut := run(t, r, args...)
		if code != 2 || out != "" || !strings.HasPrefix(errOut, "mkv-bounds: FATAL: ") {
			t.Errorf("%s: code %d stdout %q stderr %q", name, code, out, errOut)
		}
	}
}

func TestCommandFfprobeFailureIsInternal(t *testing.T) {
	code, out, errOut := run(t, exitRunner{}, regularFile(t), "24")
	if code != 4 || out != "" || !strings.Contains(errOut, "ffprobe exited 1") {
		t.Fatalf("code %d stdout %q stderr %q", code, out, errOut)
	}
}

func TestCommandVersion(t *testing.T) {
	if code, out, _ := run(t, fixtureRunner{}, "--version"); code != 0 || !strings.HasPrefix(out, "mkv-bounds ") {
		t.Fatalf("code %d out %q", code, out)
	}
}
