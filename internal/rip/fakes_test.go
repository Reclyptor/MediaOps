package rip

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/reclyptor/mediaops/internal/drives"
	"github.com/reclyptor/mediaops/internal/makemkv"
	"github.com/reclyptor/mediaops/internal/plan"
	"github.com/reclyptor/mediaops/internal/runstore"
	"github.com/reclyptor/mediaops/kit/paths"
	"github.com/reclyptor/mediaops/kit/proc"
)

// The fakes in this file are shared by every test in the package: a clock that
// runs scale times faster than the wall, drives and a sysfs tree that can be
// mutated, a proc.Runner that replays the recorded makemkvcon logs, and a rig
// that wires them to a Run.

const testdataDir = "../makemkv/testdata"

// scaledClock reports time running scale times faster than the wall clock,
// from a fixed base, so a run of hours takes milliseconds.
type scaledClock struct {
	base, started time.Time
	scale         float64
}

func newClock(scale float64) *scaledClock {
	return &scaledClock{base: time.Date(2026, 11, 13, 10, 0, 0, 0, time.UTC), started: time.Now(), scale: scale}
}

func (c *scaledClock) Now() time.Time {
	return c.base.Add(time.Duration(float64(time.Since(c.started)) * c.scale))
}

// sleep waits d of clock time, or until ctx ends.
func (c *scaledClock) sleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(time.Duration(float64(d) / c.scale))
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// fakeSys is the sysfs the health watch reads: a USB directory with a speed
// file per drive and an sr node per drive.
type fakeSys struct {
	root string
}

func newFakeSys(t *testing.T) *fakeSys {
	t.Helper()
	s := &fakeSys{root: t.TempDir()}
	if err := os.MkdirAll(s.blockDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	return s
}

func (s *fakeSys) blockDir() string { return filepath.Join(s.root, "block") }

// add creates a drive that is loaded and linked at 480 Mbit/s.
func (s *fakeSys) add(t *testing.T, serial string, n int, port string) drives.Drive {
	t.Helper()
	usb := filepath.Join(s.root, "usb", serial)
	if err := os.MkdirAll(usb, 0o755); err != nil {
		t.Fatal(err)
	}
	s.setSpeed(t, serial, 480)
	if err := os.WriteFile(filepath.Join(s.blockDir(), fmt.Sprintf("sr%d", n)), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	sg, tray, media, link := fmt.Sprintf("/dev/sg%d", n+20), "closed", true, 480
	return drives.Drive{
		Serial: serial, Dev: fmt.Sprintf("/dev/sr%d", n), SG: &sg, USBPort: port, LinkMbps: &link,
		Status: drives.StatusDiscOK, Tray: &tray, Media: &media, USBDir: usb,
	}
}

func (s *fakeSys) setSpeed(t *testing.T, serial string, mbps int) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(s.root, "usb", serial, "speed"), []byte(strconv.Itoa(mbps)+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// fakeDrives implements Drives over a mutable list.
type fakeDrives struct {
	mu       sync.Mutex
	list     []drives.Drive
	lists    int
	ejected  []string // serials
	ejectErr error
	hook     func(call int, ds []drives.Drive) // may rewrite a listing; call is 1-based
}

func (f *fakeDrives) List(ctx context.Context) ([]drives.Drive, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lists++
	out := slices.Clone(f.list)
	if f.hook != nil {
		f.hook(f.lists, out)
	}
	return out, nil
}

func (f *fakeDrives) Eject(_ context.Context, d drives.Drive) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.ejectErr != nil {
		return f.ejectErr
	}
	f.ejected = append(f.ejected, d.Serial)
	return nil
}

// edit changes the stored drive with serial.
func (f *fakeDrives) edit(serial string, fn func(*drives.Drive)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range f.list {
		if f.list[i].Serial == serial {
			fn(&f.list[i])
		}
	}
}

func (f *fakeDrives) remove(serial string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.list = slices.DeleteFunc(f.list, func(d drives.Drive) bool { return d.Serial == serial })
}

func (f *fakeDrives) listCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lists
}

func (f *fakeDrives) ejects() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.ejected)
}

