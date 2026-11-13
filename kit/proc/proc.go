// Package proc is the only place the toolset starts another program. Every
// child runs under an allowlist and a structural never-re-encode guard, and
// dies with its parent.
package proc

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"syscall"
	"time"
)

// DefaultGrace is how long a child gets between SIGTERM and SIGKILL once its
// context ends.
const DefaultGrace = 10 * time.Second

// Result is a finished child's output. ExitCode is -1 when a signal ended it.
type Result struct {
	Stdout, Stderr []byte
	ExitCode       int
}

// Runner starts allowlisted children. Callers depend on this interface so tests
// can substitute it; Exec is the real implementation.
type Runner interface {
	// Run runs argv to completion. A non-zero exit is reported in Result, not as
	// an error; the error is for a refused argv, a failed start or a cancelled
	// context.
	Run(ctx context.Context, argv ...string) (Result, error)

	// Stream runs argv with stdout and stderr merged, calling onLine for every
	// line in order from a single goroutine. Invalid UTF-8 is replaced. When ctx
	// ends the child gets SIGTERM, then SIGKILL after the grace period, and the
	// context's error is returned alongside the exit code.
	Stream(ctx context.Context, argv []string, onLine func(line string)) (exitCode int, err error)
}

// Exec is the real Runner.
type Exec struct {
	// Grace overrides DefaultGrace.
	Grace time.Duration
	// Env is appended to the parent's environment for every child.
	Env []string
	// Resolve maps an allowlisted program name to the path to execute. It
	// defaults to exec.LookPath; tests point it at a helper binary.
	Resolve func(name string) (string, error)
}

var _ Runner = (*Exec)(nil)

// Run implements Runner.
func (e *Exec) Run(ctx context.Context, argv ...string) (Result, error) {
	var stdout, stderr bytes.Buffer
	code, err := e.run(ctx, argv, &stdout, &stderr)
	return Result{Stdout: stdout.Bytes(), Stderr: stderr.Bytes(), ExitCode: code}, err
}

// Stream implements Runner.
func (e *Exec) Stream(ctx context.Context, argv []string, onLine func(string)) (int, error) {
	w := &lineWriter{onLine: onLine}
	code, err := e.run(ctx, argv, w, w)
	w.flush()
	return code, err
}

func (e *Exec) run(ctx context.Context, argv []string, stdout, stderr io.Writer) (int, error) {
	if err := Check(argv); err != nil {
		return -1, err
	}
	resolve := e.Resolve
	if resolve == nil {
		resolve = exec.LookPath
	}
	path, err := resolve(argv[0])
	if err != nil {
		return -1, fmt.Errorf("proc: locating %s: %w", argv[0], err)
	}

	cmd := exec.CommandContext(ctx, path, argv[1:]...)
	cmd.Stdout, cmd.Stderr = stdout, stderr
	cmd.Env = append(os.Environ(), e.Env...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL}
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = e.Grace
	if cmd.WaitDelay == 0 {
		cmd.WaitDelay = DefaultGrace
	}

	// Pdeathsig fires when the OS thread that forked the child exits, not when
	// the process does (golang/go#27505), so that thread stays pinned until the
	// child is reaped.
	type outcome struct {
		code int
		err  error
	}
	done := make(chan outcome, 1)
	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		if err := cmd.Start(); err != nil {
			done <- outcome{-1, fmt.Errorf("proc: starting %s: %w", argv[0], err)}
			return
		}
		waitErr := cmd.Wait()
		var exitErr *exec.ExitError
		switch {
		case waitErr == nil:
			done <- outcome{0, nil}
		case errors.As(waitErr, &exitErr):
			done <- outcome{exitErr.ExitCode(), nil}
		default:
			done <- outcome{cmd.ProcessState.ExitCode(), fmt.Errorf("proc: waiting for %s: %w", argv[0], waitErr)}
		}
	}()

	out := <-done
	if ctx.Err() != nil {
		return out.code, ctx.Err()
	}
	return out.code, out.err
}

// lineWriter splits a byte stream into lines, with invalid UTF-8 replaced.
type lineWriter struct {
	onLine func(string)
	buf    []byte
}

func (w *lineWriter) Write(p []byte) (int, error) {
	w.buf = append(w.buf, p...)
	for {
		i := bytes.IndexByte(w.buf, '\n')
		if i < 0 {
			return len(p), nil
		}
		w.emit(w.buf[:i])
		w.buf = w.buf[i+1:]
	}
}

func (w *lineWriter) flush() {
	if len(w.buf) > 0 {
		w.emit(w.buf)
		w.buf = nil
	}
}

func (w *lineWriter) emit(line []byte) {
	w.onLine(strings.ToValidUTF8(strings.TrimSuffix(string(line), "\r"), "�"))
}
