package cards

import (
	"reflect"
	"testing"

	"github.com/reclyptor/mediaops/internal/cli"
)

// These are the measured signatures of §12.3, one test per row. Each builds
// the episode the measurement describes as a sampled timeline, runs the real
// command over it with the flags the measurement used, and asserts what that
// measurement found. The numbers come from the docstrings of
// skills/anime-normalize/scripts/cardfind.py and cardfreeze.py, and from
// _plans/bin/cardfind2.py; each test cites its lines.

// span is a stretch of a timeline [from, to) with its own look.
type span struct {
	from, to float64
	look     func(t float64) colour
}

// timeline plays base except inside the spans.
func timeline(base func(float64) colour, spans ...span) func(float64) colour {
	return func(t float64) colour {
		for _, s := range spans {
			if t >= s.from && t < s.to {
				return s.look(t)
			}
		}
		return base(t)
	}
}

// series is a look that walks through values, one per second from `from`.
func series(from float64, y, u, v, ymax []float64) func(float64) colour {
	at := func(xs []float64, i int) float64 { return xs[i%len(xs)] }
	return func(t float64) colour {
		i := int(t - from)
		return colour{y: at(y, i), u: at(u, i), v: at(v, i), ymax: at(ymax, i)}
	}
}

func findOne(t *testing.T, look func(float64) colour, args ...string) (FindFile, int) {
	t.Helper()
	path := touch(t, "e01.mkv")
	ff := &fakeFFmpeg{t: t, files: map[string]*fakeFile{path: {length: 1400, look: look}}}
	out, code := run(t, ff, append([]string{"find", path}, args...)...)
	rep := decodeFind(t, ff, append([]string{"find", path}, args...)...)
	if len(rep.Files) != 1 {
		t.Fatalf("stdout:\n%s", out)
	}
	return rep.Files[0], code
}

// cardfind.py:12 and :20: Berserk's title card is red on black and
// byte-identical across its hold, Y~17.7 V~128.4, found with
// `--maxy 25 --minv 127 --static` at tol 0.01. The footage around it is dark
// too, so the hold is the only stretch whose Y does not move.
func TestSignatureBerserkHeldRedOnBlack(t *testing.T) {
	dark := func(tt float64) colour {
		k := int(tt)
		return colour{y: 15 + float64(k%5)*1.9, u: 128, v: 127.4 + float64(k%4)*0.4, ymax: 90}
	}
	hold := span{130, 134, func(float64) colour { return colour{y: 17.7, u: 127.9, v: 128.4, ymax: 60} }}
	look := timeline(dark, hold)

	f, code := findOne(t, look, "--maxy", "25", "--minv", "127", "--static")
	if code != cli.ExitOK || len(f.Candidates) != 1 {
		t.Fatalf("exit %d, %+v", code, f)
	}
	c := f.Candidates[0]
	if c.Start != 130 || c.End != 133 || c.Frames != 4 || c.At != 132 || c.YAvg != 17.7 || c.VAvg != 128.4 {
		t.Errorf("candidate = %+v", c)
	}
	if f.Best == nil || *f.Best != 132 {
		t.Errorf("best = %v", f.Best)
	}

	// Without --static the dark footage matches too and the whole window is one
	// run: colour alone cannot find this card.
	f, _ = findOne(t, look, "--maxy", "25", "--minv", "127")
	if len(f.Candidates) != 1 || f.Candidates[0].Frames != f.Samples {
		t.Errorf("without --static: %+v", f.Candidates)
	}
}

