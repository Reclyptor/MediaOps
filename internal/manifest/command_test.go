package manifest

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/reclyptor/mediaops/internal/cli"
)

// runCmd runs Command the way the binary does and returns its stdout, stderr
// and exit code.
func runCmd(t *testing.T, cfg Config, args ...string) (stdout, stderr string, code int) {
	t.Helper()
	var out, errOut bytes.Buffer
	code = cli.Run(context.Background(), "mkv-manifest", args, cli.IO{Stdout: &out, Stderr: &errOut},
		func(ctx context.Context, args []string, streams cli.IO) error {
			return Command(ctx, cfg, args, streams)
		})
	return out.String(), errOut.String(), code
}

// oneJSON decodes stdout into v and fails unless it holds exactly one value.
func oneJSON(t *testing.T, stdout string, v any) {
	t.Helper()
	dec := json.NewDecoder(strings.NewReader(stdout))
	if err := dec.Decode(v); err != nil {
		t.Fatalf("stdout is not JSON: %v\n%s", err, stdout)
	}
	if err := dec.Decode(new(json.RawMessage)); !errors.Is(err, io.EOF) {
		t.Fatalf("stdout holds more than one JSON value (next: %v)\n%s", err, stdout)
	}
}

// wantFatal fails unless the run was a refusal with the given code: nothing on
// stdout and one FATAL line on stderr.
func wantFatal(t *testing.T, stdout, stderr string, code, want int) {
	t.Helper()
	if code != want || stdout != "" {
		t.Fatalf("code %d (want %d), stdout %q, stderr %q", code, want, stdout, stderr)
	}
	if !strings.HasPrefix(stderr, "mkv-manifest: FATAL: ") || strings.Count(stderr, "\n") != 1 || !strings.HasSuffix(stderr, "\n") {
		t.Fatalf("stderr is not one FATAL line: %q", stderr)
	}
}

func cmdCheckReport(t *testing.T, stdout string) CheckReport {
	t.Helper()
	var rep CheckReport
	oneJSON(t, stdout, &rep)
	return rep
}

func cmdExecReport(t *testing.T, stdout string) ExecReport {
	t.Helper()
	var rep ExecReport
	oneJSON(t, stdout, &rep)
	return rep
}

// cleanTitle is a one-op manifest that checks clean and, run, sets a title.
func cleanTitle(t *testing.T, e *xEnv) (manifest, media string) {
	t.Helper()
	media = writeMedia(t, e.path("nas/s/a.mkv"))
	return e.write(Op{Kind: KindTitle, Path: media, Title: "New"}), media
}

// missingSource is a one-op manifest whose source does not exist.
func missingSource(e *xEnv) string {
	return e.write(Op{Kind: KindRename, Src: e.path("nas/s/gone.mkv"), Dst: e.path("nas/s/b.mkv")})
}

func TestCommandVersionAndDispatch(t *testing.T) {
	cfg := newRig(t).cfg
	stdout, stderr, code := runCmd(t, cfg, "--version")
	if code != 0 || stderr != "" || !strings.HasPrefix(stdout, "mkv-manifest ") {
		t.Fatalf("code %d, stdout %q, stderr %q", code, stdout, stderr)
	}
	for _, args := range [][]string{nil, {"frobnicate"}, {"--help"}, {"gen"}, {"gen", "nothing"}} {
		stdout, stderr, code := runCmd(t, cfg, args...)
		wantFatal(t, stdout, stderr, code, cli.ExitUsage)
		if !strings.Contains(stderr, "usage: mkv-manifest") {
			t.Errorf("args %v: stderr %q", args, stderr)
		}
	}
}

