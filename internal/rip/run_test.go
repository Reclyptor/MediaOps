package rip

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/reclyptor/mediaops/internal/drives"
	"github.com/reclyptor/mediaops/internal/runstore"
)

func destOf(g *rig, p int, discIdx int) string {
	return testPlan(serials(p)...).Discs[discIdx].Destination(g.paths.Staging())
}

func fileMode(t *testing.T, path string) os.FileMode {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info.Mode().Perm()
}

func exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

func TestNewRun(t *testing.T) {
	g := newRig(t, 1, "SERIAL00")
	p := testPlan("SERIAL00")
	run, err := NewRun(g.deps, p, "20261113-100000-abcd")
	if err != nil {
		t.Fatal(err)
	}
	if run.ID() != "20261113-100000-abcd" {
		t.Errorf("ID = %q", run.ID())
	}
	dir := g.store.Dir(run.ID())
	if mode := fileMode(t, dir); mode != 0o770 {
		t.Errorf("run dir mode = %o, want 770", mode)
	}
	if mode := fileMode(t, filepath.Join(dir, "plan.json")); mode != 0o660 {
		t.Errorf("plan.json mode = %o, want 660", mode)
	}
	data, err := os.ReadFile(filepath.Join(dir, "plan.json"))
	if err != nil {
		t.Fatal(err)
	}
	want, _ := json.MarshalIndent(p, "", " ")
	if strings.TrimSpace(string(data)) != string(want) {
		t.Errorf("plan.json = %s, want %s", data, want)
	}

	st, err := g.store.Load(run.ID())
	if err != nil {
		t.Fatal(err)
	}
	d := st.Discs[0]
	if st.Kind != runstore.KindRip || st.State != runstore.RunRunning || st.Controller.PID != os.Getpid() || st.ToolVersion != "test" {
		t.Errorf("first state = %+v", st)
	}
	if d.State != runstore.DiscPending || d.Label != "Penguindrum S01 D01" || d.Dest != destOf(g, 1, 0) {
		t.Errorf("disc = %+v", d)
	}
	if n := g.drives.listCount(); n != 0 {
		t.Errorf("NewRun listed the drives %d time(s)", n)
	}
	if _, err := NewRun(g.deps, p, run.ID()); err == nil {
		t.Error("a second run with the same id was created")
	}
}

func TestNewRunRequiresDeps(t *testing.T) {
	g := newRig(t, 1)
	g.deps.Runner = nil
	if _, err := NewRun(g.deps, testPlan("SERIAL00"), "20261113-100000-abcd"); err == nil {
		t.Fatal("NewRun accepted a missing Runner")
	}
}

func TestPreflight(t *testing.T) {
	const serial = "SERIAL00"
	tests := []struct {
		name   string
		break_ func(g *rig, dest string)
		reason func(dest string) string
	}{
		{
			name:   "12 Mbit/s",
			break_: func(g *rig, _ string) { g.drives.edit(serial, func(d *drives.Drive) { d.LinkMbps = new(12) }) },
			reason: func(string) string {
				return "link is 12 Mbit/s, not 480: the hub tree needs the reset runbook, which needs root on the node"
			},
		},
		{
			name:   "missing serial",
			break_: func(g *rig, _ string) { g.drives.remove(serial) },
			reason: func(string) string {
				return "Drive SERIAL00 is not on the USB bus; it has likely dropped off the hub and needs the hub reset (see the makemkv skill)."
			},
		},
		{
			name: "no media",
			break_: func(g *rig, _ string) {
				g.drives.edit(serial, func(d *drives.Drive) {
					d.Status, d.Media, d.Tray = drives.StatusNoDisc, new(false), new("open")
				})
			},
			reason: func(string) string { return "no disc in the drive (tray open, state no_disc)" },
		},
		{
			name: "unknown state",
			break_: func(g *rig, _ string) {
				g.drives.edit(serial, func(d *drives.Drive) {
					d.Status, d.Media, d.Error = drives.StatusUnknown, nil, new("ioctl: input/output error")
				})
			},
			reason: func(string) string { return "cannot read the drive state: ioctl: input/output error" },
		},
		{
			name:   "destination exists",
			break_: func(_ *rig, dest string) { mustMkdir(t, dest) },
			reason: func(dest string) string { return "destination already exists: " + dest },
		},
		{
			name:   "partial exists",
			break_: func(_ *rig, dest string) { mustMkdir(t, dest+".partial") },
			reason: func(dest string) string { return "a .partial from an earlier run exists: " + dest + ".partial" },
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := newRig(t, 1, serial)
			dest := destOf(g, 1, 0)
			tt.break_(g, dest)
			st := g.execute(testPlan(serial))

			d := st.Discs[0]
			if d.State != runstore.DiscBlocked || d.Reason == nil || *d.Reason != tt.reason(dest) {
				t.Errorf("disc = %s %v, want blocked %q", d.State, deref(d.Reason), tt.reason(dest))
			}
			if st.State != runstore.RunIncomplete || len(st.Problems) != 1 || st.Problems[0].Reason != tt.reason(dest) {
				t.Errorf("run = %s, problems %+v", st.State, st.Problems)
			}
			if n := len(g.runner.callsTo("makemkvcon", "")); n != 0 {
				t.Errorf("makemkvcon ran %d time(s) for a blocked disc", n)
			}
		})
	}
}

