package health

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/reclyptor/mediaops/internal/drives"
	"github.com/reclyptor/mediaops/kit/proc"
)

// fakeDmesg answers `dmesg` from whatever the test has set.
type fakeDmesg struct {
	mu     sync.Mutex
	out    string
	stderr string
	exit   int
	err    error
	argv   [][]string
}

func (f *fakeDmesg) set(out string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.out, f.stderr, f.exit, f.err = out, "", 0, nil
}

func (f *fakeDmesg) Run(_ context.Context, argv ...string) (proc.Result, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.argv = append(f.argv, argv)
	if f.err != nil {
		return proc.Result{}, f.err
	}
	return proc.Result{Stdout: []byte(f.out), Stderr: []byte(f.stderr), ExitCode: f.exit}, nil
}

func (f *fakeDmesg) Stream(context.Context, []string, func(string)) (int, error) {
	return 0, errors.New("not used")
}

// fakeSys is a sysfs with two drives: sr0 on 5-1.2.1 and sr1 on 5-1.2.2.
type fakeSys struct {
	t        *testing.T
	root     string
	sysBlock string
}

func newFakeSys(t *testing.T) *fakeSys {
	t.Helper()
	s := &fakeSys{t: t, root: t.TempDir()}
	s.sysBlock = filepath.Join(s.root, "block")
	if err := os.Mkdir(s.sysBlock, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, d := range []struct{ sr, port string }{{"sr0", "5-1.2.1"}, {"sr1", "5-1.2.2"}} {
		s.addSR(d.sr)
		s.setSpeed(d.port, "480")
	}
	return s
}

func (s *fakeSys) usbDir(port string) string { return filepath.Join(s.root, "usb", port) }

func (s *fakeSys) addSR(sr string) {
	s.t.Helper()
	if err := os.WriteFile(filepath.Join(s.sysBlock, sr), nil, 0o644); err != nil {
		s.t.Fatal(err)
	}
}

func (s *fakeSys) removeSR(sr string) {
	s.t.Helper()
	if err := os.Remove(filepath.Join(s.sysBlock, sr)); err != nil {
		s.t.Fatal(err)
	}
}

func (s *fakeSys) setSpeed(port, speed string) {
	s.t.Helper()
	if err := os.MkdirAll(s.usbDir(port), 0o755); err != nil {
		s.t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.usbDir(port), "speed"), []byte(speed+"\n"), 0o644); err != nil {
		s.t.Fatal(err)
	}
}

func (s *fakeSys) removeUSB(port string) {
	s.t.Helper()
	if err := os.RemoveAll(s.usbDir(port)); err != nil {
		s.t.Fatal(err)
	}
}

func (s *fakeSys) known() []drives.Drive {
	mbps := 480
	return []drives.Drive{
		{Dev: "/dev/sr0", USBPort: "5-1.2.1", USBDir: s.usbDir("5-1.2.1"), LinkMbps: &mbps},
		{Dev: "/dev/sr1", USBPort: "5-1.2.2", USBDir: s.usbDir("5-1.2.2"), LinkMbps: &mbps},
	}
}

func (s *fakeSys) watch(run proc.Runner) *Watch { return New(s.known(), run, s.sysBlock) }

