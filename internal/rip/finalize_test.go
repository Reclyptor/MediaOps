package rip

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/reclyptor/mediaops/internal/drives"
	"github.com/reclyptor/mediaops/internal/runstore"
)

// failedRig has run a plan of two discs whose first failed verification, with
// every file measured at half its length. Its .partial is still there, so a
// finalize can pass it once ffprobe measures the files properly (fix).
func failedRig(t *testing.T) (*rig, *runstore.State) {
	t.Helper()
	g := newRig(t, 1000, serials(2)...)
	g.runner.configure("/dev/sr0", func(d *fakeDisc) { d.FileScale = 0.5 })
	st := g.execute(testPlan(serials(2)...))
	if d := st.Discs[0]; d.State != runstore.DiscFailed || !exists(d.Dest+".partial") {
		t.Fatalf("setup: disc 0 = %s %q", d.State, deref(d.Reason))
	}
	if st.Discs[1].State != runstore.DiscDone {
		t.Fatalf("setup: disc 1 = %s", st.Discs[1].State)
	}
	return g, st
}

// fix makes ffprobe measure the files in disc's .partial as the disc says they are.
func (g *rig) fix(disc runstore.Disc) {
	g.t.Helper()
	if err := g.runner.writeFiles(disc.Dest+".partial", fakeDisc{}); err != nil {
		g.t.Fatal(err)
	}
}

func (g *rig) newFinalize(runID string) *Run {
	g.t.Helper()
	run, err := newFinalizeRun(context.Background(), g.deps, runID)
	if err != nil {
		g.t.Fatalf("newFinalizeRun: %v", err)
	}
	return run
}

func (g *rig) finalize(runID string) *runstore.State {
	g.t.Helper()
	return g.launch(g.newFinalize(runID)).wait()
}

