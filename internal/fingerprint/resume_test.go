//go:build integration

package fingerprint

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"syscall"
	"testing"

	"github.com/reclyptor/mediaops/internal/cli"
	"github.com/reclyptor/mediaops/kit/proc"
)

// roleEnv turns the test binary into mkv-fingerprint itself, so a test can
// start a batch as a process and SIGKILL it.
const roleEnv = "FINGERPRINT_TEST_ROLE"

func TestMain(m *testing.M) {
	if os.Getenv(roleEnv) == "cli" {
		cli.Main("mkv-fingerprint", func(ctx context.Context, args []string, streams cli.IO) error {
			return FingerprintCommand(ctx, &proc.Exec{}, args, streams)
		})
	}
	os.Exit(m.Run())
}

// synth runs ffmpeg directly to build lavfi test media. It lives in a _test
// file, like kit/frames' helper, so no shipped code can reach an encoder.
func synth(t *testing.T, args ...string) {
	t.Helper()
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Fatalf("ffmpeg is required to run these tests: %v", err)
	}
	if out, err := exec.Command("ffmpeg", append([]string{"-nostdin", "-v", "error"}, args...)...).CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg %q: %v\n%s", args, err, out)
	}
}

// clips builds six small, different clips and the jobs file for them. The
// first two have a preview.
func clips(t *testing.T) (jobs string, names []string) {
	t.Helper()
	dir := t.TempDir()
	sources := []string{
		"testsrc2=duration=5:size=160x90:rate=12",
		"mandelbrot=size=160x90:rate=12",
		"cellauto=size=160x90:rate=12",
		"rgbtestsrc=size=160x90:rate=12",
		"life=size=160x90:rate=12:ratio=0.2:seed=7",
		"smptebars=size=160x90:rate=12",
	}
	var lines []string
	for i, src := range sources {
		path := filepath.Join(dir, fmt.Sprintf("clip%d.mkv", i))
		synth(t, "-f", "lavfi", "-i", src, "-t", "5", "-c:v", "libx264", "-preset", "ultrafast", "-pix_fmt", "yuv420p", path)
		start := "none"
		if i < 2 {
			start = "2.5"
		}
		name := fmt.Sprintf("t%02d", i)
		names = append(names, name)
		lines = append(lines, path+"\t"+start+"\t"+name)
	}
	jobs = filepath.Join(dir, "jobs.tsv")
	if err := os.WriteFile(jobs, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return jobs, names
}

func fingerprintCmd(args ...string) *exec.Cmd {
	cmd := exec.Command(os.Args[0], append([]string{"batch"}, args...)...)
	cmd.Env = append(os.Environ(), roleEnv+"=cli")
	return cmd
}

func records(t *testing.T, dir string) map[string][]byte {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string][]byte{}
	for _, e := range entries {
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		out[e.Name()] = b
	}
	return out
}

func finishedNames(t *testing.T, dir string) []string {
	t.Helper()
	var out []string
	for name := range records(t, dir) {
		if strings.HasSuffix(name, ".json") && !strings.HasPrefix(name, ".") {
			out = append(out, strings.TrimSuffix(name, ".json"))
		}
	}
	sort.Strings(out)
	return out
}

// killMidBatch starts a one-worker batch and SIGKILLs it as soon as its first
// job has reported done. The `ok` line is printed after the record is renamed
// into place, so the kill always lands with at least one record on disk, and
// the five jobs still to run take far longer than the kill does.
func killMidBatch(t *testing.T, jobs, out string) {
	t.Helper()
	cmd := fingerprintCmd(jobs, out, "--workers", "1")
	stderr, err := cmd.StderrPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	line, err := bufio.NewReader(stderr).ReadString('\n')
	if err != nil || !strings.HasPrefix(line, "ok ") {
		cmd.Process.Kill()
		cmd.Wait()
		t.Fatalf("first progress line = %q, %v", line, err)
	}
	if err := cmd.Process.Signal(syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	_ = cmd.Wait()
}

func TestKilledBatchResumesToTheSameRecords(t *testing.T) {
	jobs, names := clips(t)

	whole := filepath.Join(t.TempDir(), "whole")
	cmd := fingerprintCmd(jobs, whole, "--workers", "2")
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("uninterrupted batch: %v\n%s", err, stderr.String())
	}
	if got := finishedNames(t, whole); !reflect.DeepEqual(got, names) {
		t.Fatalf("uninterrupted batch wrote %v, want %v", got, names)
	}

	// The kill is a race against a batch that is mostly finished only if the
	// machine stalls the test for the length of five decodes; try again then.
	var cut string
	var done []string
	for attempt := 0; attempt < 3 && cut == ""; attempt++ {
		dir := filepath.Join(t.TempDir(), "cut")
		killMidBatch(t, jobs, dir)
		done = finishedNames(t, dir)
		if len(done) >= 1 && len(done) < len(names) {
			cut = dir
		}
	}
	if cut == "" {
		t.Fatalf("three kills in a row found the batch empty or already finished (last: %v)", done)
	}

	cmd = fingerprintCmd(jobs, cut, "--workers", "1")
	stdout.Reset()
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("rerun: %v\n%s", err, stderr.String())
	}
	var rep BatchReport
	if err := json.Unmarshal(stdout.Bytes(), &rep); err != nil {
		t.Fatalf("rerun report: %v\n%s", err, stdout.String())
	}
	if !reflect.DeepEqual(rep.Skipped, done) || len(rep.Failed) != 0 || len(rep.Computed)+len(rep.Skipped) != len(names) {
		t.Errorf("rerun report = %+v, records before it: %v", rep, done)
	}

	want, got := records(t, whole), records(t, cut)
	if len(got) != len(want) {
		t.Errorf("resumed directory holds %d files, uninterrupted %d (a temp file left behind?)", len(got), len(want))
	}
	for name, b := range want {
		if !bytes.Equal(got[name], b) {
			t.Errorf("%s differs between the resumed and the uninterrupted run", name)
		}
	}

	for _, name := range []string{"t00", "t05"} {
		rec, err := Read(filepath.Join(cut, name+".json"))
		if err != nil {
			t.Fatal(err)
		}
		if len(rec.Body) < 8 || (name == "t00") != (rec.PreviewStart != nil && len(rec.Preview) >= 4) {
			t.Errorf("%s: body %d frames, preview %d", name, len(rec.Body), len(rec.Preview))
		}
	}
}