func TestSysfsEvents(t *testing.T) {
	sys := newFakeSys(t)
	w := sys.watch(&fakeDmesg{})

	expect := func(step string, want ...Event) {
		t.Helper()
		got := w.PollSysfs()
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("%s: events = %+v, want %+v", step, got, want)
		}
	}

	expect("a healthy tree")

	sys.removeSR("sr0")
	expect("sr0 vanishes", Event{Port: "5-1.2.1", Kind: KindSRNodeGone, Detail: "sr0 disappeared"})
	expect("sr0 still gone")
	sys.addSR("sr0")
	expect("sr0 back")
	sys.removeSR("sr0")
	expect("sr0 vanishes again", Event{Port: "5-1.2.1", Kind: KindSRNodeGone, Detail: "sr0 disappeared"})
	sys.addSR("sr0")
	expect("sr0 restored")

	sys.removeUSB("5-1.2.2")
	expect("usb device vanishes", Event{Port: "5-1.2.2", Kind: KindUSBDeviceGone, Detail: "5-1.2.2 left the bus"})
	expect("usb device still gone")
	sys.setSpeed("5-1.2.2", "480")
	expect("usb device back at 480")
	sys.removeUSB("5-1.2.2")
	expect("usb device vanishes again", Event{Port: "5-1.2.2", Kind: KindUSBDeviceGone, Detail: "5-1.2.2 left the bus"})
	sys.setSpeed("5-1.2.2", "480")
	expect("usb device restored")

	sys.setSpeed("5-1.2.1", "12")
	expect("link falls", Event{Port: "5-1.2.1", Kind: KindLinkDropped, Detail: "5-1.2.1 fell from 480 to 12 Mbit/s"})
	expect("link stays at 12")
	sys.setSpeed("5-1.2.1", "480")
	expect("link recovers")
	sys.setSpeed("5-1.2.1", "12")
	expect("link falls again", Event{Port: "5-1.2.1", Kind: KindLinkDropped, Detail: "5-1.2.1 fell from 480 to 12 Mbit/s"})
	sys.setSpeed("5-1.2.1", "480")
	expect("link recovers again")

	sys.removeSR("sr1")
	sys.setSpeed("5-1.2.2", "1.5")
	expect("two faults at once",
		Event{Port: "5-1.2.2", Kind: KindSRNodeGone, Detail: "sr1 disappeared"},
		Event{Port: "5-1.2.2", Kind: KindLinkDropped, Detail: "5-1.2.2 fell from 480 to 1 Mbit/s"})
}