func TestFinalizePlan(t *testing.T) {
	const serial = "SERIAL00"
	notReadyDrive := func(g *rig) {
		g.drives.edit(serial, func(d *drives.Drive) { d.Status, d.Media = drives.StatusNotReady, new(false) })
	}
	tests := []struct {
		name   string
		break_ func(g *rig, disc *runstore.Disc, runDir string)
		reason func(disc runstore.Disc, runDir string) string // "" qualifies
	}{
		{name: "everything holds", reason: func(runstore.Disc, string) string { return "" }},
		{
			name:   "no partial",
			break_: func(_ *rig, d *runstore.Disc, _ string) { os.Remove(d.Dest + ".partial") },
			reason: func(d runstore.Disc, _ string) string { return "no " + d.Dest + ".partial to verify" },
		},
		{
			name: "partial is a file",
			break_: func(_ *rig, d *runstore.Disc, _ string) {
				os.Remove(d.Dest + ".partial")
				os.WriteFile(d.Dest+".partial", nil, 0o660)
			},
			reason: func(d runstore.Disc, _ string) string { return "no " + d.Dest + ".partial to verify" },
		},
		{
			name:   "rip log missing",
			break_: func(_ *rig, _ *runstore.Disc, dir string) { os.Remove(filepath.Join(dir, "disc-00", "rip.log")) },
			reason: func(_ runstore.Disc, dir string) string {
				return "the rip log " + filepath.Join(dir, "disc-00", "rip.log") + " is missing"
			},
		},
		{
			name:   "drive not on the bus",
			break_: func(g *rig, _ *runstore.Disc, _ string) { g.drives.remove(serial) },
			reason: func(runstore.Disc, string) string { return notOnBus(serial) },
		},
		{
			name: "no disc",
			break_: func(g *rig, _ *runstore.Disc, _ string) {
				g.drives.edit(serial, func(d *drives.Drive) { d.Status, d.Media, d.Tray = drives.StatusNoDisc, new(false), new("open") })
			},
			reason: func(runstore.Disc, string) string { return "no disc in the drive (tray open, state no_disc)" },
		},
		{
			name: "link at 12 Mbit/s",
			break_: func(g *rig, _ *runstore.Disc, _ string) {
				g.drives.edit(serial, func(d *drives.Drive) { d.LinkMbps = new(12) })
			},
			reason: func(runstore.Disc, string) string {
				return "link is 12 Mbit/s, not 480: the hub tree needs the reset runbook, which needs root on the node"
			},
		},
		{
			name: "drive state unreadable",
			break_: func(g *rig, _ *runstore.Disc, _ string) {
				g.drives.edit(serial, func(d *drives.Drive) {
					d.Status, d.Media, d.Error = drives.StatusUnknown, nil, new("ioctl: input/output error")
				})
			},
			reason: func(runstore.Disc, string) string { return "cannot read the drive state: ioctl: input/output error" },
		},
		{
			name:   "drive still becoming ready",
			break_: func(g *rig, _ *runstore.Disc, _ string) { notReadyDrive(g) },
			reason: func(runstore.Disc, string) string { return "" },
		},
		{
			name:   "no recorded exit status",
			break_: func(_ *rig, d *runstore.Disc, _ string) { d.MakemkvExit = nil },
			reason: func(runstore.Disc, string) string {
				return "no exit status was recorded for the rip, so it cannot be told whether makemkvcon finished cleanly"
			},
		},
		{
			name:   "makemkvcon exited non-zero",
			break_: func(_ *rig, d *runstore.Disc, _ string) { d.MakemkvExit = new(2) },
			reason: func(runstore.Disc, string) string { return "makemkvcon exited 2" },
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := newRig(t, 1, serial)
			dest := destOf(g, 1, 0)
			disc := runstore.Disc{Index: 0, Serial: serial, Label: "Penguindrum S01 D01", Dest: dest, State: runstore.DiscFailed, MakemkvExit: new(0)}
			st := &runstore.State{RunID: g.newID(), Kind: runstore.KindRip, State: runstore.RunIncomplete, Discs: []runstore.Disc{disc}}
			// Discs that did not fail are nobody's business, whatever is true of them.
			for i, state := range []runstore.DiscState{runstore.DiscDone, runstore.DiscBlocked, runstore.DiscSuspect, runstore.DiscPending} {
				st.Discs = append(st.Discs, runstore.Disc{Index: i + 1, Serial: serial, State: state, Dest: dest, MakemkvExit: new(0)})
			}
			runDir := g.store.Dir(st.RunID)
			mustMkdir(t, dest+".partial")
			mustMkdir(t, filepath.Join(runDir, "disc-00"))
			if err := os.WriteFile(filepath.Join(runDir, "disc-00", "rip.log"), []byte("MSG:1005\n"), 0o660); err != nil {
				t.Fatal(err)
			}
			if tt.break_ != nil {
				tt.break_(g, &st.Discs[0], runDir)
			}
			found, err := g.drives.List(context.Background())
			if err != nil {
				t.Fatal(err)
			}

			report, err := finalizePlan(st, runDir, found)
			if err != nil {
				t.Fatal(err)
			}
			want := tt.reason(st.Discs[0], runDir)
			if want == "" {
				if !reflect.DeepEqual(report.WouldFinalize, []discRef{{0, serial}}) || len(report.Skipped) != 0 {
					t.Errorf("report = %+v, want disc 0 to qualify", report)
				}
				return
			}
			wantSkipped := []skippedDisc{{0, serial, want}}
			if len(report.WouldFinalize) != 0 || !reflect.DeepEqual(report.Skipped, wantSkipped) {
				t.Errorf("report = %+v, want only %+v skipped", report, wantSkipped)
			}
		})
	}
}

