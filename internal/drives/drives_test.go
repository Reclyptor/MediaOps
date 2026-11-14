package drives

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/reclyptor/mediaops/internal/cli"
	"github.com/reclyptor/mediaops/kit/proc"
)

type fakeDevice struct {
	status    Status
	statusErr error
	ejectErr  error
	onEject   func()
	reads     int
}

func (f *fakeDevice) Status(string) (Status, error) {
	f.reads++
	return f.status, f.statusErr
}

func (f *fakeDevice) Eject(string) error {
	if f.onEject != nil {
		f.onEject()
	}
	return f.ejectErr
}

// fakeRunner answers by program name and records every argv.
type fakeRunner struct {
	calls  []string
	result func(argv []string) (proc.Result, error)
}

func (f *fakeRunner) Run(_ context.Context, argv ...string) (proc.Result, error) {
	f.calls = append(f.calls, strings.Join(argv, " "))
	if f.result == nil {
		return proc.Result{}, nil
	}
	return f.result(argv)
}

func (f *fakeRunner) Stream(context.Context, []string, func(string)) (int, error) {
	return 0, errors.New("unexpected Stream")
}

func newTestHost(dev Device, run proc.Runner, sysBlock string) *Host {
	return &Host{SysBlock: sysBlock, Dev: dev, Run: run, TrayTimeout: 20 * time.Millisecond, TrayPoll: time.Millisecond}
}

// sysTree builds a sysfs shaped like styxeon's: block devices are symlinks into
// a device tree where the drive's USB device sits several levels above its SCSI
// device, with a hub above it and an interface directory between.
type sysTree struct {
	t        *testing.T
	root     string
	sysBlock string
}

func newSysTree(t *testing.T) *sysTree {
	t.Helper()
	root := t.TempDir()
	s := &sysTree{t: t, root: root, sysBlock: filepath.Join(root, "sys", "class", "block")}
	s.mkdir(s.sysBlock)
	// Files at the sysfs root must never be taken for a drive's USB device.
	s.write(filepath.Join(root, "sys"), "serial", "ROOT")
	s.write(filepath.Join(root, "sys"), "speed", "480")
	return s
}

func (s *sysTree) mkdir(path string) {
	s.t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		s.t.Fatal(err)
	}
}

func (s *sysTree) write(dir, name, content string) {
	s.t.Helper()
	s.mkdir(dir)
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content+"\n"), 0o644); err != nil {
		s.t.Fatal(err)
	}
}

// addDrive adds /sys/class/block/<sr>. port "" leaves the drive with no USB
// ancestor; sg "" leaves it without a scsi_generic node.
func (s *sysTree) addDrive(sr, port, serial, speed, sg string) {
	s.t.Helper()
	dev := filepath.Join(s.root, "sys", "devices", "pci0000:00", "ata1", "host9")
	if port != "" {
		hub := filepath.Join(s.root, "sys", "devices", "pci0000:00", "usb5", "5-1")
		s.write(hub, "serial", "HUB-"+sr)
		s.write(hub, "speed", "480")
		usb := filepath.Join(hub, "5-1.2", port)
		s.write(usb, "serial", serial)
		s.write(usb, "speed", speed)
		dev = filepath.Join(usb, port+":1.0", "host4", "target4:0:0", "4:0:0:0-"+sr)
	}
	s.mkdir(dev)
	if sg != "" {
		s.mkdir(filepath.Join(dev, "scsi_generic", sg))
	}
	blockDir := filepath.Join(dev, "block", sr)
	s.mkdir(blockDir)
	if err := os.Symlink(dev, filepath.Join(blockDir, "device")); err != nil {
		s.t.Fatal(err)
	}
	if err := os.Symlink(blockDir, filepath.Join(s.sysBlock, sr)); err != nil {
		s.t.Fatal(err)
	}
}

