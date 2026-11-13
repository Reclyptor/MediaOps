package schedule

import (
	"testing"
	"time"
)

var t0 = time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)

func TestGate(t *testing.T) {
	tests := []struct {
		name   string
		setup  func(g *Gate)
		counts Counts
		at     time.Duration
		want   bool
	}{
		{"nothing active", func(*Gate) {}, Counts{}, 0, true},
		{"below the cap", func(*Gate) {}, Counts{Ripping: 1}, 0, true},
		{"concurrency cap alone", func(*Gate) {}, Counts{Ripping: 2}, 0, false},
		{"cap counts scanning discs too", func(*Gate) {}, Counts{Scanning: 1, Ripping: 1}, 0, false},
		{"one scanning alone", func(*Gate) {}, Counts{Scanning: 1}, 0, false},
		{"gap alone, still closed", func(g *Gate) { g.ScanEnded(t0) }, Counts{Ripping: 1}, 29 * time.Second, false},
		{"gap, opens exactly at the end", func(g *Gate) { g.ScanEnded(t0) }, Counts{Ripping: 1}, 30 * time.Second, true},
		{"pause alone, still paused", func(g *Gate) { g.Pause(t0, 2*time.Minute) }, Counts{}, time.Minute, false},
		{"pause over", func(g *Gate) { g.Pause(t0, 2*time.Minute) }, Counts{}, 2 * time.Minute, true},
		{"all released", func(g *Gate) {
			g.ScanEnded(t0)
			g.Pause(t0, time.Minute)
		}, Counts{Ripping: 1}, 2 * time.Minute, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := Gate{MaxConcurrent: 2, Gap: 30 * time.Second}
			tt.setup(&g)
			if got := g.MayStart(t0.Add(tt.at), tt.counts); got != tt.want {
				t.Errorf("MayStart = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestPause(t *testing.T) {
	var g Gate
	if _, ok := g.PausedUntil(t0); ok {
		t.Fatal("a fresh gate reports a pause")
	}
	g.Pause(t0, 2*time.Minute)
	until, ok := g.PausedUntil(t0.Add(time.Minute))
	if !ok || !until.Equal(t0.Add(2*time.Minute)) {
		t.Fatalf("PausedUntil = %v, %v", until, ok)
	}
	g.Pause(t0, time.Second)
	if until, _ := g.PausedUntil(t0); !until.Equal(t0.Add(2 * time.Minute)) {
		t.Errorf("a shorter pause shortened the longer one: %v", until)
	}
	if _, ok := g.PausedUntil(t0.Add(2 * time.Minute)); ok {
		t.Error("pause still reported at its end")
	}
}
