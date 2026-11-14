package split

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/reclyptor/mediaops/internal/cli"
)

func decodeReport(t *testing.T, stdout string) Report {
	t.Helper()
	var rep Report
	if err := json.Unmarshal([]byte(stdout), &rep); err != nil {
		t.Fatalf("stdout is not a split report: %v\n%s", err, stdout)
	}
	return rep
}

// splitClean runs a --go split of the source into a fresh directory and
// returns the parts.
func splitClean(t *testing.T, args ...string) (outdir string, parts []string, rep Report) {
	t.Helper()
	outdir = filepath.Join(t.TempDir(), "out")
	full := append([]string{source(t), filepath.Join(outdir, "ep")}, args...)
	code, stdout, stderr := runCLI(t, newRecorder(), append(full, "--go")...)
	if code != cli.ExitOK {
		t.Fatalf("exit %d, stderr %q\n%s", code, stderr, stdout)
	}
	rep = decodeReport(t, stdout)
	for _, name := range dirNames(t, outdir) {
		parts = append(parts, filepath.Join(outdir, name))
	}
	return outdir, parts, rep
}

func problems(r *VerifyReport) string {
	var b strings.Builder
	for _, f := range r.Findings {
		b.WriteString(f.Problem + "\n")
	}
	return b.String()
}