// cardfind.py:13 and :66-69: Cutie Honey's card is white on red, Y~77 U~105
// V~174-188, within `--lo 90 --hi 420 --minv 160 --maxu 115`. Red scenery at
// V~155-165 precedes it and passes the same flags, so the first run is wrong
// (4/10 on the real episodes) and the most saturated run is the card.
func TestSignatureCutieHoneyMostSaturatedRunNotTheFirst(t *testing.T) {
	neutral := func(tt float64) colour { return colour{y: 110, u: 128, v: 128 + float64(int(tt)%3), ymax: 200} }
	scenery := span{100, 106, series(100, []float64{70}, []float64{110}, []float64{158, 162, 164, 161, 165, 159}, []float64{190})}
	card := span{200, 205, series(200, []float64{77}, []float64{105}, []float64{174, 180, 188, 181, 176}, []float64{235})}
	look := timeline(neutral, scenery, card)

	f, code := findOne(t, look, "--lo", "90", "--hi", "420", "--minv", "160", "--maxu", "115")
	if code != cli.ExitOK || len(f.Candidates) != 2 {
		t.Fatalf("exit %d, %+v", code, f)
	}
	first, second := f.Candidates[0], f.Candidates[1]
	if first.At != 103 || first.Frames != 4 || first.VAvg != 163 {
		t.Errorf("the scenery run = %+v", first)
	}
	if second.Start != 200 || second.End != 204 || second.At != 202 || second.VAvg != 179.8 {
		t.Errorf("the card run = %+v", second)
	}
	if f.Best == nil || *f.Best != 202 {
		t.Fatalf("best = %v, want the card at 202", f.Best)
	}
	if first.At == *f.Best {
		t.Error("a first-run rule would have picked the card as well, so this fixture proves nothing")
	}
}

// cardfind.py:20-22: the card breathes under its fade, so Y drifts a few tenths
// from second to second. `--static` at tol 0.01 misses it; tol ~0.5 finds it.
func TestSignatureCutieHoneyBreathingCardNeedsTolerance(t *testing.T) {
	neutral := func(float64) colour { return colour{y: 110, u: 128, v: 128, ymax: 200} }
	card := span{200, 205, series(200, []float64{77.0, 77.3, 77.6, 77.2, 76.9}, []float64{105}, []float64{181}, []float64{235})}
	look := timeline(neutral, card)
	flags := []string{"--lo", "90", "--hi", "420", "--minv", "160", "--maxu", "115", "--static"}

	f, code := findOne(t, look, flags...)
	if code != cli.ExitIncomplete || len(f.Candidates) != 0 || f.Best != nil {
		t.Errorf("tol 0.01: exit %d, %+v", code, f)
	}
	f, code = findOne(t, look, append(flags, "--tol", "0.5")...)
	if code != cli.ExitOK || len(f.Candidates) != 1 || f.Candidates[0].Frames != 5 || *f.Best != 202 {
		t.Errorf("tol 0.5: exit %d, %+v", code, f)
	}
}

func cutieFreezeFile(t *testing.T) (path string, ff *fakeFFmpeg) {
	t.Helper()
	path = touch(t, "e01.mkv")
	neutral := func(float64) colour { return colour{y: 110, u: 128, v: 128, ymax: 200} }
	// frozen red scenery: more saturated than the card, but no white glyphs
	scenery := span{100, 104, func(float64) colour { return colour{y: 70, u: 112, v: 184, ymax: 190} }}
	card := span{200, 205, func(float64) colour { return colour{y: 77, u: 105, v: 181, ymax: 235} }}
	ff = &fakeFFmpeg{t: t, files: map[string]*fakeFile{path: {
		length: 600,
		look:   timeline(neutral, scenery, card),
		// grain on this 1973 film exceeds -55 dB, so only -30 dB sees the plates
		freezes: []fakeFreeze{{start: 100, duration: 4, floor: -30}, {start: 200, duration: 5, floor: -30}},
	}}}
	return path, ff
}

