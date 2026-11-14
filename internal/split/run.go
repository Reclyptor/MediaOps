package split

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/reclyptor/mediaops/internal/cli"
	"github.com/reclyptor/mediaops/internal/library"
	"github.com/reclyptor/mediaops/kit/ffprobe"
)

// Request is one split as the command line gave it.
type Request struct {
	Src    string
	OutDir string
	Prefix string
	// Exactly one of ChapterList and TimeList is set, as typed: a comma list of
	// chapter numbers or of seconds. They are parsed against the source.
	ChapterList  string
	TimeList     string
	Tol          float64
	AllowOpenGOP bool
	Go           bool
}

// MkvmergeRun records the command that made the parts.
type MkvmergeRun struct {
	Argv []string `json:"argv"`
	Exit int      `json:"exit"`
}

// Report is mkv-split's one JSON value.
type Report struct {
	Source   string        `json:"source"`
	Bytes    int64         `json:"bytes"`
	Duration float64       `json:"duration"`
	Chapters int           `json:"chapters"`
	Mode     Mode          `json:"mode"`
	Cuts     []Cut         `json:"cuts"`
	Outputs  []string      `json:"outputs"`
	DryRun   bool          `json:"dry_run"`
	Findings []PlanFinding `json:"findings"`
	Notes    []string      `json:"notes"`
	Mkvmerge *MkvmergeRun  `json:"mkvmerge,omitempty"`
	Verify   *VerifyReport `json:"verify,omitempty"`
	Clean    bool          `json:"clean"`
}

// outputPattern matches the files this split writes; it is anchored so
// a neighbouring prefix ("ep" and "ep2") never matches the other's parts.
func outputPattern(prefix, suffix string) *regexp.Regexp {
	return regexp.MustCompile("^" + regexp.QuoteMeta(prefix) + `-\d{3}` + regexp.QuoteMeta(suffix) + "$")
}

