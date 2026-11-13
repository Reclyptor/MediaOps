// Package health notices the USB tree misbehaving while drives are in use.
//
// sysfs comes first: a drive's link falling out of 480 Mbit/s, its USB device
// vanishing or its sr node disappearing is exactly how the 2026-11-09 failure
// looked, and none of it needs privileges. dmesg adds the kernel's own words
// when it may be read, which it usually may not (kernel.dmesg_restrict).
package health

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/reclyptor/mediaops/internal/drives"
	"github.com/reclyptor/mediaops/kit/proc"
)

const (
	defaultSysBlock = "/sys/class/block"
	maxDmesgDetail  = 100 // of stderr, when dmesg cannot be read
	maxEventDetail  = 200 // of a dmesg line
)

// healthLine matches kernel lines that mean the USB tree is struggling. It is
// applied only to lines that name a port, so unrelated USB devices elsewhere on
// the host do not pause a rip.
var healthLine = regexp.MustCompile(`(?i)error -71|USB disconnect|\bdisconnect|reset (?:full|high|low|super)[- ]?speed|` +
	`device descriptor read|unable to enumerate|device not accepting address|` +
	`over-?current|cannot enable|new full-speed`)

var (
	// "usb 5-1.2-port3" names the child of hub 5-1.2 on port 3: device 5-1.2.3.
	hubPort = regexp.MustCompile(`(\d+-\d+(?:\.\d+)*)-port(\d+)`)
	// "usb 5-1.2.1" or "hub 5-1.2" name a device.
	device = regexp.MustCompile(`\b(?:usb|hub) (\d+-\d+(?:\.\d+)*)\b`)
	// "usb usb5" is the root hub: a whole bus.
	rootHub = regexp.MustCompile(`\busb (?:usb)?(\d+)\b`)
)

// ParseLine returns the USB port a kernel log line is about, and false when the
// line is not a health line or names no port. A bus-wide line returns the bare
// bus number.
func ParseLine(line string) (port string, ok bool) {
	if !healthLine.MatchString(line) {
		return "", false
	}
	if m := hubPort.FindStringSubmatch(line); m != nil {
		return m[1] + "." + m[2], true
	}
	if m := device.FindStringSubmatch(line); m != nil {
		return m[1], true
	}
	if m := rootHub.FindStringSubmatch(line); m != nil {
		return m[1], true
	}
	return "", false
}

// PortCovers reports whether an event on eventPort touches a drive on
// drivePort. A hub or bus event reaches everything beneath it: one reset on
// 5-1.2 hits all three drives behind it.
func PortCovers(eventPort, drivePort string) bool {
	if !strings.Contains(eventPort, "-") {
		return busOf(drivePort) == eventPort
	}
	return drivePort == eventPort || strings.HasPrefix(drivePort, eventPort+".")
}

func busOf(port string) string {
	bus, _, _ := strings.Cut(port, "-")
	return bus
}

// watched is one drive under observation.
type watched struct {
	port   string
	sr     string // the sr node's name, e.g. sr0
	usbDir string
	speed  int // last link speed seen; 0 until one is known
}

// problem is one sticky fault, reported once until it clears.
type problem struct {
	port string
	kind string
}

// Watch observes a fixed set of drives. It is not safe for concurrent use;
// Run owns it while it runs.
type Watch struct {
	run      proc.Runner
	sysBlock string
	drives   []*watched
	buses    map[string]bool

	down      map[problem]bool
	dmesgSeen map[string]int // nil until the first successful read, the baseline
}

// New watches known, reading sr nodes under sysBlock (/sys/class/block when
// empty) and the kernel log through run.
func New(known []drives.Drive, run proc.Runner, sysBlock string) *Watch {
	if sysBlock == "" {
		sysBlock = defaultSysBlock
	}
	w := &Watch{run: run, sysBlock: sysBlock, buses: map[string]bool{}, down: map[problem]bool{}}
	for _, d := range known {
		speed := 0
		if d.LinkMbps != nil {
			speed = *d.LinkMbps
		}
		w.drives = append(w.drives, &watched{
			port: d.USBPort, sr: filepath.Base(d.Dev), usbDir: d.USBDir, speed: speed,
		})
		w.buses[busOf(d.USBPort)] = true
	}
	return w
}