func TestCommandCheck(t *testing.T) {
	t.Run("clean", func(t *testing.T) {
		e := newXEnv(t)
		path, _ := cleanTitle(t, e)
		stdout, stderr, code := runCmd(t, e.cfg, "check", path)
		if code != 0 || stderr != "" {
			t.Fatalf("code %d, stderr %q", code, stderr)
		}
		rep := cmdCheckReport(t, stdout)
		if !rep.Clean || rep.Ops != 1 || rep.Pending != 1 || len(rep.Findings) != 0 {
			t.Fatalf("report %+v", rep)
		}
	})

	t.Run("findings exit 1 with the report on stdout", func(t *testing.T) {
		e := newXEnv(t)
		stdout, stderr, code := runCmd(t, e.cfg, "check", missingSource(e))
		if code != cli.ExitIncomplete || stderr != "" {
			t.Fatalf("code %d, stderr %q", code, stderr)
		}
		rep := cmdCheckReport(t, stdout)
		if rep.Clean || len(rep.Findings) == 0 {
			t.Fatalf("report %+v", rep)
		}
	})

	t.Run("a schema finding is a finding, not a usage error", func(t *testing.T) {
		e := newXEnv(t)
		path := putFile(t, filepath.Join(e.plans, "bad.json"), `{"ops":[{"kind":"nonsense"}]}`)
		stdout, _, code := runCmd(t, e.cfg, "check", path)
		if rep := cmdCheckReport(t, stdout); code != cli.ExitIncomplete || rep.Clean {
			t.Fatalf("code %d, report %+v", code, rep)
		}
	})

	t.Run("--films before or after the manifest", func(t *testing.T) {
		e := newXEnv(t)
		path, _ := cleanTitle(t, e)
		for _, args := range [][]string{{"check", "--films", path}, {"check", path, "--films"}, {"check", path, "-films"}} {
			stdout, stderr, code := runCmd(t, e.cfg, args...)
			if code != 0 || stderr != "" || !cmdCheckReport(t, stdout).Films {
				t.Errorf("args %v: code %d, stdout %q, stderr %q", args, code, stdout, stderr)
			}
		}
		stdout, _, _ := runCmd(t, e.cfg, "check", path)
		if cmdCheckReport(t, stdout).Films {
			t.Error("films is on without the flag")
		}
	})

	t.Run("usage errors", func(t *testing.T) {
		e := newXEnv(t)
		path, _ := cleanTitle(t, e)
		for name, args := range map[string][]string{
			"no manifest":   {"check"},
			"two manifests": {"check", path, path},
			"unknown flag":  {"check", "--bogus", path},
			"flag only":     {"check", "--films"},
			"exec's flag":   {"check", "--go", path},
		} {
			t.Run(name, func(t *testing.T) {
				stdout, stderr, code := runCmd(t, e.cfg, args...)
				wantFatal(t, stdout, stderr, code, cli.ExitUsage)
				if !strings.Contains(stderr, "usage: mkv-manifest check MANIFEST [--films]") {
					t.Errorf("stderr %q", stderr)
				}
			})
		}
	})

	t.Run("a manifest that cannot be used exits 2", func(t *testing.T) {
		e := newXEnv(t)
		cases := map[string]string{
			"unreadable":       filepath.Join(e.plans, "nowhere.json"),
			"a directory":      e.plans,
			"not JSON":         putFile(t, filepath.Join(e.plans, "a.json"), "{not json"),
			"empty file":       putFile(t, filepath.Join(e.plans, "empty.json"), ""),
			"not an object":    putFile(t, filepath.Join(e.plans, "b.json"), `["ops"]`),
			"null":             putFile(t, filepath.Join(e.plans, "null.json"), `null`),
			"ops missing":      putFile(t, filepath.Join(e.plans, "c.json"), `{"name":"x"}`),
			"ops not an array": putFile(t, filepath.Join(e.plans, "d.json"), `{"ops":{}}`),
			"ops null":         putFile(t, filepath.Join(e.plans, "e.json"), `{"ops":null}`),
		}
		for name, path := range cases {
			t.Run(name, func(t *testing.T) {
				stdout, stderr, code := runCmd(t, e.cfg, "check", path)
				wantFatal(t, stdout, stderr, code, cli.ExitUsage)
			})
		}
	})

	t.Run("a manifest that is /dev/null exits 2", func(t *testing.T) {
		stdout, stderr, code := runCmd(t, newRig(t).cfg, "check", "/dev/null")
		wantFatal(t, stdout, stderr, code, cli.ExitUsage)
	})

	t.Run("archive ops with an unusable ARCHIVE_ROOT exit 3", func(t *testing.T) {
		for name, mutate := range map[string]func(*Config){
			"unset":   func(c *Config) { c.Paths.Archive = "" },
			"missing": func(c *Config) { c.Paths.Archive = filepath.Join(c.Paths.MediaRoot, "nas2", "nope") },
		} {
			t.Run(name, func(t *testing.T) {
				e := newXEnv(t)
				src := e.put("nas/p/a.mkv", "a")
				path := e.write(Op{Kind: KindArchiveFile, Src: src, Dst: e.archive + "/a.mkv"})
				mutate(&e.cfg)
				stdout, stderr, code := runCmd(t, e.cfg, "check", path)
				wantFatal(t, stdout, stderr, code, cli.ExitRefused)
				if !strings.Contains(stderr, "ARCHIVE_ROOT") {
					t.Errorf("stderr %q", stderr)
				}
			})
		}
	})

	t.Run("a Python-era journal exits 3", func(t *testing.T) {
		e := newXEnv(t)
		path, _ := cleanTitle(t, e)
		putFile(t, strings.TrimSuffix(path, ".json")+".journal", "3\n")
		stdout, stderr, code := runCmd(t, e.cfg, "check", path)
		wantFatal(t, stdout, stderr, code, cli.ExitRefused)
		if !strings.Contains(stderr, "Python-era journal") {
			t.Errorf("stderr %q", stderr)
		}
	})

	t.Run("it writes nothing", func(t *testing.T) {
		e := newXEnv(t)
		path, _ := cleanTitle(t, e)
		before := snapshot(t, filepath.Dir(e.media))
		if _, _, code := runCmd(t, e.cfg, "check", path); code != 0 {
			t.Fatalf("code %d", code)
		}
		if after := snapshot(t, filepath.Dir(e.media)); !reflect.DeepEqual(after, before) {
			t.Fatalf("tree changed: %v -> %v", before, after)
		}
	})
}

