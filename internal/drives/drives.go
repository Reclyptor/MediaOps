// Package drives finds the optical drives and reports what each holds without
// spinning a disc. Everything here is read-only against the hardware except
// Eject.
package drives

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/reclyptor/mediaops/internal/cli"
	"github.com/reclyptor/mediaops/kit/proc"
)

// Status is the raw state of a drive. not_ready is a drive still becoming
// ready, which "media: false" alone would misreport as empty.
type Status string

const (
	StatusNoDisc   Status = "no_disc"
	StatusTrayOpen Status = "tray_open"
	StatusNotReady Status = "not_ready"
	StatusDiscOK   Status = "disc_ok"
	StatusUnknown  Status = "unknown"
)

// Drive is one optical drive. Error is null when the state was read.
type Drive struct {
	Serial   string  `json:"serial"`
	Dev      string  `json:"dev"`
	SG       *string `json:"sg"`
	USBPort  string  `json:"usb_port"`
	LinkMbps *int    `json:"link_mbps"`
	Status   Status  `json:"status"`
	Tray     *string `json:"tray"`
	Media    *bool   `json:"media"`
	Error    *string `json:"error"`

	USBDir string `json:"-"` // the drive's USB device directory in sysfs
}

// Loaded reports whether the drive is known to hold a disc.
func (d Drive) Loaded() bool { return d.Media != nil && *d.Media }

// Host is the machine's drives: the sysfs tree, the ioctl seam and the process
// runner for the sg_* fallbacks.
type Host struct {
	SysBlock    string // /sys/class/block
	Dev         Device
	Run         proc.Runner
	TrayTimeout time.Duration // how long Eject waits to see the tray open
	TrayPoll    time.Duration
}

// NewHost is the real machine.
func NewHost(run proc.Runner) *Host {
	return &Host{
		SysBlock:    "/sys/class/block",
		Dev:         kernelDevice{},
		Run:         run,
		TrayTimeout: 5 * time.Second,
		TrayPoll:    500 * time.Millisecond,
	}
}

var srName = regexp.MustCompile(`^sr(\d+)$`)

