package rip

import (
	"encoding/json"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/reclyptor/mediaops/internal/cli"
	"github.com/reclyptor/mediaops/internal/runstore"
)

// startDropped is a dropped exec: mkv-rip runs in a process group of its own
// with stdout on a pipe, its answer is read, and then the group is killed and
// the pipe closed. It returns the run id and the id of the group that died.
func (p *procRig) startDropped(env []string, args ...string) (runID string, pgid int) {
	p.t.Helper()
	cmd := p.command(env, "", args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		p.t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		p.t.Fatal(err)
	}
	var ref runRef
	if err := json.NewDecoder(stdout).Decode(&ref); err != nil {
		p.t.Fatalf("mkv-rip %v printed no run id: %v", args, err)
	}
	pgid = cmd.Process.Pid
	if err := syscall.Kill(-pgid, syscall.SIGKILL); err != nil && err != syscall.ESRCH {
		p.t.Fatal(err)
	}
	stdout.Close()
	_ = cmd.Wait() // it was killed; its status is the point
	return ref.RunID, pgid
}

// procStat is the fields of /proc/PID/stat after the command name.
func procStat(t *testing.T, pid int) (state string, pgrp, session int) {
	t.Helper()
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		t.Fatalf("pid %d is gone: %v", pid, err)
	}
	s := string(data)
	fields := strings.Fields(s[strings.LastIndex(s, ")")+2:])
	pgrp, _ = strconv.Atoi(fields[2])
	session, _ = strconv.Atoi(fields[3])
	return fields[0], pgrp, session
}

// gone reports whether pid is no longer a running process. A zombie waiting
// for its new parent to reap it is gone too.
func gone(pid int) bool {
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return true
	}
	s := string(data)
	return strings.HasPrefix(s[strings.LastIndex(s, ")")+2:], "Z")
}

func TestDetachSurvivesExecDrop(t *testing.T) {
	tests := []struct {
		name  string
		setup func(p *procRig) (args []string, env []string)
		want  runstore.Kind
	}{
		{
			name: "start",
			setup: func(p *procRig) ([]string, []string) {
				return []string{"start", p.planFile("SERIAL00")}, holdMakemkv
			},
			want: runstore.KindRip,
		},
		{
			name: "identify",
			setup: func(p *procRig) ([]string, []string) {
				return []string{"identify", "SERIAL00", "--spinup-gap-seconds", "0"}, holdMakemkv
			},
			want: runstore.KindIdentify,
		},
		{
			name: "finalize",
			setup: func(p *procRig) ([]string, []string) {
				id, disc := p.failedRun("SERIAL00")
				p.fixPartial(disc)
				return []string{"finalize", id}, holdMakemkv
			},
			want: runstore.KindRip,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := newProcRig(t, "SERIAL00")
			args, env := tt.setup(p)
			id, pgid := p.startDropped(env, args...)

			st := p.state(id)
			if st.Kind != tt.want || st.State.Terminal() || !st.Controller.Alive {
				t.Fatalf("after the drop: %s run %s, alive %v", st.Kind, st.State, st.Controller.Alive)
			}
			pid := st.Controller.PID
			if _, group, session := procStat(t, pid); session == pgid || group == pgid || session != pid {
				t.Errorf("controller %d is in group %d, session %d; the killed group was %d, and setsid makes it a session leader", pid, group, session, pgid)
			}

			// A hangup is what closing the terminal sends.
			if err := syscall.Kill(pid, syscall.SIGHUP); err != nil {
				t.Fatal(err)
			}
			time.Sleep(300 * time.Millisecond)
			if gone(pid) {
				t.Fatal("the controller died of SIGHUP")
			}
			if st := p.state(id); st.State.Terminal() {
				t.Fatalf("the run ended early: %s %v", st.State, deref(st.Reason))
			}

			p.release()
			r := p.run(nil, "", "wait", id)
			if r.code != 0 {
				t.Fatalf("wait exited %d: %s\n%s", r.code, r.stderr, r.stdout)
			}
			if st := p.state(id); st.State != runstore.RunComplete {
				t.Errorf("run = %s %v", st.State, deref(st.Reason))
			}
			if tt.name != "identify" {
				if d := p.state(id).Discs[0]; d.State != runstore.DiscDone || !fileExists(d.Dest) {
					t.Errorf("disc = %s %q, want a promoted copy", d.State, deref(d.Reason))
				}
			}
			assertNoKey(t, p.cfg.Staging())
		})
	}
}

