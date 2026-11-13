package rip

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/reclyptor/mediaops/internal/drives"
	"github.com/reclyptor/mediaops/internal/runstore"
)

func testIdentify(serials ...string) identifyRequest {
	return identifyRequest{Serials: serials, MinTitleSeconds: 60, SpinupGapSeconds: 30, ScanTimeoutSeconds: 900}
}

func (g *rig) identify(req identifyRequest) *runstore.State {
	g.t.Helper()
	run, err := newIdentifyRun(context.Background(), g.deps, req, g.newID())
	if err != nil {
		g.t.Fatalf("newIdentifyRun: %v", err)
	}
	return g.launch(run).wait()
}

func TestIdentifyHappyPath(t *testing.T) {
	g := newRig(t, 1000, serials(2)...)
	g.runner.configure("/dev/sr1", func(d *fakeDisc) { d.Fixture = "pwp-d2" })
	req := testIdentify()
	st := g.identify(req)

	if st.Kind != runstore.KindIdentify || st.State != runstore.RunComplete || len(st.Problems) != 0 || len(st.Discs) != 2 {
		t.Fatalf("run = %s %s %v, problems %+v, %d discs", st.Kind, st.State, deref(st.Reason), st.Problems, len(st.Discs))
	}
	d := st.Discs[0]
	if d.State != runstore.DiscDone || d.Reason != nil || d.Dest != "" || d.Eject != nil || d.MakemkvExit != nil || d.CopyStartedAt != nil {
		t.Errorf("disc 0 = %+v", d)
	}
	if d.Label != "PENGUINDRUM - Disc 2" {
		t.Errorf("label = %q, want the disc name", d.Label)
	}
	id := d.Identified
	if id == nil || id.Name != "PENGUINDRUM - Disc 2" || id.VolumeLabel != "PENGUINDRUM_D2" || id.Type != "Blu-ray disc" || len(id.Titles) != 2 {
		t.Fatalf("identified = %+v", id)
	}
	want := runstore.IdentifiedTitle{
		ID: 0, Duration: "3:10:58", DurationSeconds: new(11458), Chapters: new(49), SourcePlaylist: "00001.mpls", SizeBytes: new(int64(38917945344)),
	}
	if got := id.Titles[0]; got.ID != want.ID || got.Duration != want.Duration || *got.DurationSeconds != *want.DurationSeconds ||
		*got.Chapters != *want.Chapters || got.SourcePlaylist != want.SourcePlaylist || *got.SizeBytes != *want.SizeBytes {
		t.Errorf("title 0 = %+v, want %+v", got, want)
	}
	if got := id.Titles[1]; got.Chapters != nil || got.SourcePlaylist != "00015.m2ts" || *got.DurationSeconds != 130 {
		t.Errorf("title 1 = %+v: an unreported chapter count must be null", got)
	}
	if got := st.Discs[1].Identified; got == nil || got.Name != "Parallel World Pharmacy D2" || len(got.Titles) != 8 {
		t.Errorf("disc 1 identified = %+v", got)
	}

	for _, log := range []string{"disc-00/info.log", "disc-01/info.log"} {
		if !exists(filepath.Join(g.store.Dir(st.RunID), log)) {
			t.Errorf("%s was not written", log)
		}
	}
	var plan identifyRequest
	readJSON(t, filepath.Join(g.store.Dir(st.RunID), "plan.json"), &plan)
	if plan.Serials == nil || len(plan.Serials) != 0 || plan.MinTitleSeconds != 60 || plan.SpinupGapSeconds != 30 || plan.ScanTimeoutSeconds != 900 {
		t.Errorf("plan.json = %+v, want the request", plan)
	}
}

// One drive at a time, a gap after each, and nothing but info passes.
func TestIdentifyScansOneDriveAtATime(t *testing.T) {
	g := newRig(t, 1000, serials(3)...)
	g.deps.Tick = time.Millisecond
	for i := range 3 {
		g.runner.configure("/dev/sr"+string(rune('0'+i)), func(d *fakeDisc) { d.InfoTime = 5 * time.Second })
	}
	st := g.identify(testIdentify(serials(3)...))

	if st.State != runstore.RunComplete {
		t.Fatalf("run = %s, problems %+v", st.State, st.Problems)
	}
	spans := g.runner.scanSpans()
	if len(spans) != 3 {
		t.Fatalf("%d scans, want 3", len(spans))
	}
	slices.SortFunc(spans, func(a, b scanSpan) int { return a.start.Compare(b.start) })
	for i := 1; i < len(spans); i++ {
		prev, next := spans[i-1], spans[i]
		if next.start.Before(prev.infoEnd) {
			t.Errorf("scan %d began at %v, while scan %d ran until %v", i, next.start, i-1, prev.infoEnd)
		}
		if got := next.start.Sub(prev.infoEnd); got < 30*time.Second {
			t.Errorf("scan %d began %s after scan %d ended, want at least the 30s gap", i, got, i-1)
		}
	}
	if n := len(g.runner.callsTo("makemkvcon", "info")); n != 3 {
		t.Errorf("%d info passes, want 3", n)
	}
}

