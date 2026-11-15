package cards

import (
	"os"
	"strings"
	"testing"
)

func readLines(t *testing.T, name string) []string {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimRight(string(b), "\n"), "\n")
}

func TestStatsParserReadsARealTranscript(t *testing.T) {
	var p StatsParser
	for _, l := range readLines(t, "signalstats-clip.txt") {
		p.Feed(l)
	}
	got, err := p.Samples()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 8 {
		t.Fatalf("%d samples, want 8 (frames 5..12)", len(got))
	}
	want := Sample{T: 6, YAvg: 89.2536, UAvg: 106.916, VAvg: 189.417, YMax: 236}
	if got[1].T != want.T || got[1].YAvg != want.YAvg || got[1].VAvg != want.VAvg || got[1].YMax != want.YMax {
		t.Errorf("the card frame = %+v, want %+v", got[1], want)
	}
	if got[0].YMax != 214 || got[0].T != 5 {
		t.Errorf("the moving frame = %+v", got[0])
	}
}

func TestStatsParserMatchesByKey(t *testing.T) {
	// YAVG, YMAX and the 15 other figures all share a prefix; each must land
	// in its own field regardless of order.
	lines := []string{
		"[Parsed_metadata_2 @ 0x1] frame:0    pts:0       pts_time:12.5",
		"[Parsed_metadata_2 @ 0x1] lavfi.signalstats.VAVG=4",
		"[Parsed_metadata_2 @ 0x1] lavfi.signalstats.VMAX=250",
		"[Parsed_metadata_2 @ 0x1] lavfi.signalstats.UAVG=3",
		"[Parsed_metadata_2 @ 0x1] lavfi.signalstats.YMAX=2",
		"[Parsed_metadata_2 @ 0x1] lavfi.signalstats.YAVG=1",
		"[Parsed_metadata_2 @ 0x1] lavfi.signalstats.YAVGX=99",
	}
	var p StatsParser
	for _, l := range lines {
		p.Feed(l)
	}
	got, err := p.Samples()
	if err != nil || len(got) != 1 {
		t.Fatalf("%v, %v", got, err)
	}
	if got[0] != (Sample{T: 12.5, YAvg: 1, UAvg: 3, VAvg: 4, YMax: 2}) {
		t.Errorf("got %+v", got[0])
	}
}

func TestStatsParserFailsClosed(t *testing.T) {
	hdr := "[Parsed_metadata_2 @ 0x1] frame:0    pts:0       pts_time:0"
	cases := map[string][]string{
		"a frame with no VAVG":  {hdr, "lavfi.signalstats.YAVG=1", "lavfi.signalstats.UAVG=1", "lavfi.signalstats.YMAX=1"},
		"a figure that is NaN":  {hdr, "lavfi.signalstats.YAVG=nan"},
		"a figure that is text": {hdr, "lavfi.signalstats.YAVG=high"},
		"a time that is text":   {"[Parsed_metadata_2 @ 0x1] frame:0    pts:0       pts_time:soon"},
	}
	for name, lines := range cases {
		t.Run(name, func(t *testing.T) {
			var p StatsParser
			for _, l := range lines {
				p.Feed(l)
			}
			if got, err := p.Samples(); err == nil {
				t.Fatalf("accepted %+v", got)
			}
		})
	}
}

func TestStatsParserIgnoresOtherOutput(t *testing.T) {
	var p StatsParser
	for _, l := range []string{"Input #0, matroska,webm, from 'x.mkv':", "  Duration: 00:00:20.00", "frame=  500 fps=0.0 q=-0.0 Lsize=N/A"} {
		p.Feed(l)
	}
	got, err := p.Samples()
	if err != nil || len(got) != 0 {
		t.Fatalf("%v, %v", got, err)
	}
}

func TestFreezeParserClosedFreeze(t *testing.T) {
	var p FreezeParser
	for _, l := range readLines(t, "freezedetect-closed.txt") {
		p.Feed(l)
	}
	done, open, err := p.Freezes()
	if err != nil {
		t.Fatal(err)
	}
	if len(done) != 1 || done[0] != (Freeze{Start: 6, Duration: 5}) || open != nil {
		t.Fatalf("done %v, open %v", done, open)
	}
}

// ffmpeg 9.0.2 prints freeze_start for a freeze that is still running when the
// input ends and then nothing: no duration, no end (testdata/freezedetect-open.txt).
func TestFreezeParserOpenFreezeAtEndOfInput(t *testing.T) {
	var p FreezeParser
	for _, l := range readLines(t, "freezedetect-open.txt") {
		p.Feed(l)
	}
	done, open, err := p.Freezes()
	if err != nil {
		t.Fatal(err)
	}
	if len(done) != 1 || done[0] != (Freeze{Start: 6, Duration: 5}) {
		t.Errorf("done %v", done)
	}
	if open == nil || *open != 16 {
		t.Errorf("open = %v, want the freeze that starts at 16", open)
	}
}

func TestFreezeParserRejectsGarbage(t *testing.T) {
	var p FreezeParser
	p.Feed("[Parsed_freezedetect_0 @ 0x1] lavfi.freezedetect.freeze_start: soon")
	if _, _, err := p.Freezes(); err == nil {
		t.Fatal("accepted a start that is not a number")
	}
}
