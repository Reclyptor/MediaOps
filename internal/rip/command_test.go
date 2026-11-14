package rip

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/reclyptor/mediaops/internal/cli"
	"github.com/reclyptor/mediaops/internal/lease"
	"github.com/reclyptor/mediaops/internal/runstore"
)

// newCmdRig is a rig set up for running commands in this process: a staging
// tree that passes the NAS guard, a lease of its own, a licence key, and no
// program to detach into.
func newCmdRig(t *testing.T, serials ...string) *rig {
	return newRig(t, 1, serials...).forCommands()
}

func (g *rig) forCommands() *rig {
	g.t.Helper()
	g.deps.Paths.StatDev = fakeStatDev
	g.deps.LeasePath = filepath.Join(g.t.TempDir(), "lease")
	g.store.Live = leaseLiveness{path: g.deps.LeasePath}
	g.deps.PollEvery = 5 * time.Millisecond
	g.deps.Exe = "/nonexistent/mkv-rip"
	g.t.Setenv("HOME", g.t.TempDir())
	g.t.Setenv("MAKEMKV_KEY", "test-key")
	g.t.Setenv(cli.RoleEnv, cli.RoleRip)
	return g
}

// mkvRip runs a command as the program would, and returns what it printed and
// how it ended.
func (g *rig) mkvRip(stdin string, args ...string) (stdout, stderr string, code int) {
	g.t.Helper()
	var out, errOut strings.Builder
	code = cli.Run(context.Background(), "mkv-rip", args, cli.IO{Stdin: strings.NewReader(stdin), Stdout: &out, Stderr: &errOut},
		func(ctx context.Context, args []string, streams cli.IO) error {
			return Command(ctx, g.deps, args, streams)
		})
	return out.String(), errOut.String(), code
}

// putRun stores a run in the given state, started at the given minute.
func (g *rig) putRun(id string, state runstore.RunState, minute int) *runstore.State {
	g.t.Helper()
	st := &runstore.State{
		RunID: id, Kind: runstore.KindRip, State: state, StartedAt: time.Date(2026, 11, 13, 10, minute, 0, 0, time.UTC),
		Discs: []runstore.Disc{{Index: 0, Serial: "SERIAL00", State: runstore.DiscDone}},
	}
	if state.Terminal() {
		st.FinishedAt = &st.StartedAt
	}
	if state == runstore.RunIncomplete {
		st.Reason = new("one disc failed")
	}
	if err := g.store.Create(st); err != nil {
		g.t.Fatal(err)
	}
	return st
}

// holdLease takes the drive lease the way a controller does.
func (g *rig) holdLease(kind, runID string) *lease.Lease {
	g.t.Helper()
	l, err := lease.Acquire(context.Background(), g.deps.LeasePath, lease.Holder{Kind: kind, RunID: runID, PID: os.Getpid(), StartedAt: g.clock.Now()}, 0)
	if err != nil {
		g.t.Fatal(err)
	}
	g.t.Cleanup(func() { l.Release() })
	return l
}

const unknownRun = "20260101-000000-abcd"