// fakeDisc is how the fake makemkvcon behaves for one drive. Durations are
// clock time.
type fakeDisc struct {
	Fixture                         string // a directory under the makemkv testdata; penguindrum-d2 by default
	InfoLog                         string // overrides the fixture's info log, relative to the testdata
	InfoTime, AnalyseTime, CopyTime time.Duration
	InfoExit, RipExit               int
	HangScan                        bool    // the info pass runs until it is killed
	HoldCopy                        bool    // the copy runs until it is killed
	FileScale                       float64 // ffprobe reports this multiple of each title's length; 0 means 1
	Panic                           bool    // makemkvcon's stream panics
	AfterRip                        func(partial string)
}

func (d fakeDisc) fixture() string {
	if d.Fixture == "" {
		return "penguindrum-d2"
	}
	return d.Fixture
}

// call is one invocation seen by the fake runner.
type call struct {
	argv []string
	at   time.Time
}

// scanSpan is when a drive was scanning, as the fake saw it: from the start of
// the info pass to the first byte of the copy. infoEnd is when the info pass
// returned, which is the end of the scan when no copy follows.
type scanSpan struct {
	dev                       string
	start, infoEnd, copyStart time.Time
}

// fakeRunner is a proc.Runner that replays recorded makemkvcon logs and answers
// ffprobe, mkvpropedit and mkvmerge from what it wrote. Every argv is checked
// by proc.Check first, so the allowlist binds the fake too.
type fakeRunner struct {
	clock *scaledClock

	mu         sync.Mutex
	discs      map[string]fakeDisc // by dev; the zero fakeDisc for the rest
	calls      []call
	spans      []scanSpan
	active     map[string]bool
	maxActive  int
	media      map[string]float64 // ffprobe's answer per file path
	titles     map[string]string  // segment title per file path
	dmesgOut   string
	dmesgErr   string // when set, dmesg exits 1 with this on stderr
	failTitles bool
}

func newFakeRunner(clock *scaledClock) *fakeRunner {
	return &fakeRunner{
		clock: clock, discs: map[string]fakeDisc{}, active: map[string]bool{},
		media: map[string]float64{}, titles: map[string]string{},
	}
}

func (f *fakeRunner) configure(dev string, fn func(*fakeDisc)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	d := f.discs[dev]
	fn(&d)
	f.discs[dev] = d
}

func (f *fakeRunner) disc(dev string) fakeDisc {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.discs[dev]
}

func (f *fakeRunner) setDmesg(out string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.dmesgOut = out
}

func (f *fakeRunner) failSegmentTitles() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failTitles = true
}

func (f *fakeRunner) record(argv []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, call{slices.Clone(argv), f.clock.Now()})
}

// callsTo returns the calls to prog whose argv contains verb ("" for any).
func (f *fakeRunner) callsTo(prog, verb string) []call {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []call
	for _, c := range f.calls {
		if c.argv[0] == prog && (verb == "" || slices.Contains(c.argv, verb)) {
			out = append(out, c)
		}
	}
	return out
}

func (f *fakeRunner) scanSpans() []scanSpan {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.spans)
}

func (f *fakeRunner) peakActive() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.maxActive
}

func (f *fakeRunner) begin(dev string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.active[dev] = true
	f.maxActive = max(f.maxActive, len(f.active))
	f.spans = append(f.spans, scanSpan{dev: dev, start: f.clock.Now()})
}

func (f *fakeRunner) end(dev string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.active, dev)
}

func (f *fakeRunner) markInfoEnd(dev string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := len(f.spans) - 1; i >= 0; i-- {
		if f.spans[i].dev == dev {
			f.spans[i].infoEnd = f.clock.Now()
			return
		}
	}
}