// PollSysfs reports what changed in sysfs since the last poll. Each fault is
// reported once when it appears and again only after it has cleared and come
// back; a link drop is reported once per fall.
func (w *Watch) PollSysfs() []Event {
	var events []Event
	for _, d := range w.drives {
		if _, err := os.Stat(d.usbDir); err != nil {
			if w.raise(d.port, KindUSBDeviceGone) {
				events = append(events, Event{Port: d.port, Kind: KindUSBDeviceGone, Detail: d.port + " left the bus"})
			}
			continue
		}
		w.clear(d.port, KindUSBDeviceGone)

		if _, err := os.Stat(filepath.Join(w.sysBlock, d.sr)); err != nil {
			if w.raise(d.port, KindSRNodeGone) {
				events = append(events, Event{Port: d.port, Kind: KindSRNodeGone, Detail: d.sr + " disappeared"})
			}
		} else {
			w.clear(d.port, KindSRNodeGone)
		}

		now, err := drives.LinkMbps(d.usbDir)
		if err != nil {
			continue // unreadable speed is not a drop
		}
		if d.speed > 0 && now < d.speed {
			events = append(events, Event{
				Port: d.port, Kind: KindLinkDropped,
				Detail: fmt.Sprintf("%s fell from %d to %d Mbit/s", d.port, d.speed, now),
			})
		}
		d.speed = now
	}
	return events
}

// raise records a fault and reports whether it is new.
func (w *Watch) raise(port, kind string) bool {
	key := problem{port, kind}
	if w.down[key] {
		return false
	}
	w.down[key] = true
	return true
}

func (w *Watch) clear(port, kind string) { delete(w.down, problem{port, kind}) }

// PollDmesg reads the kernel log. The first successful read is a baseline and
// yields no events; after that every line that was not there before, and names
// a port on a watched bus, is an event. An unreadable log is reported in the
// returned Dmesg, never as an error.
func (w *Watch) PollDmesg(ctx context.Context) ([]Event, Dmesg) {
	res, err := w.run.Run(ctx, "dmesg")
	if err != nil {
		return nil, Dmesg{Detail: clip(err.Error(), maxDmesgDetail)}
	}
	if res.ExitCode != 0 {
		detail := strings.TrimSpace(string(res.Stderr))
		if detail == "" {
			detail = fmt.Sprintf("dmesg exited %d", res.ExitCode)
		}
		return nil, Dmesg{Detail: clip(detail, maxDmesgDetail)}
	}

	current := map[string]int{}
	var fresh []string
	for _, line := range strings.Split(string(res.Stdout), "\n") {
		current[line]++
		// Lines are matched to the earlier snapshot by count, so a message that
		// repeats shows up as news only for its extra occurrences.
		if w.dmesgSeen != nil && current[line] > w.dmesgSeen[line] {
			fresh = append(fresh, line)
		}
	}
	baseline := w.dmesgSeen == nil
	w.dmesgSeen = current
	if baseline {
		return nil, Dmesg{Available: true}
	}

	var events []Event
	for _, line := range fresh {
		if port, ok := ParseLine(line); ok && w.buses[busOf(port)] {
			events = append(events, Event{Port: port, Kind: KindDmesg, Detail: clip(strings.TrimSpace(line), maxEventDetail)})
		}
	}
	return events, Dmesg{Available: true}
}

// clip cuts s to at most n runes.
func clip(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}

// Update is what Run reports: new events, and the state of the kernel log.
type Update struct {
	Events []Event
	Dmesg  Dmesg
}

// Run polls until ctx ends. It reads dmesg once up front, for the baseline and
// to learn whether the log is readable, then polls sysfs every sysfsEvery and
// dmesg every dmesgEvery. An Update is sent when there are events, and when the
// state of the log changes, which includes the first read.
func (w *Watch) Run(ctx context.Context, sysfsEvery, dmesgEvery time.Duration, out chan<- Update) {
	var last Dmesg
	send := func(u Update) bool {
		select {
		case out <- u:
			return true
		case <-ctx.Done():
			return false
		}
	}
	// poll reports whether Run should carry on.
	poll := func() bool {
		events, d := w.PollDmesg(ctx)
		if ctx.Err() != nil {
			return false
		}
		changed := d != last
		last = d
		if len(events) == 0 && !changed {
			return true
		}
		return send(Update{Events: events, Dmesg: d})
	}

	if !poll() {
		return
	}
	sysfs := time.NewTicker(sysfsEvery)
	defer sysfs.Stop()
	dmesg := time.NewTicker(dmesgEvery)
	defer dmesg.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-sysfs.C:
			if events := w.PollSysfs(); len(events) > 0 && !send(Update{Events: events, Dmesg: last}) {
				return
			}
		case <-dmesg.C:
			if !poll() {
				return
			}
		}
	}
}