func TestExitCodes(t *testing.T) {
	validPlan := `{"discs":[{"serial":"SERIAL00","category":"anime","series":"Penguindrum","season":1,"disc":1}]}`
	tests := []struct {
		name   string
		setup  func(g *rig)
		stdin  string
		args   []string
		code   int
		stderr string // a substring of the one FATAL line; "" when nothing is printed
	}{
		{name: "no arguments", code: 2, stderr: "mkv-rip: FATAL: usage: mkv-rip start PLAN.json|-"},
		{name: "unknown command", args: []string{"bogus"}, code: 2, stderr: `unknown command "bogus"; usage: mkv-rip start`},
		{name: "unknown flag", args: []string{"identify", "--bogus"}, code: 2, stderr: "identify: flag provided but not defined: -bogus"},
		{name: "a flag out of range", args: []string{"identify", "--min-title-seconds", "0"}, code: 2, stderr: "--min-title-seconds must be at least 1"},
		{name: "a serial twice", args: []string{"identify", "A", "B", "A"}, code: 2, stderr: "serial A is given twice"},
		{name: "start without a plan", args: []string{"start"}, code: 2, stderr: "usage: mkv-rip start PLAN.json|-"},
		{name: "a plan that is not there", args: []string{"start", "/nonexistent/plan.json"}, code: 2, stderr: "cannot read the plan /nonexistent/plan.json"},
		{name: "a plan with nothing in it", args: []string{"start", "-"}, stdin: `{"discs":[]}`, code: 2, stderr: "bad plan -: plan.discs must be a non-empty list"},
		{name: "a plan naming its bad entry", args: []string{"start", "-"}, stdin: `{"discs":[{"serial":"S","category":"anime","series":"X","disc":0}]}`, code: 2, stderr: "discs[0]"},
		{name: "unknown run: status", args: []string{"status", unknownRun}, code: 2, stderr: "no such run"},
		{name: "unknown run: wait", args: []string{"wait", unknownRun}, code: 2, stderr: "no such run"},
		{name: "unknown run: stop", args: []string{"stop", unknownRun}, code: 2, stderr: "no such run"},
		{name: "unknown run: finalize", args: []string{"finalize", unknownRun, "--dry-run"}, code: 2, stderr: "no such run"},
		{name: "a run id that is not one", args: []string{"status", "../etc"}, code: 2, stderr: "is not a run id"},
		{name: "no runs to show", args: []string{"status"}, code: 2, stderr: "there are no runs"},
		{
			name: "missing MAKEMKV_KEY", args: []string{"start", "-"}, stdin: validPlan, code: 3, stderr: "MAKEMKV_KEY is not set",
			setup: func(g *rig) { g.t.Setenv("MAKEMKV_KEY", "") },
		},
		{
			name: "unusable MAKEMKV_KEY", args: []string{"identify"}, code: 3, stderr: "MAKEMKV_KEY contains",
			setup: func(g *rig) { g.t.Setenv("MAKEMKV_KEY", `a"b`) },
		},
		{
			name: "staging on the root filesystem", args: []string{"list"}, code: 3, stderr: "is on the pod's root filesystem, so the NAS mount is missing",
			setup: func(g *rig) { g.deps.Paths.StatDev = nil },
		},
		{
			name: "staging missing", args: []string{"status"}, code: 3, stderr: "the staging tree does not exist",
			setup: func(g *rig) { g.deps.Paths.MediaRoot = filepath.Join(g.t.TempDir(), "nowhere") },
		},
		{
			name: "staging guard before a start", args: []string{"start", "-"}, stdin: validPlan, code: 3, stderr: "the staging tree does not exist",
			setup: func(g *rig) { g.deps.Paths.MediaRoot = filepath.Join(g.t.TempDir(), "nowhere") },
		},
		{
			name: "wait on a complete run", args: []string{"wait", "20261113-100000-0001"}, code: 0,
			setup: func(g *rig) { g.putRun("20261113-100000-0001", runstore.RunComplete, 0) },
		},
		{
			name: "wait on an incomplete run", args: []string{"wait", "20261113-100000-0002"}, code: 1,
			setup: func(g *rig) { g.putRun("20261113-100000-0002", runstore.RunIncomplete, 0) },
		},
		{name: "list with nothing in it", args: []string{"list"}, code: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := newCmdRig(t, "SERIAL00")
			if tt.setup != nil {
				tt.setup(g)
			}
			_, stderr, code := g.mkvRip(tt.stdin, tt.args...)

			if code != tt.code {
				t.Errorf("exit code = %d, want %d; stderr %q", code, tt.code, stderr)
			}
			if tt.stderr == "" {
				if stderr != "" {
					t.Errorf("stderr = %q, want nothing", stderr)
				}
				return
			}
			if !strings.HasPrefix(stderr, "mkv-rip: FATAL: ") || strings.Count(stderr, "\n") != 1 || !strings.Contains(stderr, tt.stderr) {
				t.Errorf("stderr = %q, want one FATAL line containing %q", stderr, tt.stderr)
			}
			if strings.Contains(stderr, "test-key") {
				t.Errorf("the licence key is in %q", stderr)
			}
		})
	}
}