// assertNoKey fails if the licence key the rig runs under reached any file
// under dir.
func assertNoKey(t *testing.T, dir string) {
	t.Helper()
	err := filepath.WalkDir(dir, func(path string, e fs.DirEntry, err error) error {
		if err != nil || !e.Type().IsRegular() {
			return err
		}
		data, err := os.ReadFile(path)
		if err == nil && strings.Contains(string(data), "testkey-secret") {
			t.Errorf("the MakeMKV key reached %s", path)
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestChildDiesWithController(t *testing.T) {
	p := newProcRig(t, "SERIAL00")
	id := p.runID(p.ok(holdMakemkv, "", "start", p.planFile("SERIAL00")))
	p.until("makemkvcon to start", func() bool { return p.pidFile("makemkvcon-info.pid") != 0 })
	child := p.pidFile("makemkvcon-info.pid")
	if gone(child) {
		t.Fatal("makemkvcon is not running")
	}

	if err := syscall.Kill(p.state(id).Controller.PID, syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	p.until("makemkvcon to die with its controller", func() bool { return gone(child) })

	st := p.state(id)
	if st.State != runstore.RunIncomplete || deref(st.Reason) != "controller died" || st.Controller.Alive {
		t.Errorf("run = %s %q alive=%v, want incomplete / controller died", st.State, deref(st.Reason), st.Controller.Alive)
	}
}

func TestConcurrentStarts(t *testing.T) {
	p := newProcRig(t, "SERIAL00")
	plan, err := os.ReadFile(p.planFile("SERIAL00"))
	if err != nil {
		t.Fatal(err)
	}

	// Both read their plan from a pipe, so both are released together and both
	// find the drives free before either controller has taken them.
	type launched struct {
		cmd            *exec.Cmd
		stdin          io.WriteCloser
		stdout, stderr strings.Builder
	}
	var starts [2]*launched
	for i := range starts {
		l := &launched{cmd: p.command(holdMakemkv, "", "start", "-")}
		l.cmd.Stdout, l.cmd.Stderr = &l.stdout, &l.stderr
		var err error
		if l.stdin, err = l.cmd.StdinPipe(); err != nil {
			t.Fatal(err)
		}
		if err := l.cmd.Start(); err != nil {
			t.Fatal(err)
		}
		starts[i] = l
	}
	for _, l := range starts {
		if _, err := l.stdin.Write(plan); err != nil {
			t.Fatal(err)
		}
		l.stdin.Close()
	}
	for _, l := range starts {
		_ = l.cmd.Wait() // the exit code is read below
	}

	var winner, loser *launched
	for _, l := range starts {
		switch code := l.cmd.ProcessState.ExitCode(); code {
		case 0:
			winner = l
		case cli.ExitRefused:
			loser = l
		default:
			t.Fatalf("a start exited %d: %s", code, l.stderr.String())
		}
	}
	if winner == nil || loser == nil {
		t.Fatalf("exit codes %d and %d, want one run and one refusal", starts[0].cmd.ProcessState.ExitCode(), starts[1].cmd.ProcessState.ExitCode())
	}
	id := p.runID(outcome{stdout: winner.stdout.String()})
	if want := "held by a rip (run " + id + ", pid "; !strings.Contains(loser.stderr.String(), want) || strings.Count(loser.stderr.String(), "\n") != 1 {
		t.Errorf("the refused start said %q, want one FATAL line naming %q", loser.stderr.String(), want)
	}
	if loser.stdout.Len() != 0 {
		t.Errorf("the refused start printed %q", loser.stdout.String())
	}
	if dirs := p.runDirs(); len(dirs) != 1 || dirs[0] != id {
		t.Errorf("run directories = %v, want only %s", dirs, id)
	}

	p.release()
	if r := p.run(nil, "", "wait", id); r.code != 0 {
		t.Errorf("the run that proceeded did not complete: %d %s", r.code, r.stderr)
	}
}

// A second kind of controller is refused, naming whoever holds the drives.
func TestIdentifyRefusedWhileRunning(t *testing.T) {
	p := newProcRig(t, "SERIAL00")
	id := p.runID(p.ok(holdMakemkv, "", "start", p.planFile("SERIAL00")))

	r := p.run(nil, "", "identify")
	if want := "mkv-rip: FATAL: the drives are held by a rip (run " + id + ", pid "; r.code != cli.ExitRefused || !strings.HasPrefix(r.stderr, want) || r.stdout != "" {
		t.Errorf("identify exited %d, stdout %q, stderr %q; want exit 3 and %q", r.code, r.stdout, r.stderr, want)
	}
	if dirs := p.runDirs(); len(dirs) != 1 {
		t.Errorf("run directories = %v, want only the running one", dirs)
	}

	p.release()
	if r := p.run(nil, "", "wait", id); r.code != 0 {
		t.Errorf("the running run did not complete: %d %s", r.code, r.stderr)
	}
}

func TestStartRefusedWhileFinalizing(t *testing.T) {
	p := newProcRig(t, "SERIAL00")
	id, disc := p.failedRun("SERIAL00")
	p.fixPartial(disc)
	p.ok(holdMakemkv, "", "finalize", id)
	if st := p.state(id); st.State != runstore.RunFinalizing || !st.Controller.Alive {
		t.Fatalf("setup: run = %s alive %v", st.State, st.Controller.Alive)
	}

	r := p.run(nil, "", "start", p.planFile("SERIAL00"))
	if want := "mkv-rip: FATAL: the drives are held by a finalize (run " + id + ", pid "; r.code != cli.ExitRefused || !strings.HasPrefix(r.stderr, want) {
		t.Errorf("start exited %d, stderr %q; want exit 3 and %q", r.code, r.stderr, want)
	}
	if dirs := p.runDirs(); len(dirs) != 1 || dirs[0] != id {
		t.Errorf("run directories = %v, want only the finalized run", dirs)
	}

	p.release()
	if r := p.run(nil, "", "wait", id); r.code != 0 {
		t.Errorf("the finalize did not complete the run: %d %s", r.code, r.stderr)
	}
}

func TestStopCommand(t *testing.T) {
	p := newProcRig(t, "SERIAL00")
	id := p.runID(p.ok(holdMakemkv, "", "start", p.planFile("SERIAL00")))
	p.until("makemkvcon to start", func() bool { return p.pidFile("makemkvcon-info.pid") != 0 })
	child := p.pidFile("makemkvcon-info.pid")

	r := p.ok(nil, "", "stop", id)
	var got struct {
		RunID    string `json:"run_id"`
		Stopping bool   `json:"stopping"`
	}
	if err := json.Unmarshal([]byte(r.stdout), &got); err != nil || got.RunID != id || !got.Stopping {
		t.Errorf("stop printed %q (%v)", r.stdout, err)
	}
	if r := p.run(nil, "", "wait", id); r.code != cli.ExitIncomplete {
		t.Errorf("wait exited %d after a stop, want 1", r.code)
	}
	p.until("makemkvcon to be terminated", func() bool { return gone(child) })

	st := p.state(id)
	d := st.Discs[0]
	if st.State != runstore.RunIncomplete || deref(st.Reason) != "stopped" ||
		d.State != runstore.DiscFailed || deref(d.Reason) != "run was stopped while this disc was still scanning" {
		t.Errorf("run = %s %q, disc = %s %q", st.State, deref(st.Reason), d.State, deref(d.Reason))
	}

	again := p.run(nil, "", "stop", id)
	if want := "mkv-rip: FATAL: run " + id + " does not hold the drives (it is incomplete), so there is nothing to stop\n"; again.code != cli.ExitRefused || again.stderr != want {
		t.Errorf("a second stop exited %d with %q, want exit 3 and %q", again.code, again.stderr, want)
	}
	if unknown := p.run(nil, "", "stop", "20261113-100000-abcd"); unknown.code != cli.ExitUsage {
		t.Errorf("stop of an unknown run exited %d, want 2", unknown.code)
	}
}

// A controller that does not answer in time is reported as an internal error,
// and carries on regardless.
func TestNoHandshakeExits4(t *testing.T) {
	g := newCmdRig(t)
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(t.TempDir(), "carried-on")
	t.Setenv(roleEnv, roleSilent)
	t.Setenv(markerEnv, marker)
	g.deps.Exe = exe
	g.deps.HandshakeTimeout = 100 * time.Millisecond
	plan := writePlan(t, t.TempDir(), "SERIAL00")

	_, stderr, code := g.mkvRip("", "start", plan)
	if code != cli.ExitInternal || !strings.Contains(stderr, "did not report within 100ms") {
		t.Errorf("start exited %d with %q, want 4 and a report that the controller was silent", code, stderr)
	}
	deadline := time.Now().Add(10 * time.Second)
	for !fileExists(marker) {
		if time.Now().After(deadline) {
			t.Fatal("the controller did not carry on after the foreground gave up")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
