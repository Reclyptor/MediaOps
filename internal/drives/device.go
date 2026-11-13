package drives

import (
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

const (
	cdromDriveStatus = 0x5326
	cdromEject       = 0x5309

	cdsNoDisc, cdsTrayOpen, cdsDriveNotReady, cdsDiscOK = 1, 2, 3, 4
)

// Device is the ioctl seam: what the kernel knows about a drive, and the eject
// request. Neither reads a sector.
type Device interface {
	Status(dev string) (Status, error)
	Eject(dev string) error
}

type kernelDevice struct{}

// Status asks CDROM_DRIVE_STATUS on a non-blocking fd. That asks the drive what
// it already knows instead of reading the disc, so nothing spins up.
func (kernelDevice) Status(dev string) (Status, error) {
	f, err := os.OpenFile(dev, os.O_RDONLY|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return StatusUnknown, err
	}
	defer f.Close()

	// This ioctl returns the status as its result rather than through memory.
	r, _, errno := unix.Syscall(unix.SYS_IOCTL, f.Fd(), cdromDriveStatus, 0)
	if errno != 0 {
		return StatusUnknown, fmt.Errorf("%s: ioctl: %w", dev, errno)
	}
	switch r {
	case cdsNoDisc:
		return StatusNoDisc, nil
	case cdsTrayOpen:
		return StatusTrayOpen, nil
	case cdsDriveNotReady:
		return StatusNotReady, nil
	case cdsDiscOK:
		return StatusDiscOK, nil
	}
	return StatusUnknown, fmt.Errorf("%s: ioctl returned the unrecognised status %d", dev, r)
}

func (kernelDevice) Eject(dev string) error {
	f, err := os.OpenFile(dev, os.O_RDONLY|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, _, errno := unix.Syscall(unix.SYS_IOCTL, f.Fd(), cdromEject, 0); errno != 0 {
		return fmt.Errorf("%s: ioctl: %w", dev, errno)
	}
	return nil
}