func TestIdentifyNeverEjectsOrRips(t *testing.T) {
	g := newRig(t, 1000, serials(2)...)
	g.runner.configure("/dev/sr1", func(d *fakeDisc) { d.InfoExit = 1 })
	st := g.identify(testIdentify())

	if st.Discs[0].State != runstore.DiscDone || st.Discs[1].State != runstore.DiscFailed {
		t.Fatalf("discs = %s, %s", st.Discs[0].State, st.Discs[1].State)
	}
	if ejected := g.drives.ejects(); len(ejected) != 0 {
		t.Errorf("identify ejected %v", ejected)
	}
	if n := len(g.runner.callsTo("makemkvcon", "mkv")); n != 0 {
		t.Errorf("identify ran %d rip(s)", n)
	}
	for _, prog := range []string{"mkvpropedit", "ffprobe", "sg_start", "sg_prevent"} {
		if n := len(g.runner.callsTo(prog, "")); n != 0 {
			t.Errorf("identify ran %s %d time(s)", prog, n)
		}
	}
	entries, err := os.ReadDir(g.paths.Staging())
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != ".rips" {
		t.Errorf("identify left %v in the staging tree, want only .rips", entries)
	}
}

func TestIdentifyFailures(t *testing.T) {
	tests := []struct {
		name   string
		disc   fakeDisc
		scan   int
		reason func(string) bool
	}{
		{"info exits non-zero", fakeDisc{InfoExit: 1}, 900, func(r string) bool { return r == "the title info pass exited 1" }},
		{"fatal info pass", fakeDisc{InfoLog: "synthetic/info-fatal-5010.log"}, 900, func(r string) bool {
			return strings.HasPrefix(r, "the title info pass reported a fatal error: Failed to open disc")
		}},
		{"scan timeout", fakeDisc{HangScan: true}, 100, func(r string) bool { return strings.HasPrefix(r, "still scanning after 100s; killed") }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := newRig(t, 500, "SERIAL00")
			g.runner.configure("/dev/sr0", func(d *fakeDisc) { *d = tt.disc })
			req := testIdentify("SERIAL00")
			req.ScanTimeoutSeconds = tt.scan
			st := g.identify(req)

			d := st.Discs[0]
			if d.State != runstore.DiscFailed || !tt.reason(deref(d.Reason)) || strings.Contains(deref(d.Reason), "no rip was attempted") {
				t.Errorf("disc = %s %q", d.State, deref(d.Reason))
			}
			if d.Identified != nil {
				t.Errorf("a failed scan recorded %+v", d.Identified)
			}
			if st.State != runstore.RunIncomplete || len(st.Problems) != 1 {
				t.Errorf("run = %s, problems %+v", st.State, st.Problems)
			}
		})
	}
}

// blockedRig has one good drive, one at 12 Mbit/s, one empty, and a fourth
// serial that is not on the bus.
func blockedRig(t *testing.T) *rig {
	g := newRig(t, 1000, serials(4)...)
	g.drives.edit("SERIAL01", func(d *drives.Drive) { d.LinkMbps = new(12) })
	g.drives.edit("SERIAL02", func(d *drives.Drive) {
		d.Status, d.Media, d.Tray = drives.StatusNoDisc, new(false), new("open")
	})
	g.drives.remove("SERIAL03")
	return g
}

func TestIdentifyBlockedRows(t *testing.T) {

	t.Run("every drive", func(t *testing.T) {
		g := blockedRig(t)
		st := g.identify(testIdentify())

		if len(st.Discs) != 3 {
			t.Fatalf("%d discs, want one per listed drive", len(st.Discs))
		}
		wantReason := map[string]string{
			"SERIAL01": "link is 12 Mbit/s, not 480: the hub tree needs the reset runbook, which needs root on the node",
			"SERIAL02": "no disc in the drive (tray open, state no_disc)",
		}
		for _, d := range st.Discs {
			switch d.Serial {
			case "SERIAL00":
				if d.State != runstore.DiscDone {
					t.Errorf("the loaded drive = %s %q", d.State, deref(d.Reason))
				}
			default:
				if d.State != runstore.DiscBlocked || deref(d.Reason) != wantReason[d.Serial] {
					t.Errorf("%s = %s %q, want blocked %q", d.Serial, d.State, deref(d.Reason), wantReason[d.Serial])
				}
			}
		}
		if n := len(g.runner.callsTo("makemkvcon", "info")); n != 1 {
			t.Errorf("%d info passes, want only the loaded drive's", n)
		}
		if st.State != runstore.RunIncomplete || len(st.Problems) != 2 {
			t.Errorf("run = %s, problems %+v", st.State, st.Problems)
		}
	})

	t.Run("a serial that is not on the bus", func(t *testing.T) {
		g := blockedRig(t)
		st := g.identify(testIdentify("SERIAL00", "SERIAL03"))

		if st.Discs[0].State != runstore.DiscDone {
			t.Errorf("disc 0 = %s", st.Discs[0].State)
		}
		d := st.Discs[1]
		if d.State != runstore.DiscBlocked || !strings.Contains(deref(d.Reason), "SERIAL03 is not on the USB bus") {
			t.Errorf("disc 1 = %s %q", d.State, deref(d.Reason))
		}
		if d.Label != "SERIAL03" {
			t.Errorf("a blocked disc is labelled %q, want its serial", d.Label)
		}
	})
}