func TestCommandExec(t *testing.T) {
	t.Run("dry run reports pending and touches nothing", func(t *testing.T) {
		e := newXEnv(t)
		path, media := cleanTitle(t, e)
		before := string(mustRead(t, media))
		stdout, stderr, code := runCmd(t, e.cfg, "exec", path)
		if code != 0 || stderr != "" {
			t.Fatalf("code %d, stderr %q", code, stderr)
		}
		rep := cmdExecReport(t, stdout)
		if !rep.DryRun || rep.Applied != 0 || !rep.Check.Clean || !eqStrings(statuses(rep), []string{StatusPending}) {
			t.Fatalf("report %+v", rep)
		}
		if present(JournalPath(path)) || len(e.fake.calls) != 0 || string(mustRead(t, media)) != before {
			t.Fatal("the dry run touched something")
		}
	})

	t.Run("--go applies", func(t *testing.T) {
		e := newXEnv(t)
		path, media := cleanTitle(t, e)
		stdout, stderr, code := runCmd(t, e.cfg, "exec", "--go", path)
		if code != 0 || stderr != "" {
			t.Fatalf("code %d, stdout %q, stderr %q", code, stdout, stderr)
		}
		rep := cmdExecReport(t, stdout)
		if rep.DryRun || rep.Applied != 1 || rep.Error != "" || rep.FailedOp != nil {
			t.Fatalf("report %+v", rep)
		}
		if m, _, err := readFakeMedia(media); err != nil || m.Title != "New" {
			t.Fatalf("title %q, err %v", m.Title, err)
		}
		if got := journalEvents(t, path); !eqStrings(got, []string{"start", "begin 1", "done 1", "finish"}) {
			t.Fatalf("journal %v", got)
		}
	})

	t.Run("findings exit 1 with the report and touch nothing", func(t *testing.T) {
		for _, args := range [][]string{{"exec"}, {"exec", "--go"}} {
			e := newXEnv(t)
			path := missingSource(e)
			stdout, stderr, code := runCmd(t, e.cfg, append(args, path)...)
			if code != cli.ExitIncomplete || stderr != "" {
				t.Fatalf("args %v: code %d, stderr %q", args, code, stderr)
			}
			rep := cmdExecReport(t, stdout)
			if rep.Check.Clean || len(rep.Check.Findings) == 0 || rep.Applied != 0 {
				t.Fatalf("args %v: report %+v", args, rep)
			}
			if present(JournalPath(path)) || len(e.fake.calls) != 0 {
				t.Fatalf("args %v: findings did not stop the run", args)
			}
		}
	})

	t.Run("an op failure exits 1 with the failed op in the report", func(t *testing.T) {
		e := newXEnv(t)
		e.fake.ignoreTitle = true
		path, _ := cleanTitle(t, e)
		stdout, stderr, code := runCmd(t, e.cfg, "exec", "--go", path)
		if code != cli.ExitIncomplete || !strings.HasPrefix(stderr, "mkv-manifest: FATAL: ") {
			t.Fatalf("code %d, stderr %q", code, stderr)
		}
		rep := cmdExecReport(t, stdout)
		if rep.Error == "" || rep.FailedOp == nil || *rep.FailedOp != 1 || !eqStrings(statuses(rep), []string{StatusFailed}) {
			t.Fatalf("report %+v", rep)
		}
	})

	t.Run("a lock held by another run exits 3", func(t *testing.T) {
		e := newXEnv(t)
		path, media := cleanTitle(t, e)
		release, err := e.cfg.Lock(mustLoad(t, path))
		if err != nil {
			t.Fatal(err)
		}
		defer release()
		stdout, stderr, code := runCmd(t, e.cfg, "exec", "--go", path)
		wantFatal(t, stdout, stderr, code, cli.ExitRefused)
		if !strings.Contains(stderr, "another exec --go holds") {
			t.Errorf("stderr %q", stderr)
		}
		if m, _, err := readFakeMedia(media); err != nil || m.Title == "New" {
			t.Fatal("the refused run changed the file")
		}
		if _, _, code := runCmd(t, e.cfg, "exec", path); code != 0 {
			t.Errorf("a dry run needs no lock, got code %d", code)
		}
	})

	t.Run("a Python-era journal exits 3", func(t *testing.T) {
		e := newXEnv(t)
		path, _ := cleanTitle(t, e)
		putFile(t, strings.TrimSuffix(path, ".json")+".journal", "3\n")
		stdout, stderr, code := runCmd(t, e.cfg, "exec", "--go", path)
		wantFatal(t, stdout, stderr, code, cli.ExitRefused)
	})

	t.Run("flags before or after the manifest", func(t *testing.T) {
		for _, tc := range []struct {
			args    []string
			dry     bool
			applied int
		}{
			{[]string{"--go", "M"}, false, 1},
			{[]string{"M", "--go"}, false, 1},
			{[]string{"M", "-go"}, false, 1},
			{[]string{"--films", "M"}, true, 0},
			{[]string{"M", "--films"}, true, 0},
			{[]string{"--go", "--films", "M"}, false, 1},
			{[]string{"M", "--films", "--go"}, false, 1},
		} {
			e := newXEnv(t)
			path, _ := cleanTitle(t, e)
			args := []string{"exec"}
			for _, a := range tc.args {
				if a == "M" {
					a = path
				}
				args = append(args, a)
			}
			stdout, stderr, code := runCmd(t, e.cfg, args...)
			if code != 0 || stderr != "" {
				t.Fatalf("args %v: code %d, stdout %q, stderr %q", tc.args, code, stdout, stderr)
			}
			rep := cmdExecReport(t, stdout)
			if rep.DryRun != tc.dry || rep.Applied != tc.applied || rep.Check.Films != (strings.Contains(strings.Join(tc.args, " "), "--films")) {
				t.Errorf("args %v: report %+v", tc.args, rep)
			}
		}
	})

	t.Run("usage errors", func(t *testing.T) {
		e := newXEnv(t)
		path, _ := cleanTitle(t, e)
		for name, args := range map[string][]string{
			"no manifest":   {"exec"},
			"two manifests": {"exec", path, path},
			"unknown flag":  {"exec", "--bogus", path},
			"flags only":    {"exec", "--go", "--films"},
		} {
			t.Run(name, func(t *testing.T) {
				stdout, stderr, code := runCmd(t, e.cfg, args...)
				wantFatal(t, stdout, stderr, code, cli.ExitUsage)
				if !strings.Contains(stderr, "usage: mkv-manifest exec MANIFEST [--go] [--films]") {
					t.Errorf("stderr %q", stderr)
				}
			})
		}
		if present(JournalPath(path)) || len(e.fake.calls) != 0 {
			t.Fatal("a usage error ran something")
		}
	})

	t.Run("an unusable manifest exits 2 with nothing on stdout", func(t *testing.T) {
		e := newXEnv(t)
		for name, path := range map[string]string{
			"missing":     filepath.Join(e.plans, "nowhere.json"),
			"not JSON":    putFile(t, filepath.Join(e.plans, "a.json"), "nope"),
			"ops missing": putFile(t, filepath.Join(e.plans, "b.json"), `{}`),
			"/dev/null":   "/dev/null",
		} {
			t.Run(name, func(t *testing.T) {
				stdout, stderr, code := runCmd(t, e.cfg, "exec", "--go", path)
				wantFatal(t, stdout, stderr, code, cli.ExitUsage)
			})
		}
	})

	t.Run("archive ops with an unusable ARCHIVE_ROOT exit 3", func(t *testing.T) {
		e := newXEnv(t)
		src := e.put("nas/p/a.mkv", "a")
		path := e.write(Op{Kind: KindArchiveFile, Src: src, Dst: e.archive + "/a.mkv"})
		e.cfg.Paths.Archive = ""
		for _, args := range [][]string{{"exec", path}, {"exec", "--go", path}} {
			stdout, stderr, code := runCmd(t, e.cfg, args...)
			wantFatal(t, stdout, stderr, code, cli.ExitRefused)
		}
		if !present(src) {
			t.Fatal("the source moved")
		}
	})
}