func mustMkdir(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o770); err != nil {
		t.Fatal(err)
	}
}

func deref(s *string) string {
	if s == nil {
		return "<nil>"
	}
	return *s
}

func notReady(g *rig) {
	g.drives.edit("SERIAL00", func(d *drives.Drive) {
		d.Status, d.Media = drives.StatusNotReady, new(false)
	})
}

// A drive that is not_ready is waited out on later ticks, not slept on: the
// loop keeps polling health meanwhile, and the disc starts as soon as the drive
// reports disc_ok.
func TestPreflightSettles(t *testing.T) {
	g := newRig(t, 100, "SERIAL00")
	notReady(g)
	const dmesgPollsWhileWaiting = 4
	g.drives.hook = func(_ int, ds []drives.Drive) {
		if len(g.runner.callsTo("dmesg", "")) >= dmesgPollsWhileWaiting {
			ds[0].Status, ds[0].Media = drives.StatusDiscOK, new(true)
		}
	}
	st := g.execute(testPlan("SERIAL00"))

	if st.Discs[0].State != runstore.DiscDone {
		t.Fatalf("disc = %s %v", st.Discs[0].State, deref(st.Discs[0].Reason))
	}
	polls := g.runner.callsTo("dmesg", "")
	infos := g.runner.callsTo("makemkvcon", "info")
	if len(polls) < dmesgPollsWhileWaiting || len(infos) != 1 || infos[0].at.Before(polls[dmesgPollsWhileWaiting-1].at) {
		t.Errorf("health was not polled while the drive settled: %d polls, info at %v", len(polls), infos)
	}
	if n := g.drives.listCount(); n < 4 {
		t.Errorf("the drive was re-checked %d time(s); want one per tick", n)
	}
}

func TestPreflightSettleDeadline(t *testing.T) {
	g := newRig(t, 1000, "SERIAL00")
	notReady(g)
	began := g.clock.Now()
	st := g.execute(testPlan("SERIAL00"))

	const want = "the drive is still not ready after 20s (tray closed, state not_ready)"
	if d := st.Discs[0]; d.State != runstore.DiscBlocked || deref(d.Reason) != want {
		t.Errorf("disc = %s %q, want blocked %q", d.State, deref(d.Reason), want)
	}
	if waited := g.clock.Now().Sub(began); waited < 20*time.Second {
		t.Errorf("blocked after %s of clock time, want at least 20s", waited)
	}
	if n := len(g.runner.callsTo("makemkvcon", "")); n != 0 {
		t.Errorf("makemkvcon ran %d time(s)", n)
	}
}