func TestRoleGuard(t *testing.T) {
	commands := [][]string{
		nil,
		{"bogus"},
		{"start", "-"},
		{"identify"},
		{"status"},
		{"wait", "20261113-100000-0001"},
		{"list"},
		{"finalize", "20261113-100000-0001"},
		{"stop", "20261113-100000-0001"},
		{controllerArg},
	}
	for _, role := range []struct {
		name  string
		set   bool
		value string
	}{{"unset", false, ""}, {"empty", true, ""}, {"library", true, "library"}, {"rip with a suffix", true, "ripper"}} {
		t.Run(role.name, func(t *testing.T) {
			g := newCmdRig(t, "SERIAL00")
			// A running run whose lease is not held is what a pod without the
			// lease would mark dead on any read.
			id := "20261113-100000-0001"
			g.putRun(id, runstore.RunRunning, 0)
			stateFile := filepath.Join(g.paths.RipsRoot(), id, "state.json")
			before, err := os.ReadFile(stateFile)
			if err != nil {
				t.Fatal(err)
			}
			t.Setenv(cli.RoleEnv, role.value) // registers the restore
			if !role.set {
				os.Unsetenv(cli.RoleEnv)
			}

			for _, args := range commands {
				stdout, stderr, code := g.mkvRip(`{"discs":[]}`, args...)
				if code != cli.ExitRefused || stdout != "" {
					t.Errorf("mkv-rip %v: exit %d, stdout %q, stderr %q; want exit 3 and no output", args, code, stdout, stderr)
				}
				if want := "mkv-rip: FATAL: "; !strings.HasPrefix(stderr, want) || strings.Count(stderr, "\n") != 1 ||
					!strings.Contains(stderr, "kubectl -n mediaops exec deploy/mediaops-rip -- mkv-rip") {
					t.Errorf("mkv-rip %v: stderr = %q, want one FATAL line naming the ripper", args, stderr)
				}
			}

			after, err := os.ReadFile(stateFile)
			if err != nil || string(after) != string(before) {
				t.Errorf("the run's record changed (%v):\n%s\n%s", err, before, after)
			}
			if fileExists(g.deps.LeasePath) {
				t.Error("the lease file was created")
			}
			if len(g.runner.calls) != 0 {
				t.Errorf("programs ran: %v", g.runner.calls)
			}
		})
	}

	t.Run("version needs no role", func(t *testing.T) {
		g := newCmdRig(t)
		t.Setenv(cli.RoleEnv, "")
		stdout, _, code := g.mkvRip("", "--version")
		if code != cli.ExitOK || !strings.HasPrefix(stdout, "mkv-rip ") {
			t.Errorf("--version: exit %d, stdout %q", code, stdout)
		}
	})

	t.Run("rip is let through", func(t *testing.T) {
		g := newCmdRig(t)
		if _, stderr, code := g.mkvRip("", "list"); code != cli.ExitOK {
			t.Errorf("list as the ripper: exit %d, stderr %q", code, stderr)
		}
	})
}

func TestWaitPrintsFinalState(t *testing.T) {
	for _, state := range []runstore.RunState{runstore.RunComplete, runstore.RunIncomplete} {
		t.Run(string(state), func(t *testing.T) {
			g := newCmdRig(t)
			want := g.putRun("20261113-100000-0001", state, 0)
			stdout, _, _ := g.mkvRip("", "wait", want.RunID)

			var got runstore.State
			if err := json.Unmarshal([]byte(stdout), &got); err != nil || got.State != state || got.RunID != want.RunID {
				t.Errorf("wait printed %q (%v)", stdout, err)
			}
		})
	}
}

// wait polls until the controller is done, however long that takes.
func TestWaitPollsUntilTerminal(t *testing.T) {
	g := newCmdRig(t)
	st := g.putRun("20261113-100000-0001", runstore.RunRunning, 0)
	l := g.holdLease("rip", st.RunID)
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		time.Sleep(60 * time.Millisecond)
		st.State = runstore.RunComplete
		_ = g.store.Save(st)
		l.Release()
	}()
	defer func() { <-finished }()

	stdout, stderr, code := g.mkvRip("", "wait", st.RunID)
	if code != 0 || !strings.Contains(stdout, `"state": "complete"`) {
		t.Errorf("wait exited %d with %q, stderr %q", code, stdout, stderr)
	}
}