func (f *fakeRunner) markCopy(dev string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := len(f.spans) - 1; i >= 0; i-- {
		if f.spans[i].dev == dev {
			f.spans[i].copyStart = f.clock.Now()
			return
		}
	}
}

func (f *fakeRunner) Run(_ context.Context, argv ...string) (proc.Result, error) {
	if err := proc.Check(argv); err != nil {
		return proc.Result{ExitCode: -1}, err
	}
	f.record(argv)
	f.mu.Lock()
	defer f.mu.Unlock()
	switch argv[0] {
	case "dmesg":
		if f.dmesgErr != "" {
			return proc.Result{ExitCode: 1, Stderr: []byte(f.dmesgErr)}, nil
		}
		return proc.Result{Stdout: []byte(f.dmesgOut)}, nil
	case "ffprobe":
		secs, ok := f.media[argv[len(argv)-1]]
		if !ok {
			return proc.Result{ExitCode: 1, Stderr: []byte("No such file or directory")}, nil
		}
		return proc.Result{Stdout: fmt.Appendf(nil, `{"format":{"duration":"%.6f"}}`, secs)}, nil
	case "mkvpropedit":
		if f.failTitles {
			return proc.Result{ExitCode: 2, Stdout: []byte("Error: the file is not writable")}, nil
		}
		for i, a := range argv {
			if title, ok := strings.CutPrefix(a, "title="); ok && i > 0 {
				f.titles[argv[1]] = title
			}
		}
		return proc.Result{}, nil
	case "mkvmerge":
		out, _ := json.Marshal(map[string]any{"container": map[string]any{"properties": map[string]any{"title": f.titles[argv[len(argv)-1]]}}})
		return proc.Result{Stdout: out}, nil
	}
	return proc.Result{ExitCode: -1}, fmt.Errorf("fake runner: no answer for %v", argv)
}

func (f *fakeRunner) Stream(ctx context.Context, argv []string, onLine func(string)) (int, error) {
	if err := proc.Check(argv); err != nil {
		return -1, err
	}
	f.record(argv)
	if argv[0] != "makemkvcon" {
		return -1, fmt.Errorf("fake runner: cannot stream %v", argv)
	}
	dev, verb := makemkvInvocation(argv)
	disc := f.disc(dev)
	if verb == "info" {
		return f.info(ctx, dev, disc, onLine)
	}
	return f.rip(ctx, dev, disc, argv[len(argv)-1], onLine)
}

// makemkvInvocation picks the device and the verb out of a makemkvcon argv.
func makemkvInvocation(argv []string) (dev, verb string) {
	for _, a := range argv {
		switch {
		case strings.HasPrefix(a, "dev:"):
			dev = strings.TrimPrefix(a, "dev:")
		case a == "info" || a == "mkv":
			verb = a
		}
	}
	return dev, verb
}

func (f *fakeRunner) info(ctx context.Context, dev string, disc fakeDisc, onLine func(string)) (int, error) {
	f.begin(dev)
	defer f.markInfoEnd(dev)
	if disc.Panic {
		panic("the fake makemkvcon blew up")
	}
	if err := f.clock.sleep(ctx, disc.InfoTime); err != nil {
		f.end(dev)
		return -1, err
	}
	if disc.HangScan {
		<-ctx.Done()
		f.end(dev)
		return -1, ctx.Err()
	}
	name := disc.InfoLog
	if name == "" {
		name = disc.fixture() + "/info.log"
	}
	for _, line := range readFixture(name) {
		onLine(line)
	}
	if disc.InfoExit != 0 {
		f.end(dev)
	}
	return disc.InfoExit, nil
}