// Three discs, 30 s gaps: no two scans overlap, each start is at least the gap
// after the previous scan ended, and the concurrency cap holds.
func TestStaggerEndToEnd(t *testing.T) {
	g := newRig(t, 1000, serials(3)...)
	g.deps.Tick = time.Millisecond
	for i := range 3 {
		g.runner.configure("/dev/sr"+string(rune('0'+i)), func(d *fakeDisc) {
			d.InfoTime, d.AnalyseTime, d.CopyTime = 4*time.Second, 6*time.Second, 120*time.Second
		})
	}
	p := testPlan(serials(3)...)
	p.MaxConcurrent = 2
	st := g.execute(p)

	if st.State != runstore.RunComplete {
		t.Fatalf("run = %s %v, problems %+v", st.State, deref(st.Reason), st.Problems)
	}
	spans := g.runner.scanSpans()
	if len(spans) != 3 {
		t.Fatalf("%d scans, want 3", len(spans))
	}
	slices.SortFunc(spans, func(a, b scanSpan) int { return a.start.Compare(b.start) })
	gap := time.Duration(p.SpinupGapSeconds) * time.Second
	for i := 1; i < len(spans); i++ {
		prev, next := spans[i-1], spans[i]
		if next.start.Before(prev.copyStart) {
			t.Errorf("scan %d began at %v, while scan %d was still scanning until %v", i, next.start, i-1, prev.copyStart)
		}
		if got := next.start.Sub(prev.copyStart); got < gap {
			t.Errorf("scan %d began %s after scan %d ended, want at least %s", i, got, i-1, gap)
		}
	}
	if peak := g.runner.peakActive(); peak != p.MaxConcurrent {
		t.Errorf("peak active discs = %d, want the cap of %d reached and never passed", peak, p.MaxConcurrent)
	}
}

func TestHealthPauseAndSuspect(t *testing.T) {
	g := newRig(t, 200, serials(3)...)
	for _, dev := range []string{"/dev/sr0", "/dev/sr1"} {
		g.runner.configure(dev, func(d *fakeDisc) { d.HoldCopy = true })
	}
	p := testPlan(serials(3)...)
	p.HealthPauseSeconds = 60
	r := g.start(p)

	r.until("discs 0 and 1 ripping", func(st *runstore.State) bool {
		return st.Discs[0].State == runstore.DiscRipping && st.Discs[1].State == runstore.DiscRipping
	})
	g.sys.setSpeed(t, "SERIAL00", 12)
	st := r.until("the health event", func(st *runstore.State) bool { return len(st.Health.Events) == 1 })

	ev := st.Health.Events[0]
	const detail = "5-1.1 fell from 480 to 12 Mbit/s"
	if ev.Kind != "link_dropped" || ev.Port != "5-1.1" || ev.Detail != detail || ev.Time.Before(g.clock.base) || ev.Time.Nanosecond() != 0 {
		t.Errorf("event = %+v", ev)
	}
	if st.Health.PausedUntil == nil || st.Health.PauseReason == nil || *st.Health.PauseReason != detail {
		t.Errorf("health = %+v, want a pause with the event's detail", st.Health)
	}
	if got := st.Discs[0].SuspectEvents; len(got) != 1 || got[0] != ev {
		t.Errorf("disc 0 suspect events = %+v, want the event", got)
	}
	if got := st.Discs[0].Errors; !slices.Equal(got, []string{detail}) {
		t.Errorf("disc 0 errors = %q", got)
	}
	if len(st.Discs[1].SuspectEvents) != 0 {
		t.Errorf("disc 1 was marked suspect by an event on another port: %+v", st.Discs[1].SuspectEvents)
	}

	st = r.until("disc 2 starting", func(st *runstore.State) bool { return st.Discs[2].StartedAt != nil })
	if earliest := ev.Time.Add(60 * time.Second); st.Discs[2].StartedAt.Before(earliest) {
		t.Errorf("disc 2 started at %v, inside the pause that runs to %v", st.Discs[2].StartedAt, earliest)
	}
	if st.Health.PausedUntil != nil || st.Health.PauseReason != nil {
		t.Errorf("the expired pause was not cleared: %+v", st.Health)
	}
	r.cancel()
	r.wait()
}

