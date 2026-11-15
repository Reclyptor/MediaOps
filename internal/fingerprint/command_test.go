package fingerprint

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reclyptor/mediaops/internal/cli"
)

type result struct {
	code           int
	stdout, stderr string
}

func fingerprintRun(t *testing.T, p *fakePiper, args ...string) result {
	t.Helper()
	return runCommand(t, "mkv-fingerprint", args, func(ctx context.Context, args []string, s cli.IO) error {
		return FingerprintCommand(ctx, p, args, s)
	})
}

func dupcheckRun(t *testing.T, args ...string) result {
	t.Helper()
	return runCommand(t, "mkv-dupcheck", args, func(ctx context.Context, args []string, s cli.IO) error {
		return DupcheckCommand(ctx, newFake(t), args, s)
	})
}

func runCommand(t *testing.T, tool string, args []string, run func(context.Context, []string, cli.IO) error) result {
	t.Helper()
	var out, errOut bytes.Buffer
	code := cli.Run(context.Background(), tool, args, cli.IO{Stdout: &out, Stderr: &errOut}, run)
	return result{code, out.String(), errOut.String()}
}

func decodeJSON(t *testing.T, s string, v any) {
	t.Helper()
	if err := json.Unmarshal([]byte(s), v); err != nil {
		t.Fatalf("stdout is not one JSON value: %v\n%s", err, s)
	}
}

func TestHashCommand(t *testing.T) {
	dir := t.TempDir()
	file := media(t, dir, "t03.mkv")[0]
	out := filepath.Join(dir, "t03.json")
	fake := newFake(t)

	res := fingerprintRun(t, fake, "hash", file, "1301.5", out)
	if res.code != 0 || res.stderr != "" {
		t.Fatalf("exit %d, stderr %q", res.code, res.stderr)
	}
	var sum HashSummary
	decodeJSON(t, res.stdout, &sum)
	if sum.Path != file || sum.Out != out || sum.Body != 12 || sum.Preview != 6 {
		t.Errorf("summary = %+v", sum)
	}
	rec, err := Read(out)
	if err != nil || rec.Path != file || *rec.PreviewStart != 1301.5 || len(rec.Body) != 12 || len(rec.Preview) != 6 {
		t.Errorf("record = %+v, %v", rec, err)
	}

	// An existing regular OUT is replaced; flags and positionals may mix.
	res = fingerprintRun(t, fake, "hash", file, "none", out)
	if res.code != 0 {
		t.Fatalf("replace: exit %d, %s", res.code, res.stderr)
	}
	if rec, _ := Read(out); rec.PreviewStart != nil || len(rec.Preview) != 0 {
		t.Errorf("record was not replaced: %+v", rec)
	}
}

func TestHashCommandUsageErrorsDecodeNothing(t *testing.T) {
	dir := t.TempDir()
	file := media(t, dir, "t.mkv")[0]
	if err := os.Mkdir(filepath.Join(dir, "d.json"), 0o755); err != nil {
		t.Fatal(err)
	}
	cases := map[string][]string{
		"no arguments":     {"hash"},
		"missing OUT":      {"hash", file, "none"},
		"extra argument":   {"hash", file, "none", filepath.Join(dir, "a.json"), "x"},
		"unknown flag":     {"hash", file, "none", filepath.Join(dir, "a.json"), "--fast"},
		"zero start":       {"hash", file, "0", filepath.Join(dir, "a.json")},
		"negative start":   {"hash", file, "-4", filepath.Join(dir, "a.json")},
		"junk start":       {"hash", file, "soon", filepath.Join(dir, "a.json")},
		"FILE missing":     {"hash", filepath.Join(dir, "nope.mkv"), "none", filepath.Join(dir, "a.json")},
		"FILE a directory": {"hash", dir, "none", filepath.Join(dir, "a.json")},
		"OUT not json":     {"hash", file, "none", filepath.Join(dir, "a.mkv")},
		"OUT is media":     {"hash", file, "none", file},
		"OUT a directory":  {"hash", file, "none", filepath.Join(dir, "d.json")},
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			fake := newFake(t)
			res := fingerprintRun(t, fake, args...)
			if res.code != cli.ExitUsage || res.stdout != "" || !strings.Contains(res.stderr, "mkv-fingerprint: FATAL: ") {
				t.Errorf("exit %d, stdout %q, stderr %q", res.code, res.stdout, res.stderr)
			}
			if n := len(fake.decodes()); n != 0 {
				t.Errorf("decoded %d times before refusing", n)
			}
		})
	}
}