// rip replays the recorded copy: the analysis lines, a pause, the first
// "Saving to MKV file" step with the files appearing in dest, a pause, and the
// rest.
func (f *fakeRunner) rip(ctx context.Context, dev string, disc fakeDisc, dest string, onLine func(string)) (int, error) {
	defer f.end(dev)
	lines := readFixture(disc.fixture() + "/rip.log")
	first := slices.IndexFunc(lines, func(l string) bool { return strings.HasPrefix(l, "PRGC:") && strings.Contains(l, "Saving to MKV file") })
	if first < 0 {
		return -1, fmt.Errorf("fake runner: %s/rip.log never starts copying", disc.fixture())
	}
	for _, line := range lines[:first] {
		onLine(line)
	}
	if err := f.clock.sleep(ctx, disc.AnalyseTime); err != nil {
		return -1, err
	}
	f.markCopy(dev)
	if err := f.writeFiles(dest, disc); err != nil {
		return -1, err
	}
	onLine(lines[first])
	if disc.HoldCopy {
		<-ctx.Done()
		return -1, ctx.Err()
	}
	if err := f.clock.sleep(ctx, disc.CopyTime); err != nil {
		return -1, err
	}
	for _, line := range lines[first+1:] {
		onLine(line)
	}
	if disc.AfterRip != nil {
		disc.AfterRip(dest)
	}
	return disc.RipExit, nil
}

// writeFiles creates one .mkv in dest for every title the rip log announces,
// named as the info log says, and teaches ffprobe how long each is.
func (f *fakeRunner) writeFiles(dest string, disc fakeDisc) error {
	scale := disc.FileScale
	if scale == 0 {
		scale = 1
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for name, secs := range fixtureOutputs(disc) {
		path := filepath.Join(dest, name)
		if err := os.WriteFile(path, []byte("not really matroska"), 0o666); err != nil {
			return err
		}
		f.media[path] = secs*scale + 0.321
	}
	return nil
}

// fixtureOutputs is the file name and length in seconds of every title the
// recorded rip log announces, as the info log names and measures them.
func fixtureOutputs(disc fakeDisc) map[string]float64 {
	infoName := disc.InfoLog
	if infoName == "" {
		infoName = disc.fixture() + "/info.log"
	}
	info, rip := makemkv.NewParser(), makemkv.NewParser()
	for _, l := range readFixture(infoName) {
		info.Feed(l)
	}
	for _, l := range readFixture(disc.fixture() + "/rip.log") {
		rip.Feed(l)
	}
	out := map[string]float64{}
	for id := range rip.Announced() {
		title, _ := info.Title(id)
		name := title.OutputName
		if name == "" {
			name = fmt.Sprintf("title_t%02d.mkv", id)
		}
		secs, _ := title.Seconds()
		out[name] = float64(secs)
	}
	return out
}

func readFixture(name string) []string {
	file, err := os.Open(filepath.Join(testdataDir, name))
	if err != nil {
		panic(err)
	}
	defer file.Close()
	var lines []string
	sc := bufio.NewScanner(file)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		lines = append(lines, sc.Text())
	}
	if err := sc.Err(); err != nil {
		panic(err)
	}
	return lines
}

// syncBuffer is a bytes.Buffer safe to read while the loop writes it.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

type aliveAlways struct{}

func (aliveAlways) Alive(string) (bool, error) { return true, nil }

// rig is a Run's world: fakes, a staging tree and a run store in temp dirs.
type rig struct {
	t      *testing.T
	clock  *scaledClock
	sys    *fakeSys
	drives *fakeDrives
	runner *fakeRunner
	paths  paths.Config
	store  *runstore.Store
	log    *syncBuffer
	deps   Deps
}