func TestHealthHubEventCoversDrivesBeneathIt(t *testing.T) {
	g := newRig(t, 200, serials(2)...)
	for _, dev := range []string{"/dev/sr0", "/dev/sr1"} {
		g.runner.configure(dev, func(d *fakeDisc) { d.HoldCopy = true })
	}
	r := g.start(testPlan(serials(2)...))
	r.until("both discs ripping", func(st *runstore.State) bool {
		return st.Discs[0].State == runstore.DiscRipping && st.Discs[1].State == runstore.DiscRipping
	})
	g.runner.setDmesg("usb 5-1: USB disconnect, device number 4\n")
	st := r.until("both discs suspect", func(st *runstore.State) bool {
		return len(st.Discs[0].SuspectEvents) == 1 && len(st.Discs[1].SuspectEvents) == 1
	})
	if !st.Health.Dmesg.Available || st.Health.Events[0].Kind != "dmesg" || st.Health.Events[0].Port != "5-1" {
		t.Errorf("health = %+v", st.Health)
	}
	r.cancel()
	r.wait()
}

func TestHealthDmesgMirrored(t *testing.T) {
	g := newRig(t, 1, "SERIAL00")
	g.runner.dmesgErr = "dmesg: klogctl: Operation not permitted"
	st := g.execute(testPlan("SERIAL00"))
	want := "dmesg: klogctl: Operation not permitted"
	if st.Health.Dmesg.Available || st.Health.Dmesg.Detail != want {
		t.Errorf("dmesg = %+v, want unavailable with %q", st.Health.Dmesg, want)
	}
	if st.State != runstore.RunComplete {
		t.Errorf("an unreadable dmesg ended the run %s", st.State)
	}
}

func TestScanTimeout(t *testing.T) {
	g := newRig(t, 500, serials(2)...)
	g.runner.configure("/dev/sr0", func(d *fakeDisc) { d.HangScan = true })
	p := testPlan(serials(2)...)
	p.ScanTimeoutSeconds = 100
	st := g.execute(p)

	d := st.Discs[0]
	reason := deref(d.Reason)
	if d.State != runstore.DiscFailed || !strings.HasPrefix(reason, "still scanning after 100s; killed") || !strings.HasSuffix(reason, "(no rip was attempted)") {
		t.Errorf("disc 0 = %s %q", d.State, reason)
	}
	if st.Discs[1].State != runstore.DiscDone {
		t.Errorf("the stuck scan starved disc 1: %s %v", st.Discs[1].State, deref(st.Discs[1].Reason))
	}
	if n := len(g.runner.callsTo("makemkvcon", "mkv")); n != 1 {
		t.Errorf("%d rips ran, want only disc 1's", n)
	}
}

func TestInfoPassGatesRip(t *testing.T) {
	tests := []struct {
		name   string
		disc   fakeDisc
		reason string
	}{
		{"non-zero exit", fakeDisc{InfoExit: 1}, "the title info pass exited 1 (no rip was attempted)"},
		{
			"fatal message",
			fakeDisc{InfoLog: "synthetic/info-fatal-5010.log"},
			"the title info pass reported a fatal error: Failed to open disc; the title info pass listed no titles (no rip was attempted)",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := newRig(t, 1, "SERIAL00")
			g.runner.configure("/dev/sr0", func(d *fakeDisc) { *d = tt.disc })
			st := g.execute(testPlan("SERIAL00"))

			d := st.Discs[0]
			if d.State != runstore.DiscFailed || deref(d.Reason) != tt.reason {
				t.Errorf("disc = %s %q, want failed %q", d.State, deref(d.Reason), tt.reason)
			}
			if n := len(g.runner.callsTo("makemkvcon", "mkv")); n != 0 {
				t.Errorf("the rip ran %d time(s) after a bad info pass", n)
			}
			if exists(d.Dest + ".partial") {
				t.Error("a .partial was created for a rip that never started")
			}
			if d.MakemkvExit != nil {
				t.Errorf("makemkv_exit = %d for a rip that never ran", *d.MakemkvExit)
			}
			if len(d.Errors) == 0 {
				t.Error("the info problems are not in the disc's errors")
			}
			if !exists(filepath.Join(g.store.Dir(st.RunID), "disc-00", "info.log")) {
				t.Error("the info log was not kept")
			}
		})
	}
}