func TestHashCommandWritesNothingWithoutFrames(t *testing.T) {
	dir := t.TempDir()
	file := media(t, dir, "t.mkv")[0]
	out := filepath.Join(dir, "t.json")
	if err := os.WriteFile(out, []byte("previous"), 0o644); err != nil {
		t.Fatal(err)
	}
	for name, fake := range map[string]*fakePiper{
		"no body frames":    {silent: map[string]bool{file: true}},
		"no preview frames": {silentPreview: map[string]bool{file: true}},
		"ffmpeg fails":      {failing: map[string]string{file: "moov atom not found"}},
	} {
		fake.t, fake.raw = t, grayFrames(t)
		res := fingerprintRun(t, fake, "hash", file, "10", out)
		if res.code != cli.ExitInternal || res.stdout != "" {
			t.Errorf("%s: exit %d, stdout %q", name, res.code, res.stdout)
		}
		if b, _ := os.ReadFile(out); string(b) != "previous" {
			t.Errorf("%s: the old file was replaced by %q", name, b)
		}
	}
}

func TestBatchCommand(t *testing.T) {
	dir := t.TempDir()
	files := media(t, dir, "a.mkv", "b.mkv", "c.mkv")
	jobs := writeJobs(t, files[0]+"\t100\tt00", files[1]+"\tnone\tt01", files[2]+"\tnone\tt02")
	out := filepath.Join(dir, "fp")
	fake := newFake(t)
	fake.failing = map[string]string{files[2]: "boom"}

	res := fingerprintRun(t, fake, "batch", jobs, out, "--workers", "1")
	if res.code != cli.ExitIncomplete {
		t.Fatalf("exit %d, stderr %q", res.code, res.stderr)
	}
	var rep BatchReport
	decodeJSON(t, res.stdout, &rep)
	if rep.Jobs != 3 || rep.OutDir != out || len(rep.Computed) != 2 || len(rep.Failed) != 1 || rep.Failed[0].Name != "t02" {
		t.Errorf("report = %+v", rep)
	}
	if want := "ok t00\nok t01\nFAIL t02: "; !strings.HasPrefix(res.stderr, want) {
		t.Errorf("stderr = %q, want it to start %q", res.stderr, want)
	}
	if info, err := os.Stat(out); err != nil || info.Mode().Perm() != 0o770 {
		t.Errorf("OUTDIR = %v, %v", info, err)
	}

	fake.failing = nil
	res = fingerprintRun(t, fake, "batch", dir, out) // a directory is not a jobs file
	if res.code != cli.ExitUsage {
		t.Errorf("exit %d for a directory as the jobs file", res.code)
	}
	res = fingerprintRun(t, fake, "batch", jobs, out)
	if res.code != 0 {
		t.Fatalf("rerun: exit %d, stderr %q", res.code, res.stderr)
	}
	decodeJSON(t, res.stdout, &rep)
	if len(rep.Skipped) != 2 || len(rep.Computed) != 1 || rep.Computed[0] != "t02" {
		t.Errorf("rerun report = %+v", rep)
	}
}

func TestBatchCommandUsageErrors(t *testing.T) {
	dir := t.TempDir()
	file := media(t, dir, "a.mkv")[0]
	jobs := writeJobs(t, file+"\tnone\tt00")
	bad := writeJobs(t, file+"\tnone")
	cases := map[string][]string{
		"no arguments":       {"batch"},
		"one argument":       {"batch", jobs},
		"zero workers":       {"batch", jobs, filepath.Join(dir, "o"), "--workers", "0"},
		"junk workers":       {"batch", jobs, filepath.Join(dir, "o"), "--workers", "many"},
		"bad jobs":           {"batch", bad, filepath.Join(dir, "o")},
		"missing parent":     {"batch", jobs, filepath.Join(dir, "x", "o")},
		"OUTDIR is file":     {"batch", jobs, file},
		"missing jobs":       {"batch", filepath.Join(dir, "nope.tsv"), filepath.Join(dir, "o")},
		"unknown subcommand": {"scan"},
		"nothing":            {},
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			fake := newFake(t)
			res := fingerprintRun(t, fake, args...)
			if res.code != cli.ExitUsage || res.stdout != "" || !strings.Contains(res.stderr, "FATAL") {
				t.Errorf("exit %d, stdout %q, stderr %q", res.code, res.stdout, res.stderr)
			}
			if len(fake.decodes()) != 0 {
				t.Error("decoded before refusing")
			}
		})
	}
	if _, err := os.Stat(filepath.Join(dir, "o")); err == nil {
		t.Error("a refused batch created OUTDIR")
	}
}