// matching lists the names in dir that match re, sorted; a directory that does
// not exist has none.
func matching(dir string, re *regexp.Regexp) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("listing %s: %w", dir, err)
	}
	var names []string
	for _, e := range entries {
		if re.MatchString(e.Name()) {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	return names, nil
}

func partName(prefix string, n int, suffix string) string {
	return fmt.Sprintf("%s-%03d%s", prefix, n, suffix)
}

// Run plans the split and, with req.Go, makes it and proves it. The source is
// only read. The report is returned with the error nil whenever there is one
// to show; clean is false for findings.
func Run(ctx context.Context, ex Tools, req Request) (*Report, error) {
	size, err := statRegular(req.Src)
	if err != nil {
		return nil, cli.Usage(fmt.Errorf("source: %w", err))
	}
	if st, err := os.Stat(req.OutDir); err == nil && !st.IsDir() {
		return nil, cli.Usage(fmt.Errorf("%s exists and is not a directory", req.OutDir))
	}
	if strings.ContainsAny(req.OutDir+req.Prefix, "%") {
		return nil, cli.Usage(errors.New("OUTDIR/PREFIX must not contain a %: mkvmerge reads it as a numbering pattern"))
	}

	info, err := ffprobe.Probe(ctx, ex, req.Src)
	if err != nil {
		return nil, err
	}
	rep := &Report{
		Source: req.Src, Bytes: size, Duration: info.Format.Duration, Chapters: len(info.Chapters),
		Cuts: []Cut{}, Outputs: []string{}, DryRun: !req.Go, Findings: []PlanFinding{}, Notes: []string{},
	}

	var times []float64
	var chapters []int
	if req.ChapterList != "" {
		rep.Mode = ModeChapters
		if len(info.Chapters) == 0 {
			return nil, cli.Usage(errors.New("the source has no chapters"))
		}
		if chapters, err = parseChapterCuts(req.ChapterList, len(info.Chapters)); err != nil {
			return nil, cli.Usage(err)
		}
		for _, c := range chapters {
			times = append(times, info.Chapters[c-1].Start)
		}
	} else {
		rep.Mode = ModeTimestamps
		if times, err = parseTimeCuts(req.TimeList, info.Format.Duration); err != nil {
			return nil, cli.Usage(err)
		}
	}

	existing, err := matching(req.OutDir, outputPattern(req.Prefix, ".mkv"))
	if err != nil {
		return nil, err
	}
	if len(existing) > 0 {
		return nil, cli.Refused(fmt.Errorf("%d output files already exist for %s (first: %s); refusing to overwrite",
			len(existing), filepath.Join(req.OutDir, req.Prefix), existing[0]))
	}

	rep.Cuts, rep.Findings, rep.Notes, err = precheck(ctx, ex, req.Src, info, times, chapters, req.Tol, req.AllowOpenGOP)
	if err != nil {
		return nil, err
	}
	if rep.Findings == nil {
		rep.Findings = []PlanFinding{}
	}
	if rep.Notes == nil {
		rep.Notes = []string{}
	}
	for n := 1; n <= len(rep.Cuts)+1; n++ {
		rep.Outputs = append(rep.Outputs, filepath.Join(req.OutDir, partName(req.Prefix, n, ".mkv")))
	}
	if !req.Go || len(rep.Findings) > 0 {
		rep.Clean = len(rep.Findings) == 0
		return rep, nil
	}

	if err := execute(ctx, ex, req, rep); err != nil {
		return nil, err
	}
	rep.Clean = len(rep.Findings) == 0 && rep.Verify != nil && rep.Verify.Clean
	return rep, nil
}

// execute writes the parts, places them and verifies them.
func execute(ctx context.Context, ex Tools, req Request, rep *Report) error {
	if err := os.MkdirAll(req.OutDir, 0o770); err != nil {
		return fmt.Errorf("creating %s: %w", req.OutDir, err)
	}
	partials := outputPattern(req.Prefix, ".mkv.partial")
	leftovers, err := matching(req.OutDir, partials)
	if err != nil {
		return err
	}
	for _, name := range leftovers {
		if err := library.DiscardPartial(filepath.Join(req.OutDir, name)); err != nil {
			return err
		}
	}

	argv := []string{"mkvmerge", "-o", filepath.Join(req.OutDir, req.Prefix+"-%03d.mkv.partial"), "--split", splitSpec(rep.Cuts), req.Src}
	res, err := ex.Run(ctx, argv...)
	if err != nil {
		return fmt.Errorf("running mkvmerge: %w", err)
	}
	rep.Mkvmerge = &MkvmergeRun{Argv: argv, Exit: res.ExitCode}
	// mkvmerge exits 1 for warnings, which are routine on a Play-All.
	if res.ExitCode < 0 || res.ExitCode > 1 {
		return fmt.Errorf("mkvmerge exited %d: %s", res.ExitCode, tailOf(res.Stdout, res.Stderr))
	}

	written, err := matching(req.OutDir, partials)
	if err != nil {
		return err
	}
	want := len(rep.Cuts) + 1
	if len(written) != want {
		rep.Findings = append(rep.Findings, PlanFinding{Problem: fmt.Sprintf(
			"mkvmerge wrote %d parts, expected %d; the .partial files are left in %s", len(written), want, req.OutDir)})
		return nil
	}
	for i, name := range written {
		if name != partName(req.Prefix, i+1, ".mkv.partial") {
			rep.Findings = append(rep.Findings, PlanFinding{Problem: fmt.Sprintf(
				"mkvmerge wrote %s where %s was expected; the .partial files are left in %s", name, partName(req.Prefix, i+1, ".mkv.partial"), req.OutDir)})
			return nil
		}
	}
	for _, out := range rep.Outputs {
		if err := library.PlacePartial(out); err != nil {
			if errors.Is(err, library.ErrExists) {
				return cli.Refused(err)
			}
			return err
		}
	}

	planned := make([]float64, len(rep.Cuts))
	for i, c := range rep.Cuts {
		planned[i] = c.Keyframe
	}
	rep.Verify, err = Verify(ctx, ex, req.Src, rep.Outputs, planned)
	return err
}

// splitSpec is mkvmerge's --split value. A timestamp split is given each
// keyframe's own time, never the requested one: mkvmerge starts a part at the
// first keyframe at or after the time it is given.
func splitSpec(cuts []Cut) string {
	var parts []string
	if cuts[0].Chapter != 0 {
		for _, c := range cuts {
			parts = append(parts, strconv.Itoa(c.Chapter))
		}
		return "chapters:" + strings.Join(parts, ",")
	}
	for _, c := range cuts {
		parts = append(parts, c.arg)
	}
	return "timestamps:" + strings.Join(parts, ",")
}

// tailOf is the last 2000 bytes of mkvmerge's output, which is where it says
// why it failed.
func tailOf(stdout, stderr []byte) string {
	s := strings.TrimSpace(string(stdout) + "\n" + string(stderr))
	if len(s) > 2000 {
		s = s[len(s)-2000:]
	}
	return s
}