func TestSuspectNotPromoted(t *testing.T) {
	g := newRig(t, 100, "SERIAL00")
	g.runner.configure("/dev/sr0", func(d *fakeDisc) { d.CopyTime = 30 * time.Second })
	r := g.start(testPlan("SERIAL00"))
	r.until("the copy", discState(runstore.DiscRipping))
	g.sys.setSpeed(t, "SERIAL00", 12)
	st := r.wait()

	d := st.Discs[0]
	partial := d.Dest + ".partial"
	want := "rip verified, but the USB tree reported 1 error(s) under this drive; kept at " + partial + " for review"
	if d.State != runstore.DiscSuspect || deref(d.Reason) != want {
		t.Fatalf("disc = %s %q, want suspect %q", d.State, deref(d.Reason), want)
	}
	if !exists(filepath.Join(partial, "manifest.json")) || exists(d.Dest) {
		t.Error("a suspect rip must keep its .partial with a manifest and not be promoted")
	}
	if n := len(g.runner.callsTo("mkvpropedit", "")); n != 0 {
		t.Errorf("a suspect rip was titled (%d mkvpropedit calls)", n)
	}
	if ejected := g.drives.ejects(); len(ejected) != 0 {
		t.Errorf("a suspect rip was ejected: %v", ejected)
	}
	if st.State != runstore.RunIncomplete || len(st.Problems) != 1 || st.Problems[0].State != runstore.DiscSuspect {
		t.Errorf("run = %s, problems %+v", st.State, st.Problems)
	}
	var m manifest
	readJSON(t, filepath.Join(partial, "manifest.json"), &m)
	if len(m.SuspectEvents) != 1 {
		t.Errorf("manifest suspect_events = %+v", m.SuspectEvents)
	}
}

func readJSON(t *testing.T, path string, v any) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, v); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
}

func TestDestinationAppeared(t *testing.T) {
	g := newRig(t, 1, "SERIAL00")
	g.runner.configure("/dev/sr0", func(d *fakeDisc) {
		d.AfterRip = func(partial string) { mustMkdir(t, strings.TrimSuffix(partial, ".partial")) }
	})
	st := g.execute(testPlan("SERIAL00"))

	d := st.Discs[0]
	if want := "destination appeared during the rip: " + d.Dest; d.State != runstore.DiscFailed || deref(d.Reason) != want {
		t.Errorf("disc = %s %q, want failed %q", d.State, deref(d.Reason), want)
	}
	if !exists(d.Dest+".partial") || exists(filepath.Join(d.Dest, "manifest.json")) {
		t.Error("the rip must stay in .partial and leave the destination alone")
	}
	if ejected := g.drives.ejects(); len(ejected) != 0 {
		t.Errorf("ejected %v after a failed promotion", ejected)
	}
}

func TestEjectFailureKeepsDone(t *testing.T) {
	g := newRig(t, 1, "SERIAL00")
	g.drives.ejectErr = errors.New("the tray is still closed after both attempts")
	st := g.execute(testPlan("SERIAL00"))

	d := st.Discs[0]
	if d.State != runstore.DiscDone || st.State != runstore.RunComplete {
		t.Fatalf("disc = %s %q, run = %s", d.State, deref(d.Reason), st.State)
	}
	if d.Eject == nil || d.Eject.OK || d.Eject.Detail != "the tray is still closed after both attempts" {
		t.Errorf("eject = %+v", d.Eject)
	}
	if !exists(d.Dest) || exists(d.Dest+".partial") {
		t.Error("the rip was not promoted")
	}
}

