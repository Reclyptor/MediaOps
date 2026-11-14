package proc

import (
	"context"
	"io"
	"sync/atomic"
	"syscall"
)

// stderrTailMax bounds the stderr Pipe keeps: a child that logs a line per
// frame must not grow the parent without limit.
const stderrTailMax = 1 << 20

// Piper streams a child's stdout to consume instead of buffering it. *Exec
// implements it.
type Piper interface {
	// Pipe runs argv (through Check, Pdeathsig, LockOSThread exactly as Run
	// does) and calls consume with the child's stdout. stderr is collected,
	// keeping at most the last 1 MiB. If consume returns an error the child is
	// killed (SIGKILL) and that error is returned. If consume returns nil
	// before EOF, the rest of stdout is drained. A non-zero exit is reported in
	// exit, not as err.
	Pipe(ctx context.Context, argv []string, consume func(stdout io.Reader) error) (stderr []byte, exit int, err error)
}

var _ Piper = (*Exec)(nil)

// Pipe implements Piper.
func (e *Exec) Pipe(ctx context.Context, argv []string, consume func(io.Reader) error) ([]byte, int, error) {
	// The command lives on a derived context so a consumer failure can kill it
	// outright, while ctx stays the caller's for reporting its own error.
	cmdCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	cmd, err := e.command(cmdCtx, argv)
	if err != nil {
		return nil, -1, err
	}
	var kill atomic.Bool
	graceful := cmd.Cancel
	cmd.Cancel = func() error {
		if kill.Load() {
			return cmd.Process.Signal(syscall.SIGKILL)
		}
		return graceful()
	}

	pr, pw := io.Pipe()
	stderr := &tailBuffer{max: stderrTailMax}
	cmd.Stdout, cmd.Stderr = pw, stderr

	type outcome struct {
		code int
		err  error
	}
	done := make(chan outcome, 1)
	go func() {
		code, err := wait(ctx, cmd, argv[0])
		pw.Close()
		done <- outcome{code, err}
	}()

	cerr := consume(pr)
	if cerr != nil {
		kill.Store(true)
		cancel()
		pr.CloseWithError(cerr)
	} else {
		io.Copy(io.Discard, pr)
	}
	out := <-done

	switch {
	case ctx.Err() != nil:
		return stderr.bytes(), out.code, ctx.Err()
	case cerr != nil:
		return stderr.bytes(), out.code, cerr
	}
	return stderr.bytes(), out.code, out.err
}

// tailBuffer keeps the last max bytes written to it.
type tailBuffer struct {
	max int
	buf []byte
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.buf = append(t.buf, p...)
	if len(t.buf) > 2*t.max {
		t.buf = append(t.buf[:0], t.buf[len(t.buf)-t.max:]...)
	}
	return len(p), nil
}

func (t *tailBuffer) bytes() []byte {
	if len(t.buf) > t.max {
		return t.buf[len(t.buf)-t.max:]
	}
	return t.buf
}