func TestListIgnoresOtherDirectories(t *testing.T) {
	g := newCmdRig(t)
	if stdout, _, code := g.mkvRip("", "list"); code != 0 || stdout != "[]\n" {
		t.Errorf("list with no runs = %q (exit %d), want []", stdout, code)
	}

	g.putRun("20261113-100000-0001", runstore.RunComplete, 0)
	g.putRun("20261113-100500-0002", runstore.RunIncomplete, 5)
	root := g.paths.RipsRoot()
	// What the Python tool and the owner left under .rips.
	mustMkdir(t, filepath.Join(root, "aborted-20261101-120000"))
	mustMkdir(t, filepath.Join(root, "discinfo"))
	mustMkdir(t, filepath.Join(root, "20261109-215032-4e78"))
	if err := os.WriteFile(filepath.Join(root, "20261109-215032-4e78", "state.json"), []byte(`{"run_id":"20261109-215032-4e78","state":"complete","started":"x"}`), 0o660); err != nil {
		t.Fatal(err)
	}
	mustMkdir(t, filepath.Join(root, "20261110-000000-aaaa"))
	if err := os.WriteFile(filepath.Join(root, "20261110-000000-aaaa", "state.json"), []byte(`{"schema":1,"run_id":"20261110-000000-aaaa"}`), 0o660); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "finalize.lock"), []byte("20261109-215032-4e78"), 0o660); err != nil {
		t.Fatal(err)
	}

	stdout, _, code := g.mkvRip("", "list")
	var got []runstore.Summary
	if err := json.Unmarshal([]byte(stdout), &got); err != nil || code != 0 {
		t.Fatalf("list printed %q (%v), exit %d", stdout, err, code)
	}
	if len(got) != 2 || got[0].RunID != "20261113-100500-0002" || got[1].RunID != "20261113-100000-0001" ||
		got[0].Kind != runstore.KindRip || got[0].State != runstore.RunIncomplete || got[1].FinishedAt == nil {
		t.Errorf("list = %+v, want the two schema-2 runs, newest first", got)
	}
}

func TestStatusDefaultSelection(t *testing.T) {
	statusOf := func(t *testing.T, g *rig) *runstore.State {
		t.Helper()
		stdout, stderr, code := g.mkvRip("", "status")
		var st runstore.State
		if err := json.Unmarshal([]byte(stdout), &st); err != nil || code != 0 {
			t.Fatalf("status printed %q (%v), exit %d, stderr %q", stdout, err, code, stderr)
		}
		return &st
	}

	t.Run("the newest run when nothing holds the drives", func(t *testing.T) {
		g := newCmdRig(t)
		g.putRun("20261113-100000-0001", runstore.RunComplete, 0)
		g.putRun("20261113-100500-0002", runstore.RunIncomplete, 5)
		if st := statusOf(t, g); st.RunID != "20261113-100500-0002" || st.Controller.Alive {
			t.Errorf("status = %s alive %v, want the newest run", st.RunID, st.Controller.Alive)
		}
	})

	t.Run("the run holding the drives, even when a newer one exists", func(t *testing.T) {
		g := newCmdRig(t)
		g.putRun("20261113-100000-0001", runstore.RunRunning, 0)
		g.putRun("20261113-100500-0002", runstore.RunComplete, 5)
		g.holdLease("rip", "20261113-100000-0001")
		if st := statusOf(t, g); st.RunID != "20261113-100000-0001" || !st.Controller.Alive || st.State != runstore.RunRunning {
			t.Errorf("status = %s %s alive %v, want the running run", st.RunID, st.State, st.Controller.Alive)
		}
	})

	t.Run("a run whose controller died is reported dead and the verdict kept", func(t *testing.T) {
		g := newCmdRig(t)
		g.putRun("20261113-100000-0001", runstore.RunRunning, 0)
		st := statusOf(t, g)
		if st.State != runstore.RunIncomplete || deref(st.Reason) != "controller died" || st.Controller.Alive {
			t.Errorf("status = %s %q alive %v", st.State, deref(st.Reason), st.Controller.Alive)
		}
		if disk := readState(t, g, st.RunID); disk.State != runstore.RunIncomplete {
			t.Errorf("the verdict was not persisted: %s", disk.State)
		}
	})
}