func TestStopMarksIncomplete(t *testing.T) {
	g := newRig(t, 1, serials(3)...)
	g.runner.configure("/dev/sr0", func(d *fakeDisc) { d.HoldCopy = true })
	p := testPlan(serials(3)...)
	p.MaxConcurrent = 1
	r := g.start(p)

	st := r.until("disc 0 ripping", discState(runstore.DiscRipping))
	d := st.Discs[0]
	if d.Progress == nil || d.Progress.Phase != "Saving to MKV file" || d.Titles.Total != 2 || d.CopyStartedAt == nil ||
		d.Drive == nil || d.Drive.Dev != "/dev/sr0" || d.Drive.LinkMbps != 480 || d.MakemkvExit != nil {
		t.Errorf("disc 0 while ripping = %+v", d)
	}
	r.cancel()
	st = r.wait()

	d = st.Discs[0]
	if want := "run was stopped mid-rip (kept at " + d.Dest + ".partial)"; d.State != runstore.DiscFailed || deref(d.Reason) != want {
		t.Errorf("disc 0 = %s %q, want failed %q", d.State, deref(d.Reason), want)
	}
	if d.MakemkvExit == nil {
		t.Error("makemkv_exit was not recorded for the terminated makemkvcon")
	}
	if !exists(d.Dest + ".partial") {
		t.Error("the stopped rip's .partial was not kept")
	}
	for _, d := range st.Discs[1:] {
		if d.State != runstore.DiscFailed || deref(d.Reason) != "run was stopped before this disc started" {
			t.Errorf("disc %d = %s %q", d.Index, d.State, deref(d.Reason))
		}
	}
	if st.State != runstore.RunIncomplete || deref(st.Reason) != "stopped" || st.Controller.Alive || st.FinishedAt == nil || len(st.Problems) != 3 {
		t.Errorf("run = %s %q alive=%v finished=%v problems=%d", st.State, deref(st.Reason), st.Controller.Alive, st.FinishedAt, len(st.Problems))
	}
	if n := len(g.runner.callsTo("makemkvcon", "")); n != 2 {
		t.Errorf("makemkvcon was started %d time(s); only disc 0 had started, with an info and a rip pass", n)
	}
}

func TestWorkerPanicRecovered(t *testing.T) {
	g := newRig(t, 1000, serials(2)...)
	g.runner.configure("/dev/sr0", func(d *fakeDisc) { d.Panic = true })
	p := testPlan(serials(2)...)
	p.MaxConcurrent = 1
	st := g.execute(p)

	if d := st.Discs[0]; d.State != runstore.DiscFailed || deref(d.Reason) != "internal error: the fake makemkvcon blew up" {
		t.Errorf("disc 0 = %s %q", d.State, deref(d.Reason))
	}
	if st.Discs[1].State != runstore.DiscDone {
		t.Errorf("the panicked worker stranded its slot: disc 1 is %s", st.Discs[1].State)
	}
	if !strings.Contains(g.log.String(), "worker panicked") {
		t.Errorf("the panic's stack is not in controller.log:\n%s", g.log)
	}
}

func TestHappyPath(t *testing.T) {
	g := newRig(t, 1000, serials(2)...)
	g.runner.configure("/dev/sr1", func(d *fakeDisc) { d.Fixture = "pwp-d2" })
	st := g.execute(testPlan(serials(2)...))

	if st.State != runstore.RunComplete || st.Reason != nil || len(st.Problems) != 0 || st.Controller.Alive || st.FinishedAt == nil {
		t.Fatalf("run = %s %v problems %+v alive=%v", st.State, deref(st.Reason), st.Problems, st.Controller.Alive)
	}
	wantTitles := []int{2, 8}
	files := 0
	for i, d := range st.Discs {
		if d.State != runstore.DiscDone || d.Reason != nil {
			t.Fatalf("disc %d = %s %q", i, d.State, deref(d.Reason))
		}
		if d.Titles.Done != wantTitles[i] || d.Titles.Total != wantTitles[i] {
			t.Errorf("disc %d titles = %+v, want %d of %d", i, d.Titles, wantTitles[i], wantTitles[i])
		}
		if d.MakemkvExit == nil || *d.MakemkvExit != 0 || d.Eject == nil || !d.Eject.OK {
			t.Errorf("disc %d exit %v eject %+v", i, d.MakemkvExit, d.Eject)
		}
		for _, ts := range []*time.Time{d.StartedAt, d.CopyStartedAt, d.FinishedAt} {
			if ts == nil || ts.Nanosecond() != 0 {
				t.Errorf("disc %d timestamp %v is missing or not whole seconds", i, ts)
			}
		}
		if exists(d.Dest+".partial") || fileMode(t, d.Dest) != 0o770 {
			t.Errorf("disc %d: .partial left behind or destination mode wrong", i)
		}
		entries, err := os.ReadDir(d.Dest)
		if err != nil {
			t.Fatal(err)
		}
		var mkvs int
		for _, e := range entries {
			if fileMode(t, filepath.Join(d.Dest, e.Name())) != 0o660 {
				t.Errorf("%s is not mode 660", e.Name())
			}
			if strings.HasSuffix(e.Name(), ".mkv") {
				mkvs++
			}
		}
		if mkvs != wantTitles[i] || !exists(filepath.Join(d.Dest, "manifest.json")) {
			t.Errorf("disc %d: %d mkv files and manifest %v", i, mkvs, exists(filepath.Join(d.Dest, "manifest.json")))
		}
		files += mkvs

		discDir := filepath.Join(g.store.Dir(st.RunID), "disc-0"+string(rune('0'+i)))
		for _, log := range []string{"info.log", "rip.log"} {
			if mode := fileMode(t, filepath.Join(discDir, log)); mode != 0o660 {
				t.Errorf("disc-0%d/%s mode = %o", i, log, mode)
			}
		}
		if mode := fileMode(t, discDir); mode != 0o770 {
			t.Errorf("%s mode = %o", discDir, mode)
		}
	}
	if got := slices.Sorted(slices.Values(g.drives.ejects())); !slices.Equal(got, serials(2)) {
		t.Errorf("ejected %v", got)
	}
	edits := g.runner.callsTo("mkvpropedit", "")
	if len(edits) != files {
		t.Fatalf("%d mkvpropedit calls for %d files", len(edits), files)
	}
	for _, c := range edits {
		if !slices.Contains(c.argv, "title=Penguindrum") {
			t.Errorf("segment title not set on %v", c.argv)
		}
	}
	if got, want := len(readLines(t, filepath.Join(g.store.Dir(st.RunID), "disc-00", "rip.log"))), len(readFixture("penguindrum-d2/rip.log")); got != want {
		t.Errorf("rip.log has %d lines, the recorded rip has %d", got, want)
	}
}