func TestCommandGenRenameSeries(t *testing.T) {
	seriesDir := func(t *testing.T) string {
		dir := filepath.Join(t.TempDir(), "Old Show")
		putFile(t, filepath.Join(dir, "Season 1", "Old - S01E01 - Pilot - [2020-01-02].mkv"), "a")
		putFile(t, filepath.Join(dir, "Season 1", "Old - S01E02 - Next - [2020-01-09].mkv"), "bb")
		putFile(t, filepath.Join(dir, "Season 2", "Old - S02E01 - Again - [2020-02-02].mkv"), "c")
		return dir
	}

	t.Run("success prints one manifest Load accepts", func(t *testing.T) {
		cfg := newRig(t).cfg
		dir := seriesDir(t)
		stdout, stderr, code := runCmd(t, cfg, "gen", "rename-series", dir, "New Show")
		if code != 0 || stderr != "" {
			t.Fatalf("code %d, stderr %q", code, stderr)
		}
		gen := loadGenerated(t, stdout)
		if gen.Generated.By != "mkv-manifest gen rename-series" || gen.Name != "New Show" || len(gen.Ops) == 0 {
			t.Fatalf("generated %+v", gen)
		}
	})

	t.Run("--only and --strip-date, before or after the positionals", func(t *testing.T) {
		cfg := newRig(t).cfg
		dir := seriesDir(t)
		for _, tail := range [][]string{
			{"--only", "1", "--strip-date", dir, "New"},
			{dir, "New", "--only", "1", "--strip-date"},
			{dir, "--only=1", "New", "-strip-date"},
		} {
			stdout, stderr, code := runCmd(t, cfg, append([]string{"gen", "rename-series"}, tail...)...)
			if code != 0 || stderr != "" {
				t.Fatalf("%v: code %d, stderr %q", tail, code, stderr)
			}
			gen := loadGenerated(t, stdout)
			for _, op := range gen.Ops {
				if strings.Contains(op.Src, "Season 2") || strings.Contains(op.Dst, "[2020") {
					t.Errorf("%v: op %+v ignores --only or --strip-date", tail, op)
				}
			}
		}
	})

	t.Run("refusals exit 2 with the FATAL line only", func(t *testing.T) {
		cfg := newRig(t).cfg
		dir := seriesDir(t)
		empty := t.TempDir()
		file := putFile(t, filepath.Join(t.TempDir(), "file"), "x")
		noEpisodes := filepath.Join(t.TempDir(), "S")
		putFile(t, filepath.Join(noEpisodes, "Season 01", "notes.txt"), "x")
		padded := filepath.Join(t.TempDir(), "P")
		putFile(t, filepath.Join(padded, "Season 01", "New - S01E01.mkv"), "x")
		for name, args := range map[string][]string{
			"missing dir":      {dir + "/nowhere", "N"},
			"not a directory":  {file, "N"},
			"no Season dirs":   {empty, "N"},
			"no episodes":      {noEpisodes, "N"},
			"nothing to do":    {padded, "New"},
			"bad --only":       {dir, "N", "--only", "one"},
			"negative --only":  {dir, "N", "--only", "-1"},
			"empty --only":     {dir, "N", "--only", "1,,2"},
			"--only no season": {dir, "N", "--only", "9"},
			"slash in name":    {dir, "a/b"},
			"empty name":       {dir, ""},
			"no arguments":     {},
			"one argument":     {dir},
			"three arguments":  {dir, "N", "extra"},
			"unknown flag":     {dir, "N", "--bogus"},
		} {
			t.Run(name, func(t *testing.T) {
				stdout, stderr, code := runCmd(t, cfg, append([]string{"gen", "rename-series"}, args...)...)
				wantFatal(t, stdout, stderr, code, cli.ExitUsage)
			})
		}
	})
}