// start, identify and finalize all refuse in the foreground while the lease is
// held, naming the holder, before any controller is started. The detached
// controller refuses by the same rule, which TestConcurrentStarts exercises.
func TestLaunchRefusedWhileHeld(t *testing.T) {
	g, before := failedRig(t)
	g.forCommands()
	plan := writePlan(t, t.TempDir(), "SERIAL00")
	g.holdLease("finalize", "20261113-100000-0001")

	for _, args := range [][]string{{"start", plan}, {"identify"}, {"finalize", before.RunID}} {
		_, stderr, code := g.mkvRip("", args...)
		if want := "mkv-rip: FATAL: the drives are held by a finalize (run 20261113-100000-0001, pid "; code != cli.ExitRefused || !strings.HasPrefix(stderr, want) {
			t.Errorf("%v exited %d with %q, want exit 3 and %q", args, code, stderr, want)
		}
	}
	if runs, err := g.store.List(); err != nil || len(runs) != 1 {
		t.Errorf("runs = %+v (%v), want only the failed one", runs, err)
	}
}

func TestFinalizeDryRun(t *testing.T) {
	report := func(t *testing.T, stdout string) finalizeReport {
		t.Helper()
		var r finalizeReport
		if err := json.Unmarshal([]byte(stdout), &r); err != nil {
			t.Fatalf("output %q: %v", stdout, err)
		}
		return r
	}

	t.Run("something qualifies", func(t *testing.T) {
		g, before := failedRig(t)
		g.forCommands()
		stateBefore, _ := os.ReadFile(filepath.Join(g.store.Dir(before.RunID), "state.json"))
		calls := len(g.runner.callsTo("makemkvcon", ""))

		stdout, stderr, code := g.mkvRip("", "finalize", before.RunID, "--dry-run")
		r := report(t, stdout)
		if code != 0 || stderr != "" || !reflect.DeepEqual(r.WouldFinalize, []discRef{{0, "SERIAL00"}}) || r.Skipped == nil || len(r.Skipped) != 0 {
			t.Errorf("exit %d, stderr %q, report %+v", code, stderr, r)
		}
		stateAfter, _ := os.ReadFile(filepath.Join(g.store.Dir(before.RunID), "state.json"))
		if string(stateAfter) != string(stateBefore) || len(g.runner.callsTo("makemkvcon", "")) != calls {
			t.Error("a dry run changed the run or spun a drive")
		}
		if _, _, code := g.mkvRip("", "finalize", "--dry-run", before.RunID); code != 0 {
			t.Errorf("the flag before the run id: exit %d", code)
		}
	})

	t.Run("nothing qualifies", func(t *testing.T) {
		g, before := failedRig(t)
		g.forCommands()
		g.drives.remove("SERIAL00")

		for _, args := range [][]string{{"finalize", before.RunID, "--dry-run"}, {"finalize", before.RunID}} {
			stdout, stderr, code := g.mkvRip("", args...)
			r := report(t, stdout)
			if code != 1 || stderr != "" || r.WouldFinalize == nil || len(r.WouldFinalize) != 0 ||
				!reflect.DeepEqual(r.Skipped, []skippedDisc{{0, "SERIAL00", notOnBus("SERIAL00")}}) {
				t.Errorf("%v: exit %d, stderr %q, report %+v", args, code, stderr, r)
			}
		}
		if st := readState(t, g, before.RunID); st.State != runstore.RunIncomplete {
			t.Errorf("run = %s, want it left alone", st.State)
		}
	})

	t.Run("an identify run is not a rip", func(t *testing.T) {
		g := newCmdRig(t, "SERIAL00")
		st := g.putRun("20261113-100000-0001", runstore.RunIncomplete, 0)
		st.Kind = runstore.KindIdentify
		if err := g.store.Save(st); err != nil {
			t.Fatal(err)
		}
		_, stderr, code := g.mkvRip("", "finalize", st.RunID, "--dry-run")
		if code != 2 || !strings.Contains(stderr, "is an identify run") {
			t.Errorf("exit %d, stderr %q", code, stderr)
		}
	})
}