// cardfreeze.py:13-17 and :35-38: freezedetect at -30 dB isolates the held
// plate (-55 dB finds nothing on this grain); the card's white glyphs pin YMAX
// near 235 while a frozen red background stays far below, which is what
// `--minymax` separates. Without it the scan returned 63 candidates in one
// episode.
func TestSignatureCutieHoneyFreezeAndMinYMax(t *testing.T) {
	path, ff := cutieFreezeFile(t)

	rep := decodeFreeze(t, ff, "freeze", path)
	f := rep.Files[0]
	if rep.Params.DB != -30 || len(f.Candidates) != 2 {
		t.Fatalf("defaults: %+v", rep)
	}
	if f.Best == nil || *f.Best != 102 {
		t.Fatalf("without --minymax the more saturated scenery wins, got best %v", f.Best)
	}

	rep = decodeFreeze(t, ff, "freeze", path, "--minymax", "230")
	f = rep.Files[0]
	if len(f.Candidates) != 1 || f.Best == nil || *f.Best != 202.5 {
		t.Fatalf("--minymax 230: %+v", f)
	}
	if len(f.Rejected) != 1 || f.Rejected[0].At != 102 || *f.Rejected[0].YMax != 190 {
		t.Errorf("the scenery should be listed as rejected with its YMAX: %+v", f.Rejected)
	}
	if c := f.Candidates[0]; c.YMax != 235 || c.UAvg != 105 || c.VAvg != 181 {
		t.Errorf("card = %+v", c)
	}

	out, code := run(t, ff, "freeze", path, "--db", "-55")
	var none FreezeReport
	if err := jsonInto(out, &none); err != nil {
		t.Fatal(err)
	}
	if code != cli.ExitIncomplete || none.Files[0].Freezes != 0 || len(none.Files[0].Candidates) != 0 {
		t.Errorf("-55 dB: exit %d, %+v", code, none.Files[0])
	}
}

// cardfind2.py:3-7: the cards of one show do not share a palette. E04's is
// dark blue, U=131 V=123, which neither the Cutie Honey colour flags nor the
// freeze colour defaults can accept. This is the documented limit of a colour
// method: the answer is `none` and exit 1, and the caller falls back to a
// contact sheet.
func TestSignatureCutieHoneyDarkBlueCardIsTheDocumentedLimit(t *testing.T) {
	path := touch(t, "e04.mkv")
	neutral := func(float64) colour { return colour{y: 110, u: 128, v: 128, ymax: 200} }
	card := span{150, 156, func(float64) colour { return colour{y: 40, u: 131, v: 123, ymax: 235} }}
	ff := &fakeFFmpeg{t: t, files: map[string]*fakeFile{path: {
		length: 600, look: timeline(neutral, card),
		freezes: []fakeFreeze{{start: 150, duration: 6, floor: -30}},
	}}}

	out, code := run(t, ff, "find", path, "--lo", "90", "--hi", "420", "--minv", "160", "--maxu", "115")
	var found FindReport
	if err := jsonInto(out, &found); err != nil {
		t.Fatal(err)
	}
	f := found.Files[0]
	if code != cli.ExitIncomplete || found.Found != 0 || len(f.Candidates) != 0 || f.Best != nil || f.Error != "" {
		t.Errorf("find: exit %d, %+v", code, f)
	}

	out, code = run(t, ff, "freeze", path)
	var frozen FreezeReport
	if err := jsonInto(out, &frozen); err != nil {
		t.Fatal(err)
	}
	z := frozen.Files[0]
	if code != cli.ExitIncomplete || len(z.Candidates) != 0 || len(z.Rejected) != 1 {
		t.Fatalf("freeze: exit %d, %+v", code, z)
	}
	if want := (FreezeRejected{Start: 150, Duration: 6, At: 153}); z.Rejected[0].Start != want.Start || *z.Rejected[0].VAvg != 123 || *z.Rejected[0].UAvg != 131 {
		t.Errorf("rejected = %+v", z.Rejected[0])
	}
}

// The transcript ffmpeg 9.0.2 actually printed, parsed and run through the
// grammar: the held card of testdata/signalstats-clip.txt is one run, 6..10 s.
func TestRealTranscriptHasTheCardAsOneRun(t *testing.T) {
	var p StatsParser
	for _, l := range readLines(t, "signalstats-clip.txt") {
		p.Feed(l)
	}
	samples, err := p.Samples()
	if err != nil {
		t.Fatal(err)
	}
	fp := FindParams{MaxY: 255, MaxU: 255, MinV: 150, MaxV: 255, MinRun: 3, Static: true, Tol: 0.05}
	got := times(Runs(samples, fp))
	if want := [][]float64{{6, 7, 8, 9, 10}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("runs = %v, want %v", got, want)
	}
}
