package cards

import (
	"reflect"
	"strings"
	"testing"
)

func solid(c colour) func(float64) colour { return func(float64) colour { return c } }

func TestFreezeFileArgvsAreExact(t *testing.T) {
	path := touch(t, "e01.mkv")
	ff := &fakeFFmpeg{t: t, files: map[string]*fakeFile{path: {
		length:  600,
		look:    solid(colour{y: 80, u: 105, v: 181, ymax: 235}),
		freezes: []fakeFreeze{{start: 200, duration: 5, floor: -30}},
	}}}
	run(t, ff, "freeze", path)
	head := []string{"ffmpeg", "-nostdin", "-hide_banner", "-nostats", "-v", "info"}
	want := [][]string{
		append(append([]string{}, head...), "-ss", "92", "-t", "428", "-i", path, "-map", "0:v:0",
			"-vf", "freezedetect=n=-30dB:d=3.5", "-f", "null", "-"),
		append(append([]string{}, head...), "-ss", "202.5", "-t", "1", "-i", path, "-map", "0:v:0",
			"-vf", "fps=1,signalstats,metadata=print", "-f", "null", "-"),
	}
	if !reflect.DeepEqual(ff.argvs, want) {
		t.Fatalf("argvs =\n%q\nwant\n%q", ff.argvs, want)
	}
}

func TestFreezeSamplesTheLastFrameOfTheMidpointSecond(t *testing.T) {
	path := touch(t, "e01.mkv")
	// the card is fading in at its edges and settled by the midpoint; the
	// 1 s sample at the midpoint reads the figures the transcript ends on
	look := func(tt float64) colour {
		if tt < 202.5 {
			return colour{y: 10, u: 10, v: 10, ymax: 10}
		}
		return colour{y: 80, u: 105, v: 181, ymax: 235}
	}
	ff := &fakeFFmpeg{t: t, files: map[string]*fakeFile{path: {length: 600, look: look,
		freezes: []fakeFreeze{{start: 200, duration: 5, floor: -30}}}}}
	rep := decodeFreeze(t, ff, "freeze", path)
	if len(rep.Files[0].Candidates) != 1 {
		t.Fatalf("%+v", rep.Files[0])
	}
	got := rep.Files[0].Candidates[0]
	want := FreezeCandidate{Start: 200, Duration: 5, At: 202.5, YMax: 235, UAvg: 105, VAvg: 181}
	if got != want {
		t.Errorf("candidate = %+v, want %+v", got, want)
	}
}

func TestFreezeBestIsHighestVStrictlyGreater(t *testing.T) {
	path := touch(t, "e01.mkv")
	look := func(tt float64) colour {
		switch {
		case tt < 150: // first freeze
			return colour{u: 100, v: 170, ymax: 235}
		case tt < 250: // second: same V, must not displace the first
			return colour{u: 100, v: 170, ymax: 235}
		}
		return colour{u: 100, v: 190, ymax: 235}
	}
	ff := &fakeFFmpeg{t: t, files: map[string]*fakeFile{path: {length: 600, look: look, freezes: []fakeFreeze{
		{start: 100, duration: 4, floor: -30}, {start: 200, duration: 4, floor: -30},
	}}}}
	rep := decodeFreeze(t, ff, "freeze", path)
	if f := rep.Files[0]; len(f.Candidates) != 2 || f.Best == nil || *f.Best != 102 {
		t.Fatalf("%+v", f)
	}

	ff.files[path].freezes = append(ff.files[path].freezes, fakeFreeze{start: 300, duration: 4, floor: -30})
	rep = decodeFreeze(t, ff, "freeze", path)
	if f := rep.Files[0]; len(f.Candidates) != 3 || f.Best == nil || *f.Best != 302 {
		t.Fatalf("a higher V did not win: %+v", f)
	}
}

func TestFreezeRejectsAreListedWithTheirValues(t *testing.T) {
	path := touch(t, "e01.mkv")
	ff := &fakeFFmpeg{t: t, files: map[string]*fakeFile{path: {
		length:  600,
		look:    solid(colour{y: 60, u: 130, v: 120, ymax: 190}),
		freezes: []fakeFreeze{{start: 100, duration: 4, floor: -30}},
	}}}
	rep := decodeFreeze(t, ff, "freeze", path, "--minymax", "230")
	f := rep.Files[0]
	if len(f.Candidates) != 0 || len(f.Rejected) != 1 || f.Best != nil {
		t.Fatalf("%+v", f)
	}
	r := f.Rejected[0]
	if r.At != 102 || r.Start != 100 || r.Duration != 4 || r.YMax == nil || *r.YMax != 190 || *r.UAvg != 130 || *r.VAvg != 120 {
		t.Errorf("rejected = %+v", r)
	}
	for _, want := range []string{"V 120 is outside 150..255", "U 130 is outside 0..122", "YMAX 190 is below 230"} {
		if !strings.Contains(r.Reason, want) {
			t.Errorf("reason %q lacks %q", r.Reason, want)
		}
	}
}

func TestFreezeWithNoFrameSampledIsRejectedNotDropped(t *testing.T) {
	path := touch(t, "e01.mkv")
	// signalstats yields nothing at the midpoint, as on a damaged stretch
	ff := &fakeFFmpeg{t: t, files: map[string]*fakeFile{path: {
		length:  600,
		blind:   101,
		look:    solid(colour{y: 60, u: 100, v: 170, ymax: 235}),
		freezes: []fakeFreeze{{start: 100, duration: 4, floor: -30}},
	}}}
	rep := decodeFreeze(t, ff, "freeze", path)
	f := rep.Files[0]
	if len(f.Rejected) != 1 || f.Rejected[0].Reason != "no frame sampled" || f.Rejected[0].YMax != nil {
		t.Fatalf("%+v", f)
	}
}

// A freeze that is still running when the window ends is printed by ffmpeg as
// a start and nothing more (testdata/freezedetect-open.txt). The Python dropped
// it silently; it is a note now.
func TestFreezeStillRunningAtTheEndOfTheWindowIsANote(t *testing.T) {
	path := touch(t, "e01.mkv")
	ff := &fakeFFmpeg{t: t, files: map[string]*fakeFile{path: {
		length:  600,
		look:    solid(colour{y: 80, u: 105, v: 181, ymax: 235}),
		freezes: []fakeFreeze{{start: 500, duration: 60, floor: -30}},
	}}}
	rep := decodeFreeze(t, ff, "freeze", path)
	f := rep.Files[0]
	if len(f.Candidates) != 0 || f.Freezes != 0 || len(f.Notes) != 1 {
		t.Fatalf("%+v", f)
	}
	for _, want := range []string{"500s", "520s", "--hi"} {
		if !strings.Contains(f.Notes[0], want) {
			t.Errorf("note %q lacks %q", f.Notes[0], want)
		}
	}
}

func TestFreezeNonZeroExitIsTheFilesError(t *testing.T) {
	path := touch(t, "e01.mkv")
	ff := &fakeFFmpeg{t: t, files: map[string]*fakeFile{path: {exit: 183}}}
	rep := decodeFreeze(t, ff, "freeze", path)
	if e := rep.Files[0].Error; !strings.Contains(e, "ffmpeg exited 183") || !strings.Contains(e, "Invalid data") {
		t.Fatalf("error = %q", e)
	}
}