func readLines(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
}

func TestVerificationFailureKeepsPartial(t *testing.T) {
	g := newRig(t, 1000, serials(2)...)
	g.runner.configure("/dev/sr0", func(d *fakeDisc) { d.FileScale = 0.5 })
	st := g.execute(testPlan(serials(2)...))

	d := st.Discs[0]
	partial := d.Dest + ".partial"
	reason := deref(d.Reason)
	if d.State != runstore.DiscFailed || !strings.Contains(reason, "s on disk vs ") || !strings.HasSuffix(reason, "(kept at "+partial+")") {
		t.Fatalf("disc 0 = %s %q", d.State, reason)
	}
	if !exists(partial) || exists(d.Dest) || exists(filepath.Join(partial, "manifest.json")) {
		t.Error("a failed rip must stay in .partial without a manifest")
	}
	if d.MakemkvExit == nil || *d.MakemkvExit != 0 || d.Titles.Done != 2 {
		t.Errorf("exit %v, titles %+v", d.MakemkvExit, d.Titles)
	}
	if got := g.drives.ejects(); !slices.Equal(got, []string{"SERIAL01"}) {
		t.Errorf("ejected %v, want only the good disc", got)
	}
	if st.Discs[1].State != runstore.DiscDone {
		t.Errorf("disc 1 = %s", st.Discs[1].State)
	}
	if st.State != runstore.RunIncomplete || len(st.Problems) != 1 || st.Problems[0].Index != 0 ||
		deref(st.Reason) != "1 of 2 disc(s) did not finish ok; see problems" {
		t.Errorf("run = %s %q problems %+v", st.State, deref(st.Reason), st.Problems)
	}
}

func TestSetTitleFailure(t *testing.T) {
	g := newRig(t, 1, "SERIAL00")
	g.runner.failSegmentTitles()
	st := g.execute(testPlan("SERIAL00"))

	d := st.Discs[0]
	partial := d.Dest + ".partial"
	reason := deref(d.Reason)
	if d.State != runstore.DiscFailed || !strings.HasPrefix(reason, "cannot set the segment title: ") || !strings.HasSuffix(reason, "(kept at "+partial+")") {
		t.Fatalf("disc = %s %q", d.State, reason)
	}
	if !exists(partial) || exists(d.Dest) {
		t.Error("the rip must stay in .partial when titling fails")
	}
	if ejected := g.drives.ejects(); len(ejected) != 0 {
		t.Errorf("ejected %v", ejected)
	}
}

