// Package lease is the drive lease: whoever may spin an optical drive holds an
// exclusive flock on one file for its whole life. The kernel drops the lock the
// moment its holder dies, so the lock is the only liveness signal. The holder's
// description inside the file exists to name it in a refusal and is never
// trusted as proof that anyone is alive.
package lease

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"

	"golang.org/x/sys/unix"
)

// DefaultPath lives in the pod's /tmp on purpose: every process that can touch
// the drives shares the pod, and a pod roll leaves an empty /tmp.
const DefaultPath = "/tmp/mkv-rip.lease"

const retryEvery = 100 * time.Millisecond

// Holder describes the process holding the lease.
type Holder struct {
	Kind      string    `json:"kind"` // rip, finalize or identify
	RunID     string    `json:"run_id"`
	PID       int       `json:"pid"`
	StartedAt time.Time `json:"started_at"`
}

// HeldError reports that another process holds the lease.
type HeldError struct{ Holder Holder }

func (e *HeldError) Error() string {
	if e.Holder == (Holder{}) {
		return "the drives are held by an unknown process"
	}
	return fmt.Sprintf("the drives are held by a %s (run %s, pid %d)", e.Holder.Kind, e.Holder.RunID, e.Holder.PID)
}

// Lease is a held drive lease.
type Lease struct{ f *os.File }

// Acquire takes the exclusive lease at path, retrying every 100 ms for up to
// wait, then writes h into the file. A lease still held after wait is a
// *HeldError naming its holder.
func Acquire(ctx context.Context, path string, h Holder, wait time.Duration) (*Lease, error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|unix.O_CLOEXEC, 0o660)
	if err != nil {
		return nil, fmt.Errorf("opening the drive lease: %w", err)
	}
	deadline := time.Now().Add(wait)
	for {
		err := flock(f, unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			break
		}
		if !errors.Is(err, unix.EWOULDBLOCK) {
			f.Close()
			return nil, fmt.Errorf("locking the drive lease: %w", err)
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			held := &HeldError{Holder: readHolder(f)}
			f.Close()
			return nil, held
		}
		select {
		case <-ctx.Done():
			f.Close()
			return nil, fmt.Errorf("waiting for the drive lease: %w", ctx.Err())
		case <-time.After(min(retryEvery, remaining)):
		}
	}

	if err := writeHolder(f, h); err != nil {
		f.Close()
		return nil, err
	}
	return &Lease{f: f}, nil
}

// Release gives the lease up. Releasing twice is harmless.
func (l *Lease) Release() error {
	if l == nil || l.f == nil {
		return nil
	}
	f := l.f
	l.f = nil
	if err := f.Close(); err != nil {
		return fmt.Errorf("releasing the drive lease: %w", err)
	}
	return nil
}

// Status is what Probe found.
type Status struct {
	Held   bool
	Holder Holder // zero when the content was unreadable
}

// Probe reports whether anyone holds the lease. It takes a shared lock and
// drops it at once, so it neither blocks nor disturbs a holder. A missing file
// means nobody ever held it. Content that cannot be read from a held lease (a
// holder mid-write, or a damaged file) gives Held with a zero Holder.
func Probe(path string) (Status, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|unix.O_CLOEXEC, 0)
	if errors.Is(err, os.ErrNotExist) {
		return Status{}, nil
	}
	if err != nil {
		return Status{}, fmt.Errorf("opening the drive lease: %w", err)
	}
	defer f.Close()

	err = flock(f, unix.LOCK_SH|unix.LOCK_NB)
	switch {
	case err == nil:
		return Status{}, nil
	case errors.Is(err, unix.EWOULDBLOCK):
		return Status{Held: true, Holder: readHolder(f)}, nil
	default:
		return Status{}, fmt.Errorf("probing the drive lease: %w", err)
	}
}

func flock(f *os.File, how int) error {
	for {
		err := unix.Flock(int(f.Fd()), how)
		if !errors.Is(err, unix.EINTR) {
			return err
		}
	}
}

func readHolder(f *os.File) Holder {
	var h Holder
	buf := make([]byte, 4096)
	n, _ := f.ReadAt(buf, 0) // a short read ends in io.EOF; the bytes read are all that matter
	if json.Unmarshal(buf[:n], &h) != nil {
		return Holder{}
	}
	return h
}

func writeHolder(f *os.File, h Holder) error {
	b, err := json.Marshal(h)
	if err != nil {
		return fmt.Errorf("encoding the drive lease holder: %w", err)
	}
	if err := f.Truncate(0); err != nil {
		return fmt.Errorf("writing the drive lease holder: %w", err)
	}
	if _, err := f.WriteAt(append(b, '\n'), 0); err != nil {
		return fmt.Errorf("writing the drive lease holder: %w", err)
	}
	return nil
}
