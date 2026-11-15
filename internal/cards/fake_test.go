package cards

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/reclyptor/mediaops/internal/cli"
	"github.com/reclyptor/mediaops/kit/proc"
)

// colour is what signalstats would measure on one frame.
type colour struct{ y, u, v, ymax float64 }

// fakeFile is a video, described by what any second of it looks like and by
// the freezes freezedetect would report on it.
type fakeFile struct {
	length  float64
	look    func(t float64) colour
	freezes []fakeFreeze
	// exit makes every ffmpeg on this file fail with this status.
	exit int
	// blind, when set, is where signalstats stops producing frames.
	blind float64
}

// fakeFreeze is a frozen span in the file's timeline. floor is the grain level
// in dB: the span is reported only when the filter's -n is at or above it, so
// -55 dB finds nothing on grainy film.
type fakeFreeze struct {
	start, duration, floor float64
}

// fakeFFmpeg answers ffmpeg argvs the way the real one prints its logs, from
// files keyed by path. It runs every argv through the real guard.
type fakeFFmpeg struct {
	t     *testing.T
	files map[string]*fakeFile

	mu    sync.Mutex
	argvs [][]string
}

func (f *fakeFFmpeg) Run(context.Context, ...string) (proc.Result, error) {
	panic("not used")
}

func (f *fakeFFmpeg) Pipe(context.Context, []string, func(io.Reader) error) ([]byte, int, error) {
	panic("not used")
}

func (f *fakeFFmpeg) Stream(_ context.Context, argv []string, onLine func(string)) (int, error) {
	f.t.Helper()
	if err := proc.Check(argv); err != nil {
		f.t.Errorf("guard refused %q: %v", argv, err)
	}
	f.mu.Lock()
	f.argvs = append(f.argvs, argv)
	f.mu.Unlock()

	file := f.files[argAfter(argv, "-i")]
	if file == nil {
		onLine("Error opening input file.")
		return 1, nil
	}
	if file.exit != 0 {
		onLine("Invalid data found when processing input")
		return file.exit, nil
	}
	lo := mustFloat(f.t, argAfter(argv, "-ss"))
	dur := mustFloat(f.t, argAfter(argv, "-t"))
	end := min(lo+dur, file.length)
	filter := argAfter(argv, "-vf")
	switch {
	case strings.Contains(filter, "signalstats"):
		if file.blind > 0 {
			end = min(end, file.blind)
		}
		for k := 0; lo+float64(k) < end; k++ {
			for _, l := range statsFrame(k, file.look(lo+float64(k))) {
				onLine(l)
			}
		}
	case strings.HasPrefix(filter, "freezedetect"):
		dbText, _, _ := strings.Cut(strings.TrimPrefix(filter, "freezedetect=n="), "dB:d=")
		db := mustFloat(f.t, dbText)
		for _, z := range file.freezes {
			if z.floor > db || z.start < lo || z.start >= end {
				continue
			}
			for _, l := range freezeLines(z.start-lo, z.duration, z.start+z.duration <= end) {
				onLine(l)
			}
		}
	default:
		f.t.Errorf("unexpected filter %q", filter)
	}
	return 0, nil
}

func argAfter(argv []string, flag string) string {
	for i, a := range argv[:len(argv)-1] {
		if a == flag {
			return argv[i+1]
		}
	}
	return ""
}

func mustFloat(t *testing.T, s string) float64 {
	t.Helper()
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		t.Fatalf("%q is not a number", s)
	}
	return v
}

// statsFrame is one frame of metadata=print output, in ffmpeg's own key order.
func statsFrame(k int, c colour) []string {
	const p = "[Parsed_metadata_2 @ 0x7c480c004dc0] "
	n := func(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }
	return []string{
		fmt.Sprintf("%sframe:%-4d pts:%-7d pts_time:%d", p, k, k, k),
		p + "lavfi.signalstats.YMIN=16",
		p + "lavfi.signalstats.YAVG=" + n(c.y),
		p + "lavfi.signalstats.YMAX=" + n(c.ymax),
		p + "lavfi.signalstats.UMAX=246",
		p + "lavfi.signalstats.UAVG=" + n(c.u),
		p + "lavfi.signalstats.VAVG=" + n(c.v),
		p + "lavfi.signalstats.VMAX=249",
		p + "lavfi.signalstats.SATAVG=105.281",
	}
}

func freezeLines(start, duration float64, closed bool) []string {
	const p = "[Parsed_freezedetect_0 @ 0x715020004780] lavfi.freezedetect."
	n := func(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }
	out := []string{p + "freeze_start: " + n(start)}
	if closed {
		out = append(out, p+"freeze_duration: "+n(duration), p+"freeze_end: "+n(start+duration))
	}
	return out
}

// touch makes an empty regular file for the command to stat.
func touch(t *testing.T, name string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// run runs Command and returns its stdout and exit code.
func run(t *testing.T, r Tools, args ...string) (string, int) {
	t.Helper()
	var out, errOut bytes.Buffer
	err := Command(context.Background(), r, args, cli.IO{Stdout: &out, Stderr: &errOut})
	return out.String(), cli.Code(err)
}