func TestCommandGenArchiveTree(t *testing.T) {
	t.Run("success prints one manifest Load accepts", func(t *testing.T) {
		e := newXEnv(t)
		src := e.mkdir("nas/_pending/rip")
		putFile(t, filepath.Join(src, "a.mkv"), "a")
		stdout, stderr, code := runCmd(t, e.cfg, "gen", "archive-tree", src, filepath.Join(e.archive, "rip"))
		if code != 0 || stderr != "" {
			t.Fatalf("code %d, stderr %q", code, stderr)
		}
		gen := loadGenerated(t, stdout)
		if gen.Generated.By != "mkv-manifest gen archive-tree" || gen.Name != "rip" {
			t.Fatalf("generated %+v", gen)
		}
	})

	t.Run("refusals exit 2 with the FATAL line only", func(t *testing.T) {
		cfg := newRig(t).cfg
		src := filepath.Join(t.TempDir(), "rip")
		putFile(t, filepath.Join(src, "a.mkv"), "a")
		file := putFile(t, filepath.Join(t.TempDir(), "file"), "x")
		empty := t.TempDir()
		dst := filepath.Join(t.TempDir(), "out")
		for name, args := range map[string][]string{
			"missing source":    {src + "/nowhere", dst},
			"source is a file":  {file, dst},
			"empty source":      {empty, dst},
			"dst inside source": {src, filepath.Join(src, "inner")},
			"no arguments":      {},
			"one argument":      {src},
			"three arguments":   {src, dst, "extra"},
			"unknown flag":      {src, dst, "--bogus"},
		} {
			t.Run(name, func(t *testing.T) {
				stdout, stderr, code := runCmd(t, cfg, append([]string{"gen", "archive-tree"}, args...)...)
				wantFatal(t, stdout, stderr, code, cli.ExitUsage)
			})
		}
	})
}