// List returns every optical drive in sr order. A drive with no USB ancestor is
// not one of ours and is skipped.
func (h *Host) List(ctx context.Context) ([]Drive, error) {
	entries, err := os.ReadDir(h.SysBlock)
	if err != nil {
		return nil, fmt.Errorf("listing block devices: %w", err)
	}
	type sr struct {
		name string
		n    int
	}
	var srs []sr
	for _, e := range entries {
		if m := srName.FindStringSubmatch(e.Name()); m != nil {
			n, _ := strconv.Atoi(m[1])
			srs = append(srs, sr{e.Name(), n})
		}
	}
	sort.Slice(srs, func(i, j int) bool { return srs[i].n < srs[j].n })

	var out []Drive
	for _, s := range srs {
		usb, err := h.usbDir(s.name)
		if err != nil {
			return nil, err
		}
		if usb == "" {
			continue
		}
		d, err := h.describe(ctx, s.name, usb)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, nil
}

// usbDir finds the USB device directory behind the block device: the nearest
// ancestor holding both `serial` and `speed`. Interface directories (5-1.2.1:1.0)
// have neither, and hubs further up have both but are not the drive, so the
// first match walking up is the drive itself. "" means there is none.
func (h *Host) usbDir(sr string) (string, error) {
	dir, err := filepath.EvalSymlinks(filepath.Join(h.SysBlock, sr, "device"))
	if err != nil {
		return "", fmt.Errorf("resolving %s: %w", sr, err)
	}
	// /sys is the grandparent of /sys/class/block, wherever the tree is rooted.
	stop, err := filepath.EvalSymlinks(filepath.Dir(filepath.Dir(h.SysBlock)))
	if err != nil {
		return "", fmt.Errorf("resolving the sysfs root: %w", err)
	}
	for dir != stop && dir != "/" && dir != "." {
		if isFile(filepath.Join(dir, "serial")) && isFile(filepath.Join(dir, "speed")) {
			return dir, nil
		}
		dir = filepath.Dir(dir)
	}
	return "", nil
}

func isFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

func (h *Host) describe(ctx context.Context, sr, usb string) (Drive, error) {
	serial, err := os.ReadFile(filepath.Join(usb, "serial"))
	if err != nil {
		return Drive{}, fmt.Errorf("reading the serial of %s: %w", sr, err)
	}
	d := Drive{
		Serial:  strings.TrimSpace(string(serial)),
		Dev:     "/dev/" + sr,
		USBPort: filepath.Base(usb),
		USBDir:  usb,
	}
	if sgs, err := os.ReadDir(filepath.Join(h.SysBlock, sr, "device", "scsi_generic")); err == nil && len(sgs) > 0 {
		sg := "/dev/" + sgs[0].Name()
		d.SG = &sg
	}
	if mbps, err := LinkMbps(usb); err == nil {
		d.LinkMbps = &mbps
	}

	status, statusErr := h.status(ctx, d)
	d.Status = status
	if status != StatusUnknown {
		tray := "closed"
		if status == StatusTrayOpen {
			tray = "open"
		}
		media := status == StatusDiscOK
		d.Tray, d.Media = &tray, &media
	}
	if statusErr != nil {
		msg := statusErr.Error()
		d.Error = &msg
	}
	return d, nil
}

// LinkMbps reads the negotiated USB speed. The drives belong at 480; 12 means
// the port fell back to USB 1.1 and needs the hub reset. sysfs writes 1.5 for
// low speed, which truncates to 1.
func LinkMbps(usbDir string) (int, error) {
	raw, err := os.ReadFile(filepath.Join(usbDir, "speed"))
	if err != nil {
		return 0, err
	}
	speed, err := strconv.ParseFloat(strings.TrimSpace(string(raw)), 64)
	if err != nil {
		return 0, fmt.Errorf("parsing %s: %w", filepath.Join(usbDir, "speed"), err)
	}
	return int(speed), nil
}

// status is the ioctl first, since it needs only the sr node. When that fails
// (an unprivileged pod sees sysfs but no /dev entries), TEST UNIT READY on the
// sg node says the same thing in sense text. When neither works the state is
// unknown and the reason is returned: a drive wrongly reported empty would be
// skipped silently.
func (h *Host) status(ctx context.Context, d Drive) (Status, error) {
	status, ioctlErr := h.Dev.Status(d.Dev)
	if ioctlErr == nil {
		return status, nil
	}
	reasons := []string{ioctlErr.Error()}
	if d.SG != nil {
		status, reason := h.statusFromSG(ctx, *d.SG)
		if reason == "" {
			return status, nil
		}
		reasons = append(reasons, reason)
	}
	return StatusUnknown, errors.New(strings.Join(reasons, "; "))
}

func (h *Host) statusFromSG(ctx context.Context, sg string) (Status, string) {
	res, err := h.Run.Run(ctx, "sg_turs", sg)
	if err != nil {
		return StatusUnknown, fmt.Sprintf("sg_turs: %v", err)
	}
	if res.ExitCode == 0 {
		return StatusDiscOK, ""
	}
	text := strings.ToLower(string(res.Stdout) + string(res.Stderr))
	switch {
	case strings.Contains(text, "tray open"):
		return StatusTrayOpen, ""
	case strings.Contains(text, "medium not present"):
		return StatusNoDisc, ""
	case strings.Contains(text, "becoming ready"), strings.Contains(text, "not ready"):
		return StatusNotReady, ""
	}
	return StatusUnknown, fmt.Sprintf("sg_turs: exit %d with output it could not read: %s", res.ExitCode, clip(text))
}

// Eject opens the tray and proves it opened. makemkvcon leaves PREVENT MEDIUM
// REMOVAL set on the drive, and with that lock on CDROMEJECT still returns
// success while the drive ignores it, so a return code proves nothing: the tray
// state read back is what counts. The lock is released first, and
// `sg_start --eject` is the fallback when the ioctl leaves the tray closed.
func (h *Host) Eject(ctx context.Context, d Drive) error {
	var steps []string
	if d.SG != nil {
		if msg := h.runStep(ctx, "sg_prevent", "--allow", *d.SG); msg != "" {
			steps = append(steps, msg)
		}
	}
	if err := h.Dev.Eject(d.Dev); err != nil {
		steps = append(steps, fmt.Sprintf("CDROMEJECT: %v", err))
	}
	opened, err := h.trayOpened(ctx, d)
	if err != nil {
		return err
	}
	if opened {
		return nil
	}
	steps = append(steps, "CDROMEJECT left the tray closed")

	if d.SG != nil {
		if msg := h.runStep(ctx, "sg_start", "--eject", *d.SG); msg != "" {
			steps = append(steps, msg)
		}
		if opened, err = h.trayOpened(ctx, d); err != nil {
			return err
		}
		if opened {
			return nil
		}
		steps = append(steps, "sg_start --eject left the tray closed")
	}
	return fmt.Errorf("could not open the tray of %s: %s", d.Dev, strings.Join(steps, "; "))
}

// runStep runs an sg_* helper and describes its failure, or returns "".
func (h *Host) runStep(ctx context.Context, argv ...string) string {
	res, err := h.Run.Run(ctx, argv...)
	switch {
	case err != nil:
		return fmt.Sprintf("%s: %v", argv[0], err)
	case res.ExitCode != 0:
		return fmt.Sprintf("%s: exit %d: %s", argv[0], res.ExitCode, clip(string(res.Stderr)+string(res.Stdout)))
	}
	return ""
}

// trayOpened is true once the drive reports its tray open (or empty) within
// TrayTimeout.
func (h *Host) trayOpened(ctx context.Context, d Drive) (bool, error) {
	deadline := time.Now().Add(h.TrayTimeout)
	for {
		status, _ := h.status(ctx, d)
		if status == StatusTrayOpen || status == StatusNoDisc {
			return true, nil
		}
		if !time.Now().Before(deadline) {
			return false, nil
		}
		select {
		case <-ctx.Done():
			return false, fmt.Errorf("waiting for the tray of %s: %w", d.Dev, ctx.Err())
		case <-time.After(h.TrayPoll):
		}
	}
}

func clip(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 120 {
		return s[:120]
	}
	return s
}

// Command is the body of mkv-drives: it prints every drive as one JSON array.
func Command(ctx context.Context, h *Host, args []string, streams cli.IO) error {
	if len(args) > 0 {
		return cli.Usage(errors.New("usage: mkv-drives [--version]"))
	}
	found, err := h.List(ctx)
	if err != nil {
		return err
	}
	if len(found) == 0 {
		fmt.Fprintln(streams.Stderr, "no optical drives found under "+h.SysBlock)
		found = []Drive{}
	}
	return cli.PrintJSON(streams.Stdout, found)
}