func TestSysfsUnknownBaselineAndUnreadableSpeed(t *testing.T) {
	sys := newFakeSys(t)
	known := sys.known()
	known[0].LinkMbps = nil
	w := New(known, &fakeDmesg{}, sys.sysBlock)

	sys.setSpeed("5-1.2.1", "12")
	if got := w.PollSysfs(); len(got) != 0 {
		t.Fatalf("with no known baseline the first reading is the baseline, got %+v", got)
	}
	sys.setSpeed("5-1.2.1", "480")
	if got := w.PollSysfs(); len(got) != 0 {
		t.Fatalf("a rise is not an event: %+v", got)
	}

	if err := os.WriteFile(filepath.Join(sys.usbDir("5-1.2.1"), "speed"), []byte("garbage"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := w.PollSysfs(); len(got) != 0 {
		t.Fatalf("an unreadable speed is not a drop: %+v", got)
	}
	sys.setSpeed("5-1.2.1", "12")
	if got := w.PollSysfs(); len(got) != 1 || got[0].Kind != KindLinkDropped {
		t.Fatalf("the baseline must survive an unreadable poll, got %+v", got)
	}
}

const (
	lineDisconnect = "[ 100.1] usb 5-1.2.1: USB disconnect, device number 9"
	lineReset      = "[ 101.2] usb 5-1.2: reset high-speed USB device number 4 using xhci_hcd"
	lineOtherBus   = "[ 102.3] usb 3-2: USB disconnect, device number 2"
	lineBenign     = "[ 103.4] eth0: link up"
)

func TestDmesgBaseline(t *testing.T) {
	run := &fakeDmesg{}
	w := New([]drives.Drive{{Dev: "/dev/sr0", USBPort: "5-1.2.1"}}, run, t.TempDir())
	ctx := context.Background()

	run.set(lineDisconnect + "\n" + lineBenign + "\n")
	events, d := w.PollDmesg(ctx)
	if len(events) != 0 || !d.Available {
		t.Fatalf("the first read is a baseline: events %+v, dmesg %+v", events, d)
	}
	if !reflect.DeepEqual(run.argv, [][]string{{"dmesg"}}) {
		t.Errorf("argv = %v", run.argv)
	}

	events, _ = w.PollDmesg(ctx)
	if len(events) != 0 {
		t.Fatalf("an unchanged log produced %+v", events)
	}

	run.set(lineDisconnect + "\n" + lineBenign + "\n" + lineReset + "\n" + lineOtherBus + "\n" + "[ 104.5] eth0: link down\n")
	events, _ = w.PollDmesg(ctx)
	want := []Event{{Port: "5-1.2", Kind: KindDmesg, Detail: lineReset}}
	if !reflect.DeepEqual(events, want) {
		t.Fatalf("new lines: %+v, want %+v (other buses and non-health lines are not events)", events, want)
	}

	events, _ = w.PollDmesg(ctx)
	if len(events) != 0 {
		t.Fatalf("a line is news once, got %+v", events)
	}

	// The same text again, as a second occurrence, is a new message.
	run.set(lineDisconnect + "\n" + lineDisconnect + "\n" + lineReset + "\n")
	events, _ = w.PollDmesg(ctx)
	if len(events) != 1 || events[0].Port != "5-1.2.1" {
		t.Fatalf("a repeated message must count: %+v", events)
	}

	// Old lines scrolling out of the ring buffer are not events either.
	run.set(lineDisconnect + "\n" + lineDisconnect + "\n")
	if events, _ = w.PollDmesg(ctx); len(events) != 0 {
		t.Fatalf("lines rotating out produced %+v", events)
	}
}

func TestDmesgDetailIsClipped(t *testing.T) {
	run := &fakeDmesg{}
	w := New([]drives.Drive{{USBPort: "5-1.2.1"}}, run, t.TempDir())
	run.set("")
	w.PollDmesg(context.Background())
	run.set("usb 5-1.2.1: USB disconnect " + strings.Repeat("é", 400) + "\n")
	events, _ := w.PollDmesg(context.Background())
	if len(events) != 1 || len([]rune(events[0].Detail)) != 200 {
		t.Fatalf("events = %+v", events)
	}
}

func TestDmesgUnavailable(t *testing.T) {
	run := &fakeDmesg{exit: 1, stderr: "  dmesg: read kernel buffer failed: Operation not permitted\n"}
	w := New([]drives.Drive{{Dev: "/dev/sr0", USBPort: "5-1.2.1"}}, run, t.TempDir())
	ctx := context.Background()

	events, d := w.PollDmesg(ctx)
	want := Dmesg{Available: false, Detail: "dmesg: read kernel buffer failed: Operation not permitted"}
	if len(events) != 0 || d != want {
		t.Fatalf("events %+v, dmesg %+v; want no events and %+v", events, d, want)
	}

	run.stderr = strings.Repeat("x", 300)
	if _, d = w.PollDmesg(ctx); len(d.Detail) != 100 {
		t.Errorf("detail is %d bytes, want 100", len(d.Detail))
	}

	run.stderr = ""
	if _, d = w.PollDmesg(ctx); d.Available || d.Detail == "" {
		t.Errorf("an unreadable log with no stderr still needs a reason: %+v", d)
	}

	run.err = errors.New("dmesg: executable file not found")
	if events, d = w.PollDmesg(ctx); len(events) != 0 || d.Available || !strings.Contains(d.Detail, "not found") {
		t.Errorf("a failed start: %+v %+v", events, d)
	}

	// Once readable, the first good read is still the baseline, not a flood.
	run.set(lineDisconnect + "\n")
	events, d = w.PollDmesg(ctx)
	if len(events) != 0 || !d.Available || d.Detail != "" {
		t.Errorf("first readable poll: %+v %+v", events, d)
	}
}

func TestParseLine(t *testing.T) {
	tests := []struct {
		line string
		port string
		ok   bool
	}{
		{"usb 5-1.2-port3: attempt power cycle", "", false},
		{"usb 5-1.2-port3: disabled by hub (EMI?), re-enabling... error -71", "5-1.2.3", true},
		{"usb 5-1.2.1: USB disconnect, device number 9", "5-1.2.1", true},
		{"usb 5-1.2: reset high-speed USB device number 4 using xhci_hcd", "5-1.2", true},
		{"usb 5-1.4.3.3: new full-speed USB device number 12", "5-1.4.3.3", true},
		{"usb 3-2: USB disconnect, device number 2", "3-2", true},
		{"eth0: link up", "", false},
		{"usb usb5: USB disconnect, device number 1", "5", true},
		{"usb 5-1.2.1: device descriptor read/64, error -71", "5-1.2.1", true},
		{"hub 5-1.2:1.0: over-current change on port 3", "5-1.2", true},
		{"USB DISCONNECT with no port in sight", "", false},
	}
	for _, tt := range tests {
		port, ok := ParseLine(tt.line)
		if port != tt.port || ok != tt.ok {
			t.Errorf("ParseLine(%q) = %q, %v; want %q, %v", tt.line, port, ok, tt.port, tt.ok)
		}
	}
}

func TestPortCovers(t *testing.T) {
	tests := []struct {
		event, drive string
		want         bool
	}{
		{"5-1.2", "5-1.2.1", true},
		{"5-1.2.1", "5-1.2.1", true},
		{"5-1.2", "5-1.20", false},
		{"5-1.2", "5-1.3.3", false},
		{"5-1.2.1", "5-1.2", false},
		{"5", "5-1.4.3.3", true},
		{"5", "6-1", false},
		{"5", "55-1", false},
		{"3-2", "5-1.2.1", false},
	}
	for _, tt := range tests {
		if got := PortCovers(tt.event, tt.drive); got != tt.want {
			t.Errorf("PortCovers(%q, %q) = %v, want %v", tt.event, tt.drive, got, tt.want)
		}
	}
}

func recv(t *testing.T, out <-chan Update) Update {
	t.Helper()
	select {
	case u := <-out:
		return u
	case <-time.After(5 * time.Second):
		t.Fatal("no update arrived")
		return Update{}
	}
}

func TestRun(t *testing.T) {
	sys := newFakeSys(t)
	run := &fakeDmesg{}
	run.set(lineBenign + "\n")
	w := sys.watch(run)

	ctx, cancel := context.WithCancel(context.Background())
	out := make(chan Update)
	done := make(chan struct{})
	go func() {
		w.Run(ctx, 2*time.Millisecond, 2*time.Millisecond, out)
		close(done)
	}()

	if u := recv(t, out); len(u.Events) != 0 || u.Dmesg != (Dmesg{Available: true}) {
		t.Fatalf("first update must report the log is readable: %+v", u)
	}
	select {
	case u := <-out:
		t.Fatalf("an unchanged tree produced %+v", u)
	case <-time.After(40 * time.Millisecond):
	}

	sys.setSpeed("5-1.2.1", "12")
	u := recv(t, out)
	if len(u.Events) != 1 || u.Events[0].Kind != KindLinkDropped || !u.Dmesg.Available {
		t.Fatalf("link drop update: %+v", u)
	}

	run.set(lineBenign + "\n" + lineReset + "\n")
	u = recv(t, out)
	if len(u.Events) != 1 || u.Events[0].Kind != KindDmesg || u.Events[0].Port != "5-1.2" {
		t.Fatalf("dmesg update: %+v", u)
	}

	run.mu.Lock()
	run.exit, run.stderr = 1, "denied"
	run.mu.Unlock()
	if u = recv(t, out); len(u.Events) != 0 || u.Dmesg != (Dmesg{Detail: "denied"}) {
		t.Fatalf("a change of availability alone is an update: %+v", u)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after its context ended")
	}
}

func TestRunStopsWhileBlockedOnSend(t *testing.T) {
	w := newFakeSys(t).watch(&fakeDmesg{})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		w.Run(ctx, time.Hour, time.Hour, make(chan Update)) // nobody reads
		close(done)
	}()
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run stuck sending to a reader that left")
	}
}