// newRig makes a rig with one loaded, 480 Mbit/s drive per serial, serial i on
// /dev/sr<i> at USB port 5-1.<i+1>. scale is how much faster than the wall the
// clock runs.
func newRig(t *testing.T, scale float64, serials ...string) *rig {
	t.Helper()
	root := t.TempDir()
	cfg := paths.Config{MediaRoot: root}
	if err := os.MkdirAll(cfg.RipsRoot(), 0o770); err != nil {
		t.Fatal(err)
	}
	clock := newClock(scale)
	g := &rig{
		t: t, clock: clock, sys: newFakeSys(t), drives: &fakeDrives{}, runner: newFakeRunner(clock),
		paths: cfg, store: &runstore.Store{Root: cfg.RipsRoot(), Live: aliveAlways{}, Now: clock.Now}, log: &syncBuffer{},
	}
	for i, serial := range serials {
		g.drives.list = append(g.drives.list, g.sys.add(t, serial, i, fmt.Sprintf("5-1.%d", i+1)))
	}
	g.deps = Deps{
		Paths: cfg, Drives: g.drives, Runner: g.runner, Store: g.store, Clock: clock,
		SysBlock: g.sys.blockDir(), ToolVersion: "test",
		Tick: 2 * time.Millisecond, HealthSysfsEvery: 2 * time.Millisecond, HealthDmesgEvery: 5 * time.Millisecond,
		Log: g.log,
	}
	return g
}

// testPlan is a plan of Penguindrum discs, one per serial in order, with the
// production defaults.
func testPlan(serials ...string) plan.Plan {
	p := plan.Plan{MinTitleSeconds: 60, MaxConcurrent: 3, SpinupGapSeconds: 30, HealthPauseSeconds: 120, ScanTimeoutSeconds: 900}
	for i, serial := range serials {
		season, disc := 1, i+1
		p.Discs = append(p.Discs, plan.Entry{Serial: serial, Category: "anime", Series: "Penguindrum", Season: &season, Disc: &disc})
	}
	return p
}

// running is an Execute in flight.
type running struct {
	g      *rig
	run    *Run
	cancel context.CancelFunc
	done   chan error
}

func (g *rig) newID() string {
	g.t.Helper()
	id, err := runstore.NewID(g.clock.Now(), rand.Reader)
	if err != nil {
		g.t.Fatal(err)
	}
	return id
}

func (g *rig) start(p plan.Plan) *running {
	g.t.Helper()
	run, err := NewRun(g.deps, p, g.newID())
	if err != nil {
		g.t.Fatalf("NewRun: %v", err)
	}
	return g.launch(run)
}

// launch executes run in the background.
func (g *rig) launch(run *Run) *running {
	ctx, cancel := context.WithCancel(context.Background())
	g.t.Cleanup(cancel)
	r := &running{g: g, run: run, cancel: cancel, done: make(chan error, 1)}
	go func() { r.done <- run.Execute(ctx) }()
	return r
}

// wait returns the saved terminal state once Execute has returned.
func (r *running) wait() *runstore.State {
	r.g.t.Helper()
	select {
	case err := <-r.done:
		if err != nil {
			r.g.t.Fatalf("Execute: %v", err)
		}
	case <-time.After(30 * time.Second):
		r.g.t.Fatal("Execute did not finish in 30s")
	}
	return r.state()
}

// state reads state.json as it is on disk now.
func (r *running) state() *runstore.State {
	r.g.t.Helper()
	data, err := os.ReadFile(filepath.Join(r.g.store.Dir(r.run.ID()), "state.json"))
	if err != nil {
		r.g.t.Fatal(err)
	}
	var st runstore.State
	if err := json.Unmarshal(data, &st); err != nil {
		r.g.t.Fatalf("state.json: %v", err)
	}
	return &st
}

// until polls state.json until cond holds.
func (r *running) until(what string, cond func(*runstore.State) bool) *runstore.State {
	r.g.t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if st := r.state(); cond(st) {
			return st
		}
		time.Sleep(time.Millisecond)
	}
	r.g.t.Fatalf("timed out waiting for %s", what)
	return nil
}

func (g *rig) execute(p plan.Plan) *runstore.State { g.t.Helper(); return g.start(p).wait() }

func serials(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = fmt.Sprintf("SERIAL%02d", i)
	}
	return out
}

func discState(want runstore.DiscState) func(*runstore.State) bool {
	return func(st *runstore.State) bool { return st.Discs[0].State == want }
}