func TestChapterSplitGo(t *testing.T) {
	src := source(t)
	before := fingerprintOf(t, src)

	// A stale partial of this split is discarded; another name is not touched.
	outdir := filepath.Join(t.TempDir(), "out")
	if err := os.MkdirAll(outdir, 0o770); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"ep-001.mkv.partial", "keep.partial"} {
		if err := os.WriteFile(filepath.Join(outdir, name), []byte("stale"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	code, stdout, stderr := runCLI(t, newRecorder(), src, filepath.Join(outdir, "ep"), "2,3,4", "--go")
	if code != cli.ExitOK {
		t.Fatalf("exit %d, stderr %q\n%s", code, stderr, stdout)
	}
	rep := decodeReport(t, stdout)
	parts := rep.Outputs

	if want := []string{"ep-001.mkv", "ep-002.mkv", "ep-003.mkv", "ep-004.mkv", "keep.partial"}; !slices.Equal(dirNames(t, outdir), want) {
		t.Fatalf("outdir holds %v, want %v", dirNames(t, outdir), want)
	}
	if !rep.Clean || rep.Verify == nil || !rep.Verify.Clean {
		t.Fatalf("report is not clean: %+v", rep)
	}
	if rep.DryRun || rep.Mode != ModeChapters || len(rep.Cuts) != 3 || len(parts) != 4 {
		t.Fatalf("unexpected report shape: %+v", rep)
	}
	v := rep.Verify
	if len(v.Seams) != 3 || v.BytesPercent < 99 || v.BytesPercent > 100.5 {
		t.Fatalf("seams %d, bytes %.3f%%", len(v.Seams), v.BytesPercent)
	}
	for _, s := range v.Streams {
		if !s.Identical || s.Accounted != s.Packets {
			t.Errorf("stream %d: %+v", s.Index, s)
		}
		if want := map[string]string{"video": "packets", "audio": "bytes", "subtitle": "packets"}[s.Type]; s.Compared != want {
			t.Errorf("stream %d (%s) compared %q, want %q", s.Index, s.Type, s.Compared, want)
		}
		if s.Compared == "bytes" && (s.Bytes == 0 || len(s.PartBytes) != 4) {
			t.Errorf("PCM stream %+v lacks its byte counts", s)
		}
		if s.Type == "video" && s.AUDRemoved != s.Packets {
			t.Errorf("video aud_removed %d of %d packets", s.AUDRemoved, s.Packets)
		}
	}
	if after := fingerprintOf(t, src); after != before {
		t.Fatal("the source changed")
	}
}

func TestDryRunWritesNothing(t *testing.T) {
	outdir := filepath.Join(t.TempDir(), "out")
	code, stdout, stderr := runCLI(t, newRecorder(), source(t), filepath.Join(outdir, "ep"), "2,3,4")
	if code != cli.ExitOK {
		t.Fatalf("exit %d: %s %s", code, stderr, stdout)
	}
	rep := decodeReport(t, stdout)
	if !rep.DryRun || !rep.Clean || len(rep.Outputs) != 4 || rep.Mkvmerge != nil || rep.Verify != nil {
		t.Fatalf("report: %+v", rep)
	}
	if _, err := os.Stat(outdir); !os.IsNotExist(err) {
		t.Fatalf("a dry run created %s (%v)", outdir, err)
	}
}

func TestExistingOutputRefused(t *testing.T) {
	outdir := t.TempDir()
	existing := filepath.Join(outdir, "ep-002.mkv")
	if err := os.WriteFile(existing, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, goFlag := range [][]string{nil, {"--go"}} {
		args := append([]string{source(t), filepath.Join(outdir, "ep"), "2,3,4"}, goFlag...)
		code, stdout, stderr := runCLI(t, newRecorder(), args...)
		if code != cli.ExitRefused || stdout != "" || !strings.Contains(stderr, "FATAL") {
			t.Fatalf("%v: exit %d stdout %q stderr %q", goFlag, code, stdout, stderr)
		}
	}
	if !slices.Equal(dirNames(t, outdir), []string{"ep-002.mkv"}) {
		t.Fatalf("outdir changed: %v", dirNames(t, outdir))
	}
	// A neighbouring prefix is not this split's output.
	other := filepath.Join(outdir, "ep2-001.mkv")
	if err := os.WriteFile(other, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code, _, stderr := runCLI(t, newRecorder(), source(t), filepath.Join(outdir, "ep"), "2,3,4"); code != cli.ExitRefused {
		t.Fatalf("exit %d: %s", code, stderr)
	}
}

func TestAtOnExactKeyframes(t *testing.T) {
	_, parts, rep := splitClean(t, "--at", "5,10,15")
	if !rep.Clean || len(parts) != 4 || rep.Mode != ModeTimestamps {
		t.Fatalf("%+v", rep)
	}
	for _, c := range rep.Cuts {
		if c.Chapter != 0 || c.DeltaMS != 0 {
			t.Errorf("cut %+v", c)
		}
	}
}

func TestAtHandsMkvmergeTheKeyframeTime(t *testing.T) {
	// 5 ms before the keyframe at 10 s: the cut is that keyframe, and
	// mkvmerge is given its time, not the request's.
	rec := newRecorder()
	outdir := filepath.Join(t.TempDir(), "out")
	code, stdout, stderr := runCLI(t, rec, source(t), filepath.Join(outdir, "ep"), "--at", "5,9.995", "--go")
	if code != cli.ExitOK {
		t.Fatalf("exit %d: %s\n%s", code, stderr, stdout)
	}
	rep := decodeReport(t, stdout)
	if rep.Cuts[1].Keyframe != 10 || rep.Cuts[1].Requested != 9.995 {
		t.Fatalf("planned cut is %+v, want the keyframe at 10", rep.Cuts[1])
	}
	argv := rec.mkvmergeArgv(t)
	if got := argv[len(argv)-2]; got != "timestamps:5.000s,10.000s" {
		t.Fatalf("mkvmerge was given %q", got)
	}
	if !rep.Verify.Clean || len(rep.Verify.Seams) != 2 {
		t.Fatalf("verify: %s", problems(rep.Verify))
	}
}

func TestAtHalfAMsAfterAKeyframe(t *testing.T) {
	// Inside the 1 ms slack the keyframe is still "at" the request.
	outdir := filepath.Join(t.TempDir(), "out")
	code, stdout, stderr := runCLI(t, newRecorder(), source(t), filepath.Join(outdir, "ep"), "--at", "10.0005")
	if code != cli.ExitOK {
		t.Fatalf("exit %d: %s\n%s", code, stderr, stdout)
	}
	if rep := decodeReport(t, stdout); rep.Cuts[0].Keyframe != 10 || rep.Cuts[0].DeltaMS > 0 {
		t.Fatalf("%+v", rep.Cuts[0])
	}
}

func TestAtAFewMsAfterAKeyframePlansThatKeyframe(t *testing.T) {
	// 5 ms past the keyframe at 10 s, inside the tolerance: --at hands mkvmerge
	// the keyframe's own time, so the nearest keyframe is the earlier one.
	rec := newRecorder()
	outdir := filepath.Join(t.TempDir(), "out")
	code, stdout, stderr := runCLI(t, rec, source(t), filepath.Join(outdir, "ep"), "--at", "5,10.005", "--go")
	if code != cli.ExitOK {
		t.Fatalf("exit %d: %s\n%s", code, stderr, stdout)
	}
	rep := decodeReport(t, stdout)
	if rep.Cuts[1].Keyframe != 10 || rep.Cuts[1].DeltaMS > -4 {
		t.Fatalf("planned cut is %+v, want the keyframe at 10", rep.Cuts[1])
	}
	argv := rec.mkvmergeArgv(t)
	if got := argv[len(argv)-2]; got != "timestamps:5.000s,10.000s" {
		t.Fatalf("mkvmerge was given %q", got)
	}
	if !rep.Verify.Clean || len(rep.Verify.Seams) != 2 {
		t.Fatalf("verify: %s", problems(rep.Verify))
	}
}

func TestAtOffAKeyframeWritesNothing(t *testing.T) {
	outdir := filepath.Join(t.TempDir(), "out")
	rec := newRecorder()
	code, stdout, _ := runCLI(t, rec, source(t), filepath.Join(outdir, "ep"), "--at", "2.5", "--go")
	if code != cli.ExitIncomplete {
		t.Fatalf("exit %d", code)
	}
	rep := decodeReport(t, stdout)
	if rep.Clean || len(rep.Findings) != 1 || rep.Findings[0].Cut != 1 || !strings.Contains(rep.Findings[0].Problem, "not a keyframe") {
		t.Fatalf("findings %+v", rep.Findings)
	}
	if !strings.Contains(rep.Findings[0].Problem, "2.000") || !strings.Contains(rep.Findings[0].Problem, "3.000") {
		t.Fatalf("the finding does not name the keyframes either side: %q", rep.Findings[0].Problem)
	}
	if _, err := os.Stat(outdir); !os.IsNotExist(err) {
		t.Fatal("the output directory exists")
	}
	for _, a := range rec.argvs {
		if a[0] == "mkvmerge" {
			t.Fatal("mkvmerge ran")
		}
	}
}

func TestUsageErrors(t *testing.T) {
	src, prefix := source(t), filepath.Join(t.TempDir(), "ep")
	for name, args := range map[string][]string{
		"no args":            {},
		"missing cuts":       {src, prefix},
		"chapter 1":          {src, prefix, "1,2"},
		"chapter past end":   {src, prefix, "2,9"},
		"not increasing":     {src, prefix, "3,2"},
		"not a number":       {src, prefix, "a"},
		"at past the end":    {src, prefix, "--at", "25"},
		"at not increasing":  {src, prefix, "--at", "10,5"},
		"at and a chapter":   {src, prefix, "2", "--at", "5"},
		"bad tol":            {src, prefix, "2", "--tol", "0"},
		"no such source":     {filepath.Join(t.TempDir(), "none.mkv"), prefix, "2"},
		"verify one part":    {"verify", src},
		"verify no source":   {"verify", filepath.Join(t.TempDir(), "none.mkv"), src},
		"verify dir source":  {"verify", t.TempDir(), src},
		"split dir source":   {t.TempDir(), prefix, "2"},
		"verify with no src": {"verify"},
	} {
		if code, stdout, _ := runCLI(t, newRecorder(), args...); code != cli.ExitUsage || stdout != "" {
			t.Errorf("%s: exit %d, stdout %q", name, code, stdout)
		}
	}
}

var (
	partsOnce sync.Once
	partsList []string
	partsErr  string
)

// cleanParts splits the source on its chapters once per test binary run and
// returns the four parts. Tests only read them.
func cleanParts(t *testing.T) []string {
	t.Helper()
	src := source(t)
	partsOnce.Do(func() {
		dir := filepath.Join(fixtureRoot, "parts")
		code, stdout, stderr := runCLI(t, newRecorder(), src, filepath.Join(dir, "ep"), "2,3,4", "--go")
		if code != cli.ExitOK {
			partsErr = fmt.Sprintf("exit %d: %s\n%s", code, stderr, stdout)
			return
		}
		for _, name := range dirNames(t, dir) {
			partsList = append(partsList, filepath.Join(dir, name))
		}
	})
	if partsErr != "" {
		t.Fatalf("splitting the fixture: %s", partsErr)
	}
	return partsList
}

func verify(t *testing.T, parts []string) *VerifyReport {
	t.Helper()
	rep, err := Verify(context.Background(), newRecorder(), source(t), parts, nil)
	if err != nil {
		t.Fatal(err)
	}
	return rep
}

func hasFinding(r *VerifyReport, part, stream int, substr string) bool {
	for _, f := range r.Findings {
		if (part == 0 || f.Part != nil && *f.Part == part) && (stream < 0 || f.Stream != nil && *f.Stream == stream) && strings.Contains(f.Problem, substr) {
			return true
		}
	}
	return false
}

func TestVerifyCleanSplit(t *testing.T) {
	code, stdout, stderr := runCLI(t, newRecorder(), append([]string{"verify", source(t)}, cleanParts(t)...)...)
	if code != cli.ExitOK {
		t.Fatalf("verify exit %d: %s\n%s", code, stderr, stdout)
	}
	var rep VerifyReport
	if err := json.Unmarshal([]byte(stdout), &rep); err != nil {
		t.Fatal(err)
	}
	if !rep.Clean || len(rep.Findings) != 0 || len(rep.Parts) != 4 || len(rep.Seams) != 3 {
		t.Fatalf("%+v", rep)
	}
	if rep.Parts[0].Cut != nil || rep.Parts[1].Cut == nil || *rep.Parts[1].Cut != 5 {
		t.Fatalf("cuts %v %v", rep.Parts[0].Cut, rep.Parts[1].Cut)
	}
}

func TestVerifyNeedsTwoParts(t *testing.T) {
	parts := cleanParts(t)
	if rep := verify(t, parts[:1]); rep.Clean || !hasFinding(rep, 0, -1, "at least two parts") {
		t.Fatalf("%+v", rep.Findings)
	}
	missing := append(slices.Clone(parts[:3]), filepath.Join(t.TempDir(), "gone.mkv"))
	if rep := verify(t, missing); rep.Clean || !hasFinding(rep, 4, -1, "gone.mkv") {
		t.Fatalf("%+v", rep.Findings)
	}
}

func TestVerifyDroppedPacket(t *testing.T) {
	parts := cleanParts(t)
	bad := filepath.Join(t.TempDir(), "dropped.mkv")
	synth(t, "ffmpeg", "-nostdin", "-v", "error", "-i", parts[1], "-map", "0", "-c", "copy",
		"-bsf:v", `noise=drop=eq(n\,30)`, bad)
	rep := verify(t, []string{parts[0], bad, parts[2], parts[3]})
	if rep.Clean || !hasFinding(rep, 2, 0, "packet 30") {
		t.Fatalf("findings:\n%s", problems(rep))
	}
}

func TestVerifyAlteredPCM(t *testing.T) {
	parts := cleanParts(t)
	bad := filepath.Join(t.TempDir(), "pcm.mkv")
	synth(t, "ffmpeg", "-nostdin", "-v", "error", "-i", parts[1], "-map", "0", "-c", "copy",
		"-bsf:a", `noise=drop=eq(n\,10)`, bad)
	rep := verify(t, []string{parts[0], bad, parts[2], parts[3]})
	if rep.Clean || !hasFinding(rep, 0, 1, "PCM bytes") {
		t.Fatalf("findings:\n%s", problems(rep))
	}
	if s := rep.Streams[1]; s.Compared != "bytes" || s.Identical {
		t.Fatalf("%+v", s)
	}
}

func TestVerifyNonKeyframeCut(t *testing.T) {
	parts := cleanParts(t)
	bad := filepath.Join(t.TempDir(), "midgop.mkv")
	synth(t, "ffmpeg", "-nostdin", "-v", "error", "-i", source(t), "-ss", "12.5", "-t", "2.5",
		"-copyinkf", "-map", "0", "-c", "copy", bad)
	rep := verify(t, []string{parts[0], parts[1], bad, parts[3]})
	if rep.Clean || !hasFinding(rep, 3, 0, "not a keyframe") {
		t.Fatalf("findings:\n%s", problems(rep))
	}
}

func TestVerifyShortPart(t *testing.T) {
	parts := cleanParts(t)
	dir := t.TempDir()
	synth(t, "mkvmerge", "-o", filepath.Join(dir, "short.mkv"), "--split", "parts:00:00:00-00:00:02", parts[3])
	short, err := filepath.Glob(filepath.Join(dir, "short*.mkv"))
	if err != nil || len(short) != 1 {
		t.Fatalf("short part: %v %v", short, err)
	}
	rep := verify(t, []string{parts[0], parts[1], parts[2], short[0]})
	if rep.Clean || !hasFinding(rep, 4, 0, "short") {
		t.Fatalf("findings:\n%s", problems(rep))
	}
}

func TestVerifyPlannedCut(t *testing.T) {
	// The clean case is every --go test: verify runs with the planned times.
	rep, err := Verify(context.Background(), newRecorder(), source(t), cleanParts(t), []float64{5, 10.01, 15})
	if err != nil || rep.Clean || !hasFinding(rep, 3, -1, "planned keyframe") || len(rep.Findings) != 1 {
		t.Fatalf("%v\n%s", err, problems(rep))
	}
}
