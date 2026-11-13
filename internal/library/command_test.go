package library

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reclyptor/mediaops/internal/cli"
)

// runCmd runs a command body the way the binary does and returns its stdout,
// stderr and exit code.
func runCmd(t *testing.T, tool string, body func(context.Context, Deps, []string, cli.IO) error, d Deps, args ...string) (string, string, int) {
	t.Helper()
	var out, errOut bytes.Buffer
	code := cli.Run(context.Background(), tool, args, cli.IO{Stdout: &out, Stderr: &errOut}, func(ctx context.Context, args []string, streams cli.IO) error {
		return body(ctx, d, args, streams)
	})
	return out.String(), errOut.String(), code
}

func manifestFile(t *testing.T, v any) string {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return putFile(t, filepath.Join(t.TempDir(), "manifest.json"), string(data))
}

func TestPromoteCommand(t *testing.T) {
	t.Run("version and usage", func(t *testing.T) {
		r := newRig(t)
		out, _, code := runCmd(t, "mkv-promote", PromoteCommand, r.d, "--version")
		if code != 0 || !strings.HasPrefix(out, "mkv-promote ") {
			t.Fatalf("code %d, stdout %q", code, out)
		}
		for _, args := range [][]string{nil, {"a.json", "b.json"}, {"--bogus", "a.json"}, {"--go"}} {
			_, stderr, code := runCmd(t, "mkv-promote", PromoteCommand, r.d, args...)
			if code != cli.ExitUsage || !strings.Contains(stderr, "mkv-promote: FATAL: usage: mkv-promote MANIFEST.json [--go]") {
				t.Errorf("args %v: code %d, stderr %q", args, code, stderr)
			}
		}
	})

	t.Run("an unreadable or malformed manifest names itself and exits 2", func(t *testing.T) {
		r := newRig(t)
		_, stderr, code := runCmd(t, "mkv-promote", PromoteCommand, r.d, "/nonexistent.json")
		if code != cli.ExitUsage || !strings.Contains(stderr, "cannot read the manifest") {
			t.Errorf("code %d, stderr %q", code, stderr)
		}
		bad := manifestFile(t, []map[string]string{{"src": "a", "dst": "b"}, {"scr": "a"}})
		_, stderr, code = runCmd(t, "mkv-promote", PromoteCommand, r.d, bad)
		if code != cli.ExitUsage || !strings.Contains(stderr, "entry 1") {
			t.Errorf("code %d, stderr %q", code, stderr)
		}
	})

	t.Run("dry run prints one JSON report and moves nothing", func(t *testing.T) {
		r := newRig(t)
		entries := threeEpisodes(t, r, pend)
		path := manifestFile(t, entries)
		out, stderr, code := runCmd(t, "mkv-promote", PromoteCommand, r.d, path)
		if code != 0 || stderr != "" {
			t.Fatalf("code %d, stderr %q", code, stderr)
		}
		var rep PromoteReport
		if err := json.Unmarshal([]byte(out), &rep); err != nil || !rep.DryRun || rep.Files != 3 {
			t.Fatalf("stdout %q: %v", out, err)
		}
		if !r.exists(r.path(pend + "/t01.mkv")) {
			t.Fatal("the dry run moved a file")
		}
	})

	t.Run("--go acts, before or after the manifest", func(t *testing.T) {
		for _, args := range [][]string{{"--go", "M"}, {"M", "--go"}, {"M", "-go"}} {
			r := newRig(t)
			path := manifestFile(t, threeEpisodes(t, r, pend))
			for i, a := range args {
				if a == "M" {
					args[i] = path
				}
			}
			out, stderr, code := runCmd(t, "mkv-promote", PromoteCommand, r.d, args...)
			var rep PromoteReport
			if code != 0 || json.Unmarshal([]byte(out), &rep) != nil || rep.DryRun || rep.Moved != 3 {
				t.Fatalf("args %v: code %d, stdout %q, stderr %q", args, code, out, stderr)
			}
		}
	})

	t.Run("a refusal before anything moves prints no report", func(t *testing.T) {
		r := newRig(t)
		entries := threeEpisodes(t, r, pend)
		r.put(pend+"/stray.mkv", "x")
		out, stderr, code := runCmd(t, "mkv-promote", PromoteCommand, r.d, "--go", manifestFile(t, entries))
		if code != cli.ExitUsage || out != "" || !strings.Contains(stderr, "unaccounted for") {
			t.Fatalf("code %d, stdout %q, stderr %q", code, out, stderr)
		}
	})

	t.Run("a failure after the first move prints the report and exits non-zero", func(t *testing.T) {
		r := newRig(t)
		entries := threeEpisodes(t, r, pend)
		r.put(pend+"/t02.mkv", "BROKEN")
		out, stderr, code := runCmd(t, "mkv-promote", PromoteCommand, r.d, "--go", manifestFile(t, entries))
		var rep PromoteReport
		if code != cli.ExitIncomplete || json.Unmarshal([]byte(out), &rep) != nil || len(rep.DecodeFailures) != 1 || rep.Error == "" {
			t.Fatalf("code %d, stdout %q, stderr %q", code, out, stderr)
		}
		if !strings.Contains(stderr, "mkv-promote: FATAL: 1 of 3 files failed the decode check") {
			t.Errorf("stderr %q", stderr)
		}
	})
}