func TestEnumerate(t *testing.T) {
	s := newSysTree(t)
	s.addDrive("sr10", "5-1.2.7", "SERIAL10", "480", "sg9")
	s.addDrive("sr2", "5-1.2.2", "SERIAL2", "12", "sg1")
	s.addDrive("sr0", "5-1.2.4", "SERIAL0", "480", "sg0")
	s.addDrive("sr3", "5-1.2.3", "SERIAL3", "1.5", "")
	s.addDrive("sr4", "", "", "", "sg7")
	s.mkdir(filepath.Join(s.sysBlock, "sda"))

	dev := &fakeDevice{status: StatusDiscOK}
	h := newTestHost(dev, &fakeRunner{}, s.sysBlock)
	got, err := h.List(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	var order []string
	for _, d := range got {
		order = append(order, d.Dev)
	}
	if want := "/dev/sr0 /dev/sr2 /dev/sr3 /dev/sr10"; strings.Join(order, " ") != want {
		t.Fatalf("drives = %s, want %s (numeric order; sr4 has no USB ancestor and sda is not optical)", strings.Join(order, " "), want)
	}

	cases := []struct {
		serial, port string
		sg           *string
		mbps         int
	}{
		{"SERIAL0", "5-1.2.4", new("/dev/sg0"), 480},
		{"SERIAL2", "5-1.2.2", new("/dev/sg1"), 12},
		{"SERIAL3", "5-1.2.3", nil, 1},
		{"SERIAL10", "5-1.2.7", new("/dev/sg9"), 480},
	}
	for i, c := range cases {
		d := got[i]
		if d.Serial != c.serial {
			t.Errorf("%s serial = %q, want %q (not the hub's, not the sysfs root's)", d.Dev, d.Serial, c.serial)
		}
		if d.USBPort != c.port {
			t.Errorf("%s usb_port = %q, want %q (not the interface directory)", d.Dev, d.USBPort, c.port)
		}
		if (d.SG == nil) != (c.sg == nil) || (c.sg != nil && *d.SG != *c.sg) {
			t.Errorf("%s sg = %v, want %v", d.Dev, deref(d.SG), deref(c.sg))
		}
		if d.LinkMbps == nil || *d.LinkMbps != c.mbps {
			t.Errorf("%s link_mbps = %v, want %d", d.Dev, d.LinkMbps, c.mbps)
		}
		if !strings.HasSuffix(d.USBDir, c.port) {
			t.Errorf("%s USBDir = %q, want it to end in %s", d.Dev, d.USBDir, c.port)
		}
		if d.Status != StatusDiscOK || d.Error != nil || !d.Loaded() {
			t.Errorf("%s = status %s, error %v, loaded %v", d.Dev, d.Status, deref(d.Error), d.Loaded())
		}
		if d.Tray == nil || *d.Tray != "closed" {
			t.Errorf("%s tray = %v, want closed", d.Dev, deref(d.Tray))
		}
	}
}

func deref(p *string) string {
	if p == nil {
		return "<nil>"
	}
	return *p
}

func TestLinkMbps(t *testing.T) {
	for _, c := range []struct {
		speed string
		want  int
	}{{"480", 480}, {"12", 12}, {"1.5", 1}, {"5000", 5000}} {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "speed"), []byte(c.speed+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		got, err := LinkMbps(dir)
		if err != nil || got != c.want {
			t.Errorf("LinkMbps(%q) = %d, %v; want %d", c.speed, got, err, c.want)
		}
	}
	dir := t.TempDir()
	if _, err := LinkMbps(dir); err == nil {
		t.Error("LinkMbps without a speed file succeeded")
	}
	os.WriteFile(filepath.Join(dir, "speed"), []byte("fast\n"), 0o644)
	if _, err := LinkMbps(dir); err == nil {
		t.Error("LinkMbps of a non-numeric speed succeeded")
	}
}

func TestStatusFromIoctl(t *testing.T) {
	cases := []struct {
		status Status
		tray   string
		media  bool
	}{
		{StatusNoDisc, "closed", false},
		{StatusTrayOpen, "open", false},
		{StatusNotReady, "closed", false},
		{StatusDiscOK, "closed", true},
	}
	for _, c := range cases {
		s := newSysTree(t)
		s.addDrive("sr0", "5-1.2.4", "S", "480", "sg0")
		run := &fakeRunner{}
		got, err := newTestHost(&fakeDevice{status: c.status}, run, s.sysBlock).List(t.Context())
		if err != nil || len(got) != 1 {
			t.Fatalf("%s: List = %v, %v", c.status, got, err)
		}
		d := got[0]
		if d.Status != c.status || d.Error != nil || *d.Tray != c.tray || *d.Media != c.media {
			t.Errorf("%s: got status %s tray %s media %v error %v", c.status, d.Status, deref(d.Tray), *d.Media, deref(d.Error))
		}
		if len(run.calls) != 0 {
			t.Errorf("%s: the ioctl answered but %v ran", c.status, run.calls)
		}
	}
}

func TestStatusFallback(t *testing.T) {
	ioctlErr := errors.New("/dev/sr0: no such file or directory")
	cases := []struct {
		name   string
		sg     string
		turs   func() (proc.Result, error)
		want   Status
		tray   *string
		media  *bool
		errHas []string
	}{
		{"exit 0 is a disc", "sg0", func() (proc.Result, error) { return proc.Result{}, nil }, StatusDiscOK, new("closed"), new(true), nil},
		{"tray open", "sg0", turs(2, "sg_turs: Sense key: Not Ready, tray open"), StatusTrayOpen, new("open"), new(false), nil},
		{"medium not present", "sg0", turs(2, "Sense key: Not Ready\n Medium not present - tray closed"), StatusNoDisc, new("closed"), new(false), nil},
		{"becoming ready", "sg0", turs(2, "Logical unit is in process of becoming ready"), StatusNotReady, new("closed"), new(false), nil},
		{"not ready", "sg0", turs(2, "Sense key: Not Ready"), StatusNotReady, new("closed"), new(false), nil},
		{"unreadable output", "sg0", turs(1, "something nobody planned for"), StatusUnknown, nil, nil, []string{ioctlErr.Error(), "sg_turs"}},
		{"sg_turs cannot start", "sg0", func() (proc.Result, error) { return proc.Result{}, errors.New("not found") }, StatusUnknown, nil, nil, []string{ioctlErr.Error(), "not found"}},
		{"no sg node", "", nil, StatusUnknown, nil, nil, []string{ioctlErr.Error()}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := newSysTree(t)
			s.addDrive("sr0", "5-1.2.4", "S", "480", c.sg)
			run := &fakeRunner{result: func(argv []string) (proc.Result, error) {
				if argv[0] != "sg_turs" || argv[1] != "/dev/sg0" {
					t.Errorf("unexpected command %v", argv)
				}
				return c.turs()
			}}
			h := newTestHost(&fakeDevice{statusErr: ioctlErr}, run, s.sysBlock)
			got, err := h.List(t.Context())
			if err != nil || len(got) != 1 {
				t.Fatalf("List = %v, %v", got, err)
			}
			d := got[0]
			if d.Status != c.want {
				t.Errorf("status = %s, want %s", d.Status, c.want)
			}
			if !samePtr(d.Tray, c.tray) || !sameBool(d.Media, c.media) {
				t.Errorf("tray %v media %v, want tray %v media %v", deref(d.Tray), d.Media, deref(c.tray), c.media)
			}
			if c.want == StatusUnknown {
				if d.Error == nil {
					t.Fatal("an unknown state carries no error")
				}
				for _, part := range c.errHas {
					if !strings.Contains(*d.Error, part) {
						t.Errorf("error %q lacks %q", *d.Error, part)
					}
				}
			} else if d.Error != nil {
				t.Errorf("a state that was read carries error %q", *d.Error)
			}
		})
	}
}