func TestBatchCommandInterrupted(t *testing.T) {
	dir := t.TempDir()
	files := media(t, dir, "a.mkv", "b.mkv")
	jobs := writeJobs(t, files[0]+"\tnone\tt00", files[1]+"\tnone\tt01")
	ctx, cancel := context.WithCancel(context.Background())
	fake := newFake(t)
	fake.onCall = func(string, bool) error { cancel(); return nil }
	var out, errOut bytes.Buffer
	code := cli.Run(ctx, "mkv-fingerprint", []string{"batch", jobs, filepath.Join(dir, "fp"), "--workers", "1"},
		cli.IO{Stdout: &out, Stderr: &errOut},
		func(ctx context.Context, args []string, s cli.IO) error {
			return FingerprintCommand(ctx, fake, args, s)
		})
	if code != cli.ExitInternal || out.Len() != 0 || !strings.Contains(errOut.String(), "interrupted") {
		t.Errorf("exit %d, stdout %q, stderr %q", code, out.String(), errOut.String())
	}
}

func TestMatchCommand(t *testing.T) {
	f := loadPython(t)
	dir := writeDir(t, f.records())
	res := fingerprintRun(t, newFake(t), "match", dir)
	if res.code != cli.ExitIncomplete {
		t.Fatalf("exit %d, stderr %q", res.code, res.stderr)
	}
	var rep MatchReport
	decodeJSON(t, res.stdout, &rep)
	if rep.Tracks != 7 || rep.Clean || len(rep.Findings) != 1 {
		t.Errorf("report = %+v", rep)
	}

	clean := writeDir(t, map[string]Record{
		"a": NewRecord("/m/a.mkv", ptr(10.0), []uint64{farHash(5)}, []uint64{farHash(1)}),
		"b": NewRecord("/m/b.mkv", ptr(10.0), []uint64{farHash(1), farHash(3)}, []uint64{farHash(2)}),
		"c": NewRecord("/m/c.mkv", nil, []uint64{farHash(2)}, nil),
	})
	res = fingerprintRun(t, newFake(t), "match", clean)
	if res.code != 0 {
		t.Errorf("clean chain: exit %d, stdout %s", res.code, res.stdout)
	}
}

func TestMatchCommandUsageErrors(t *testing.T) {
	one := writeDir(t, map[string]Record{"a": NewRecord("/m/a.mkv", nil, []uint64{1}, nil)})
	none := t.TempDir()
	python := writeDir(t, map[string]Record{"a": NewRecord("/m/a.mkv", nil, []uint64{1}, nil), "b": NewRecord("/m/b.mkv", nil, []uint64{2}, nil)})
	if err := os.WriteFile(filepath.Join(python, "old.json"), []byte(`{"path":"/m/o.mkv","preview_start":null,"body":[1],"preview":[]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	cases := map[string]struct {
		args []string
		want string
	}{
		"one record":    {[]string{"match", one}, "at least 2"},
		"empty":         {[]string{"match", none}, "0 fingerprint"},
		"python record": {[]string{"match", python}, "old.json"},
		"missing":       {[]string{"match", filepath.Join(none, "x")}, "cannot read"},
		"no argument":   {[]string{"match"}, "usage"},
		"two arguments": {[]string{"match", one, none}, "usage"},
	}
	for name, c := range cases {
		res := fingerprintRun(t, newFake(t), c.args...)
		if res.code != cli.ExitUsage || res.stdout != "" || !strings.Contains(res.stderr, c.want) {
			t.Errorf("%s: exit %d, stdout %q, stderr %q", name, res.code, res.stdout, res.stderr)
		}
	}
}

func TestDupcheckCommand(t *testing.T) {
	f := loadPython(t)
	dir := writeDir(t, f.records())
	res := dupcheckRun(t, dir, "t05")
	if res.code != 0 || res.stderr != "" {
		t.Fatalf("exit %d, stderr %q", res.code, res.stderr)
	}
	var rep DupReport
	decodeJSON(t, res.stdout, &rep)
	if rep.Track != "t05" || len(rep.Scores) != 6 || rep.Scores[0].Track != "t05b" || rep.Control == nil {
		t.Errorf("report = %+v", rep)
	}

	for name, args := range map[string][]string{
		"absent track": {dir, "t99"},
		"no track":     {dir},
		"extra":        {dir, "t05", "t00"},
		"missing dir":  {filepath.Join(dir, "nope"), "t05"},
		"unknown flag": {dir, "t05", "--all"},
	} {
		res := dupcheckRun(t, args...)
		if res.code != cli.ExitUsage || res.stdout != "" || !strings.Contains(res.stderr, "mkv-dupcheck: FATAL") {
			t.Errorf("%s: exit %d, stdout %q, stderr %q", name, res.code, res.stdout, res.stderr)
		}
	}

	if err := os.WriteFile(filepath.Join(dir, "bad.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if res := dupcheckRun(t, dir, "t05"); res.code != cli.ExitUsage || !strings.Contains(res.stderr, "bad.json") {
		t.Errorf("bad record: exit %d, stderr %q", res.code, res.stderr)
	}
}