func TestControllerLogLines(t *testing.T) {
	g := newRig(t, 1, "SERIAL00")
	st := g.execute(testPlan("SERIAL00"))

	line := regexp.MustCompile(`^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d(Z|[+-]\d\d:\d\d) \S.*$`)
	text := g.log.String()
	for _, l := range strings.Split(strings.TrimSuffix(text, "\n"), "\n") {
		if !line.MatchString(l) {
			t.Errorf("not a controller.log line: %q", l)
		}
	}
	for _, want := range []string{
		"disc 0 (SERIAL00): pending -> scanning",
		"disc 0 (SERIAL00): scanning -> ripping",
		"disc 0 (SERIAL00): ripping -> done",
		"run " + st.RunID + " complete",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("controller.log lacks %q:\n%s", want, text)
		}
	}
}

func TestManifestShape(t *testing.T) {
	g := newRig(t, 1, "SERIAL00")
	st := g.execute(testPlan("SERIAL00"))
	if st.Discs[0].State != runstore.DiscDone {
		t.Fatalf("disc = %s %q", st.Discs[0].State, deref(st.Discs[0].Reason))
	}

	data, err := os.ReadFile(filepath.Join(st.Discs[0].Dest, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.UseNumber()
	if err := dec.Decode(&got); err != nil {
		t.Fatal(err)
	}
	wantKeys(t, "manifest", got, "schema", "run_id", "disc_index", "plan", "drive", "makemkv", "disc", "titles",
		"errors", "suspect_events", "started", "copy_started", "finished")
	if got["schema"] != json.Number("2") || got["run_id"] != st.RunID || got["disc_index"] != json.Number("0") {
		t.Errorf("schema/run_id/disc_index = %v %v %v", got["schema"], got["run_id"], got["disc_index"])
	}
	mk := got["makemkv"].(map[string]any)
	wantKeys(t, "makemkv", mk, "version", "exit", "titles_saved")
	if mk["version"] != "v2.0.0 linux(x64-release)" || mk["exit"] != json.Number("0") || mk["titles_saved"] != json.Number("2") {
		t.Errorf("makemkv = %v", mk)
	}
	disc := got["disc"].(map[string]any)
	wantKeys(t, "disc", disc, "name", "volume_label", "type")
	if disc["name"] != "PENGUINDRUM - Disc 2" || disc["volume_label"] != "PENGUINDRUM_D2" || disc["type"] != "Blu-ray disc" {
		t.Errorf("disc = %v", disc)
	}
	if errs, ok := got["errors"].([]any); !ok || len(errs) != 0 {
		t.Errorf("errors = %#v, want []", got["errors"])
	}
	if ev, ok := got["suspect_events"].([]any); !ok || len(ev) != 0 {
		t.Errorf("suspect_events = %#v, want []", got["suspect_events"])
	}
	wantKeys(t, "plan", got["plan"].(map[string]any), "serial", "category", "series", "season", "disc")
	wantKeys(t, "drive", got["drive"].(map[string]any), "dev", "sg", "usb_port", "link_mbps")

	titles := got["titles"].([]any)
	if len(titles) != 2 {
		t.Fatalf("%d titles, want 2", len(titles))
	}
	first := titles[0].(map[string]any)
	wantKeys(t, "title", first, "id", "name", "file", "chapters", "duration_seconds", "duration", "size_bytes",
		"source_playlist", "file_bytes", "file_seconds", "segment_title")
	if first["chapters"] != json.Number("49") || first["duration_seconds"] != json.Number("11458") ||
		first["size_bytes"] != json.Number("38917945344") || first["duration"] != "3:10:58" ||
		first["source_playlist"] != "00001.mpls" || first["segment_title"] != "Penguindrum" ||
		first["file"] != "PENGUINDRUM - Disc 2_t00.mkv" || first["file_seconds"] != json.Number("11458.321") {
		t.Errorf("first title = %v", first)
	}
	if second := titles[1].(map[string]any); second["chapters"] != nil {
		t.Errorf("a title with no reported chapter count has chapters = %v, want null", second["chapters"])
	}
}

func wantKeys(t *testing.T, what string, got map[string]any, keys ...string) {
	t.Helper()
	have := make([]string, 0, len(got))
	for k := range got {
		have = append(have, k)
	}
	slices.Sort(have)
	want := slices.Sorted(slices.Values(keys))
	if !slices.Equal(have, want) {
		t.Errorf("%s keys = %v, want %v", what, have, want)
	}
}