func turs(exit int, text string) func() (proc.Result, error) {
	return func() (proc.Result, error) { return proc.Result{ExitCode: exit, Stderr: []byte(text)}, nil }
}

func samePtr(a, b *string) bool { return (a == nil) == (b == nil) && (a == nil || *a == *b) }
func sameBool(a, b *bool) bool  { return (a == nil) == (b == nil) && (a == nil || *a == *b) }

func TestEjectVerifiesTray(t *testing.T) {
	sg := "/dev/sg0"
	drive := Drive{Dev: "/dev/sr0", SG: &sg}
	exit := func(code int, text string) proc.Result {
		return proc.Result{ExitCode: code, Stderr: []byte(text)}
	}

	t.Run("ioctl opens the tray and it is read back", func(t *testing.T) {
		dev := &fakeDevice{status: StatusDiscOK}
		dev.onEject = func() { dev.status = StatusTrayOpen }
		run := &fakeRunner{}
		if err := newTestHost(dev, run, "").Eject(t.Context(), drive); err != nil {
			t.Fatal(err)
		}
		if got := strings.Join(run.calls, "|"); got != "sg_prevent --allow /dev/sg0" {
			t.Errorf("commands = %q, want only the PREVENT release", got)
		}
		if dev.reads == 0 {
			t.Error("the tray was never read back")
		}
	})

	t.Run("a lying ioctl falls back to sg_start and is read back", func(t *testing.T) {
		dev := &fakeDevice{status: StatusDiscOK} // CDROMEJECT returns nil, tray stays closed
		run := &fakeRunner{result: func(argv []string) (proc.Result, error) {
			if argv[0] == "sg_start" {
				dev.status = StatusTrayOpen
			}
			return proc.Result{}, nil
		}}
		if err := newTestHost(dev, run, "").Eject(t.Context(), drive); err != nil {
			t.Fatal(err)
		}
		if got := strings.Join(run.calls, "|"); got != "sg_prevent --allow /dev/sg0|sg_start --eject /dev/sg0" {
			t.Errorf("commands = %q; sg_prevent --allow must come first, then sg_start --eject", got)
		}
	})

	t.Run("an empty drive after the eject counts as open", func(t *testing.T) {
		dev := &fakeDevice{status: StatusDiscOK}
		dev.onEject = func() { dev.status = StatusNoDisc }
		if err := newTestHost(dev, &fakeRunner{}, "").Eject(t.Context(), drive); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("both routes failing names every step", func(t *testing.T) {
		dev := &fakeDevice{status: StatusDiscOK, ejectErr: errors.New("operation not permitted")}
		run := &fakeRunner{result: func(argv []string) (proc.Result, error) {
			return exit(1, argv[0]+" refused"), nil
		}}
		err := newTestHost(dev, run, "").Eject(t.Context(), drive)
		if err == nil {
			t.Fatal("Eject succeeded with the tray closed")
		}
		for _, want := range []string{"sg_prevent", "CDROMEJECT: ", "operation not permitted", "CDROMEJECT left the tray closed", "sg_start", "sg_start --eject left the tray closed"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error %q does not name %q", err, want)
			}
		}
		if strings.Contains(err.Error(), "\n") {
			t.Errorf("error is not one line: %q", err)
		}
	})

	t.Run("a drive with no sg node only has the ioctl", func(t *testing.T) {
		dev := &fakeDevice{status: StatusDiscOK}
		run := &fakeRunner{}
		err := newTestHost(dev, run, "").Eject(t.Context(), Drive{Dev: "/dev/sr0"})
		if err == nil || !strings.Contains(err.Error(), "CDROMEJECT left the tray closed") {
			t.Errorf("error = %v", err)
		}
		if len(run.calls) != 0 {
			t.Errorf("ran %v with no sg node", run.calls)
		}
	})

	t.Run("cancellation while waiting for the tray", func(t *testing.T) {
		dev := &fakeDevice{status: StatusDiscOK}
		h := newTestHost(dev, &fakeRunner{}, "")
		h.TrayTimeout = time.Minute
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		if err := h.Eject(ctx, drive); !errors.Is(err, context.Canceled) {
			t.Errorf("Eject = %v, want context.Canceled", err)
		}
	})
}

// asRipper makes the test run in the ripper pod, where the disc tools may run.
func asRipper(t *testing.T) { t.Setenv(cli.RoleEnv, cli.RoleRip) }

func TestCommandRoleGuard(t *testing.T) {
	for _, role := range []struct {
		name  string
		set   bool
		value string
	}{{"unset", false, ""}, {"empty", true, ""}, {"library", true, "library"}, {"rip with a suffix", true, "ripper"}} {
		t.Run(role.name, func(t *testing.T) {
			t.Setenv(cli.RoleEnv, role.value) // registers the restore
			if !role.set {
				os.Unsetenv(cli.RoleEnv)
			}
			s := newSysTree(t)
			s.addDrive("sr0", "5-1.2.4", "SERIAL0", "480", "sg0")
			dev := &fakeDevice{}
			runner := &fakeRunner{}
			var out, errOut bytes.Buffer
			for _, args := range [][]string{nil, {"--labels"}} {
				err := Command(t.Context(), newTestHost(dev, runner, s.sysBlock), args, cli.IO{Stdout: &out, Stderr: &errOut})
				if cli.Code(err) != cli.ExitRefused || !strings.Contains(err.Error(), "kubectl -n mediaops exec deploy/mediaops-rip -- mkv-drives") {
					t.Errorf("Command(%v) = %v (exit %d), want a refusal naming the ripper", args, err, cli.Code(err))
				}
			}
			if out.Len() != 0 || errOut.Len() != 0 || dev.reads != 0 || len(runner.calls) != 0 {
				t.Errorf("a refusal touched the drives: stdout %q, stderr %q, %d status reads, calls %v", out.String(), errOut.String(), dev.reads, runner.calls)
			}
		})
	}
}

func TestCommand(t *testing.T) {
	asRipper(t)
	t.Run("JSON array with error always present", func(t *testing.T) {
		s := newSysTree(t)
		s.addDrive("sr0", "5-1.2.4", "SERIAL0", "480", "sg0")
		s.addDrive("sr1", "5-1.2.3", "SERIAL1", "12", "")
		dev := &fakeDevice{statusErr: errors.New("no node")}
		var out, errOut bytes.Buffer
		err := Command(t.Context(), newTestHost(dev, &fakeRunner{result: func([]string) (proc.Result, error) {
			return proc.Result{ExitCode: 2, Stderr: []byte("Medium not present")}, nil
		}}, s.sysBlock), nil, cli.IO{Stdout: &out, Stderr: &errOut})
		if err != nil {
			t.Fatal(err)
		}
		var got []map[string]any
		if err := json.Unmarshal(out.Bytes(), &got); err != nil {
			t.Fatalf("stdout is not one JSON array: %v\n%s", err, out.String())
		}
		if len(got) != 2 {
			t.Fatalf("%d drives, want 2", len(got))
		}
		for _, d := range got {
			for _, key := range []string{"serial", "dev", "sg", "usb_port", "link_mbps", "status", "tray", "media", "error"} {
				if _, ok := d[key]; !ok {
					t.Errorf("%v lacks %q", d["dev"], key)
				}
			}
			if _, ok := d["usb_dir"]; ok {
				t.Error("usb_dir leaked into the output")
			}
		}
		if got[0]["status"] != "no_disc" || got[0]["error"] != nil || got[0]["sg"] != "/dev/sg0" || got[0]["link_mbps"] != 480.0 {
			t.Errorf("sr0 = %v", got[0])
		}
		if got[1]["status"] != "unknown" || got[1]["error"] == nil || got[1]["tray"] != nil || got[1]["media"] != nil || got[1]["sg"] != nil {
			t.Errorf("sr1 = %v", got[1])
		}
		if errOut.Len() != 0 {
			t.Errorf("stderr = %q", errOut.String())
		}
	})

	t.Run("no drives prints [] and a note", func(t *testing.T) {
		s := newSysTree(t)
		var out, errOut bytes.Buffer
		err := Command(t.Context(), newTestHost(&fakeDevice{}, &fakeRunner{}, s.sysBlock), nil, cli.IO{Stdout: &out, Stderr: &errOut})
		if err != nil {
			t.Fatal(err)
		}
		if strings.TrimSpace(out.String()) != "[]" {
			t.Errorf("stdout = %q, want []", out.String())
		}
		if !strings.Contains(errOut.String(), "no optical drives") {
			t.Errorf("stderr = %q, want a note", errOut.String())
		}
	})

	t.Run("any argument is a usage error", func(t *testing.T) {
		for _, args := range [][]string{{"--labels"}, {"x"}} {
			var out bytes.Buffer
			err := Command(t.Context(), newTestHost(&fakeDevice{}, &fakeRunner{}, t.TempDir()), args, cli.IO{Stdout: &out, Stderr: &out})
			if cli.Code(err) != cli.ExitUsage {
				t.Errorf("Command(%v) = %v (exit %d), want a usage error", args, err, cli.Code(err))
			}
			if out.Len() != 0 {
				t.Errorf("Command(%v) wrote %q", args, out.String())
			}
		}
	})

	t.Run("an unreadable sysfs is an internal error", func(t *testing.T) {
		err := Command(t.Context(), newTestHost(&fakeDevice{}, &fakeRunner{}, filepath.Join(t.TempDir(), "missing")), nil, cli.IO{Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}})
		if err == nil || cli.Code(err) != cli.ExitInternal {
			t.Errorf("Command = %v (exit %d)", err, cli.Code(err))
		}
	})
}