func TestFinalizeEndToEnd(t *testing.T) {
	t.Run("a disc that now matches is promoted", func(t *testing.T) {
		g, before := failedRig(t)
		g.fix(before.Discs[0])
		run := g.newFinalize(before.RunID)

		reopened := readState(t, g, before.RunID)
		if reopened.State != runstore.RunFinalizing || reopened.Discs[0].State != runstore.DiscPending || reopened.FinishedAt != nil || reopened.Reason != nil {
			t.Errorf("reopened run = %s %v, disc 0 %s", reopened.State, deref(reopened.Reason), reopened.Discs[0].State)
		}
		st := g.launch(run).wait()

		d := st.Discs[0]
		if d.State != runstore.DiscDone || d.Reason != nil || d.Eject == nil || !d.Eject.OK || d.Titles.Done != 2 || d.Titles.Total != 2 {
			t.Fatalf("disc 0 = %s %q eject %+v titles %+v", d.State, deref(d.Reason), d.Eject, d.Titles)
		}
		if !exists(d.Dest) || exists(d.Dest+".partial") || !exists(filepath.Join(d.Dest, "manifest.json")) {
			t.Error("the copy was not promoted with its manifest")
		}
		if d.MakemkvExit == nil || *d.MakemkvExit != 0 {
			t.Errorf("makemkv_exit = %v, want the recorded 0", d.MakemkvExit)
		}
		if st.State != runstore.RunComplete || st.Reason != nil || len(st.Problems) != 0 || st.FinishedAt == nil || st.Controller.Alive {
			t.Errorf("run = %s %v problems %+v", st.State, deref(st.Reason), st.Problems)
		}
		if !reflect.DeepEqual(st.Discs[1], before.Discs[1]) {
			t.Errorf("disc 1 was touched:\n got %+v\nwant %+v", st.Discs[1], before.Discs[1])
		}
		if n := len(g.runner.callsTo("makemkvcon", "mkv")); n != 2 {
			t.Errorf("%d rips ran in all, want the original two and none for the finalize", n)
		}
		if n := len(g.runner.callsTo("mkvpropedit", "")); n != 4 {
			t.Errorf("%d titles set in all, want disc 1's two and the finalized disc's two", n)
		}
		if got := g.drives.ejects(); !slices.Equal(got, []string{"SERIAL01", "SERIAL00"}) {
			t.Errorf("ejected %v", got)
		}

		var m manifest
		readJSON(t, filepath.Join(d.Dest, "manifest.json"), &m)
		if m.RunID != st.RunID || m.Makemkv.Exit != 0 || len(m.Titles) != 2 || !m.Started.Equal(*d.StartedAt) ||
			m.CopyStarted == nil || !m.CopyStarted.Equal(*d.CopyStartedAt) {
			t.Errorf("manifest = %+v", m)
		}
		entries, err := os.ReadDir(d.Dest)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range entries {
			if mode := fileMode(t, filepath.Join(d.Dest, e.Name())); mode != 0o660 {
				t.Errorf("%s mode = %o", e.Name(), mode)
			}
		}
	})

	t.Run("a disc that still does not match stays failed", func(t *testing.T) {
		g, before := failedRig(t)
		st := g.finalize(before.RunID)

		d := st.Discs[0]
		partial := d.Dest + ".partial"
		if d.State != runstore.DiscFailed || !strings.Contains(deref(d.Reason), "s on disk vs ") || !strings.HasSuffix(deref(d.Reason), "(kept at "+partial+")") {
			t.Fatalf("disc 0 = %s %q", d.State, deref(d.Reason))
		}
		if !exists(partial) || exists(d.Dest) || exists(filepath.Join(partial, "manifest.json")) {
			t.Error("a mismatched copy must stay in .partial without a manifest")
		}
		if got := g.drives.ejects(); !slices.Equal(got, []string{"SERIAL01"}) {
			t.Errorf("ejected %v after a failed finalize", got)
		}
		if n := len(g.runner.callsTo("mkvpropedit", "")); n != 2 {
			t.Errorf("%d titles set, want only disc 1's", n)
		}
		if st.State != runstore.RunIncomplete || len(st.Problems) != 1 || st.Problems[0].Index != 0 {
			t.Errorf("run = %s, problems %+v", st.State, st.Problems)
		}
	})
}