// loadGenerated checks that stdout is one manifest and that Load accepts it.
func loadGenerated(t *testing.T, stdout string) Generated {
	t.Helper()
	var gen Generated
	oneJSON(t, stdout, &gen)
	path := putFile(t, filepath.Join(t.TempDir(), "gen.json"), stdout)
	m, findings, err := Load(path)
	if err != nil || len(findings) != 0 {
		t.Fatalf("Load: %v %v", err, findings)
	}
	if len(m.Ops) != len(gen.Ops) || gen.Generated.By == "" {
		t.Fatalf("loaded %d ops of %d, generated.by %q", len(m.Ops), len(gen.Ops), gen.Generated.By)
	}
	return gen
}

// A generated manifest, written out and checked against the tree it came from,
// has nothing to report.
func TestCommandGenThenCheck(t *testing.T) {
	t.Run("rename-series", func(t *testing.T) {
		e := newXEnv(t)
		dir := e.mkdir("nas/shows/Old")
		putFile(t, filepath.Join(dir, "Season 1", "Old - S01E01 - Pilot - [2020-01-02].mkv"), "a")
		putFile(t, filepath.Join(dir, "Season 1", "Old - S01E02 - Next - [2020-01-09].mkv"), "bb")
		stdout, _, code := runCmd(t, e.cfg, "gen", "rename-series", dir, "New", "--strip-date")
		if code != 0 {
			t.Fatalf("gen: code %d", code)
		}
		assertCheckClean(t, e, stdout)
	})

	t.Run("archive-tree", func(t *testing.T) {
		e := newXEnv(t)
		src := e.mkdir("nas/_pending/rip")
		putFile(t, filepath.Join(src, "a.mkv"), "a")
		putFile(t, filepath.Join(src, "disc1", "extras", "b.mkv"), "bb")
		stdout, _, code := runCmd(t, e.cfg, "gen", "archive-tree", src, filepath.Join(e.archive, "rip"))
		if code != 0 {
			t.Fatalf("gen: code %d", code)
		}
		assertCheckClean(t, e, stdout)
	})
}

func assertCheckClean(t *testing.T, e *xEnv, generated string) {
	t.Helper()
	path := putFile(t, filepath.Join(e.plans, "gen.json"), generated)
	stdout, stderr, code := runCmd(t, e.cfg, "check", path)
	rep := cmdCheckReport(t, stdout)
	if code != 0 || stderr != "" || !rep.Clean {
		t.Fatalf("code %d, stderr %q, findings %+v", code, stderr, rep.Findings)
	}
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