func TestArchiveCommand(t *testing.T) {
	t.Run("version and usage", func(t *testing.T) {
		r := newRig(t)
		out, _, code := runCmd(t, "mkv-archive", ArchiveCommand, r.d, "--version")
		if code != 0 || !strings.HasPrefix(out, "mkv-archive ") {
			t.Fatalf("code %d, stdout %q", code, out)
		}
		_, stderr, code := runCmd(t, "mkv-archive", ArchiveCommand, r.d)
		if code != cli.ExitUsage || !strings.Contains(stderr, "usage: mkv-archive MANIFEST.json [--go]") {
			t.Fatalf("code %d, stderr %q", code, stderr)
		}
	})

	t.Run("dry run, then --go", func(t *testing.T) {
		r := newRig(t)
		r.put("box/anime/X/a.mkv", episode("a"))
		path := manifestFile(t, []ArchiveEntry{{Path: "box/anime/X/a.mkv"}})

		out, _, code := runCmd(t, "mkv-archive", ArchiveCommand, r.d, path)
		var rep ArchiveReport
		if code != 0 || json.Unmarshal([]byte(out), &rep) != nil || !rep.DryRun || rep.ArchiveRoot != r.archive {
			t.Fatalf("code %d, stdout %q", code, out)
		}
		if !r.exists(r.path("box/anime/X/a.mkv")) {
			t.Fatal("the dry run moved a file")
		}

		out, stderr, code := runCmd(t, "mkv-archive", ArchiveCommand, r.d, path, "--go")
		rep = ArchiveReport{}
		if code != 0 || json.Unmarshal([]byte(out), &rep) != nil || rep.DryRun || rep.Archived != 1 {
			t.Fatalf("code %d, stdout %q, stderr %q", code, out, stderr)
		}
		if r.read(filepath.Join(r.archive, "anime", "X", "a.mkv")) != episode("a") {
			t.Fatal("the file is not in the archive")
		}
	})

	t.Run("a missing ARCHIVE_ROOT is refused with exit 3", func(t *testing.T) {
		r := newRig(t)
		r.put("box/anime/X/a.mkv", "x")
		r.d.Paths.Archive = ""
		out, stderr, code := runCmd(t, "mkv-archive", ArchiveCommand, r.d, manifestFile(t, []ArchiveEntry{{Path: "box/anime/X/a.mkv"}}), "--go")
		if code != cli.ExitRefused || out != "" || !strings.Contains(stderr, "ARCHIVE_ROOT is not set") {
			t.Fatalf("code %d, stdout %q, stderr %q", code, out, stderr)
		}
		if !r.exists(r.path("box/anime/X/a.mkv")) {
			t.Fatal("a refused archive moved a file")
		}
	})
}

// TestReportsAreValidJSONWithNoNulls: lists are always arrays, so a skill can
// iterate them without a null check.
func TestReportsAreValidJSONWithNoNulls(t *testing.T) {
	r := newRig(t)
	path := manifestFile(t, []PromoteEntry{keep("box/x.mkv")})
	out, _, code := runCmd(t, "mkv-promote", PromoteCommand, r.d, path)
	if code != 0 || strings.Contains(out, "null") {
		t.Fatalf("code %d, stdout %s", code, out)
	}
}