func TestFinalizeKeepsOriginalLogs(t *testing.T) {
	g, before := failedRig(t)
	discDir := filepath.Join(g.store.Dir(before.RunID), "disc-00")
	original := map[string][]byte{}
	for _, name := range []string{"info.log", "rip.log"} {
		data, err := os.ReadFile(filepath.Join(discDir, name))
		if err != nil {
			t.Fatal(err)
		}
		original[name] = data
	}

	// A finalize that reads a different table than the first attempt did, so an
	// overwritten log could not pass for the original.
	g.runner.configure("/dev/sr0", func(d *fakeDisc) { d.InfoLog = "pwp-d2/info.log" })
	g.finalize(before.RunID)
	g.runner.configure("/dev/sr0", func(d *fakeDisc) { d.InfoLog = "" })
	g.fix(before.Discs[0])
	st := g.finalize(before.RunID)

	for name, want := range original {
		got, err := os.ReadFile(filepath.Join(discDir, name))
		if err != nil || string(got) != string(want) {
			t.Errorf("%s was changed by a finalize (err %v)", name, err)
		}
	}
	for n, fixture := range map[int]string{1: "pwp-d2", 2: "penguindrum-d2"} {
		name := filepath.Join(discDir, "finalize-"+string(rune('0'+n))+".info.log")
		if got := readLines(t, name); !slices.Equal(got, readFixture(fixture+"/info.log")) {
			t.Errorf("%s does not hold the info pass of attempt %d", name, n)
		}
		if mode := fileMode(t, name); mode != 0o660 {
			t.Errorf("%s mode = %o", name, mode)
		}
	}
	if st.Discs[0].State != runstore.DiscDone {
		t.Errorf("the second attempt = %s %q", st.Discs[0].State, deref(st.Discs[0].Reason))
	}
}

// A disc that gets no verdict on its copy keeps the failure it was offered
// another try for, however the try ended.
func TestFinalizeUnfinalisedDiscKeepsRecord(t *testing.T) {
	t.Run("its drive left the bus before it started", func(t *testing.T) {
		g, before := failedRig(t)
		run := g.newFinalize(before.RunID)
		g.drives.remove("SERIAL00")
		st := g.launch(run).wait()

		if got := st.Discs[0]; !reflect.DeepEqual(got, before.Discs[0]) {
			t.Errorf("disc 0 = %+v\nwant its record from before the finalize: %+v", got, before.Discs[0])
		}
		if st.State != runstore.RunIncomplete || len(st.Problems) != 1 {
			t.Errorf("run = %s, problems %+v", st.State, st.Problems)
		}
		if !strings.Contains(g.log.String(), "disc 0 (SERIAL00): not finalised") {
			t.Errorf("controller.log does not say why the disc was left alone:\n%s", g.log)
		}
	})

	t.Run("the run was stopped under it", func(t *testing.T) {
		g, before := failedRig(t)
		g.runner.configure("/dev/sr0", func(d *fakeDisc) { d.HangScan = true })
		r := g.launch(g.newFinalize(before.RunID))
		r.until("the finalize scanning", discState(runstore.DiscScanning))
		r.cancel()
		st := r.wait()

		if got := st.Discs[0]; !reflect.DeepEqual(got, before.Discs[0]) {
			t.Errorf("disc 0 = %+v\nwant its record from before the finalize: %+v", got, before.Discs[0])
		}
		if st.State != runstore.RunIncomplete || deref(st.Reason) != "stopped" {
			t.Errorf("run = %s %q", st.State, deref(st.Reason))
		}
	})
}

func readState(t *testing.T, g *rig, runID string) *runstore.State {
	t.Helper()
	var st runstore.State
	readJSON(t, filepath.Join(g.store.Dir(runID), "state.json"), &st)
	return &st
}

func TestFinalizeStartsOneDiscAtATime(t *testing.T) {
	g := newRig(t, 1000, serials(3)...)
	g.deps.Tick = time.Millisecond
	for i := range 3 {
		g.runner.configure("/dev/sr"+string(rune('0'+i)), func(d *fakeDisc) { d.FileScale = 0.5 })
	}
	p := testPlan(serials(3)...)
	st := g.execute(p)
	if len(st.Problems) != 3 {
		t.Fatalf("setup: problems %+v", st.Problems)
	}
	for i := range 3 {
		g.fix(st.Discs[i])
	}
	before := len(g.runner.scanSpans())
	st = g.finalize(st.RunID)

	if st.State != runstore.RunComplete {
		t.Fatalf("run = %s, problems %+v", st.State, st.Problems)
	}
	spans := g.runner.scanSpans()[before:]
	if len(spans) != 3 {
		t.Fatalf("%d scans in the finalize, want 3", len(spans))
	}
	slices.SortFunc(spans, func(a, b scanSpan) int { return a.start.Compare(b.start) })
	for i := 1; i < len(spans); i++ {
		if got := spans[i].start.Sub(spans[i-1].infoEnd); got < 30*time.Second {
			t.Errorf("scan %d began %s after scan %d ended, want at least the 30s gap", i, got, i-1)
		}
	}
}
