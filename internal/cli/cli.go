// Package cli is the shared shell of every mediaops command: the umask, the
// exit codes, the FATAL line and the one JSON value on stdout.
package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
)

// Version is set at build time with -ldflags -X.
var Version = "dev"

// Exit codes shared by every mediaops command.
const (
	ExitOK         = 0
	ExitIncomplete = 1
	ExitUsage      = 2
	ExitRefused    = 3
	ExitInternal   = 4
)

// Error is a failure that carries the exit code the command should end with.
// A nil Err ends the command silently with that code, for outcomes the command
// has already reported on stdout.
type Error struct {
	Code int
	Err  error
}

func (e *Error) Error() string {
	if e.Err == nil {
		return fmt.Sprintf("exit status %d", e.Code)
	}
	return e.Err.Error()
}

func (e *Error) Unwrap() error { return e.Err }

// Exit ends the command with code and no message.
func Exit(code int) error { return &Error{Code: code} }

// Usage marks err as a usage error or invalid input (exit 2).
func Usage(err error) error { return &Error{Code: ExitUsage, Err: err} }

// Refused marks err as a guard refusal (exit 3).
func Refused(err error) error { return &Error{Code: ExitRefused, Err: err} }

// Code is the exit code for err: 0 for nil, the carried code for an *Error,
// and ExitInternal for anything else.
func Code(err error) int {
	var e *Error
	switch {
	case err == nil:
		return ExitOK
	case errors.As(err, &e):
		return e.Code
	default:
		return ExitInternal
	}
}

// IO is a command's standard streams, replaceable in tests.
type IO struct {
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
}

// PrintJSON writes v as the command's one JSON value.
func PrintJSON(w io.Writer, v any) error {
	b, err := json.MarshalIndent(v, "", " ")
	if err != nil {
		return fmt.Errorf("encoding output: %w", err)
	}
	_, err = fmt.Fprintf(w, "%s\n", b)
	return err
}

// Report prints err as the tool's one-line FATAL message and returns its exit
// code. An error with no message prints nothing.
func Report(w io.Writer, tool string, err error) int {
	if err == nil {
		return ExitOK
	}
	var e *Error
	if !errors.As(err, &e) || e.Err != nil {
		fmt.Fprintf(w, "%s: FATAL: %s\n", tool, err)
	}
	return Code(err)
}

// Run is the testable body of Main: it handles --version, runs the command and
// reports its error. It returns the process exit code.
func Run(ctx context.Context, tool string, args []string, streams IO, run func(ctx context.Context, args []string, streams IO) error) int {
	if len(args) == 1 && args[0] == "--version" {
		fmt.Fprintf(streams.Stdout, "%s %s\n", tool, Version)
		return ExitOK
	}
	return Report(streams.Stderr, tool, run(ctx, args, streams))
}

// Main runs a command as a process: umask 0007, SIGINT and SIGTERM cancelling
// the context, and the exit code from Run. It does not return.
//
// The umask is how every file a tool creates reaches the library group-writable
// (0660 files, 0770 directories); kubectl exec bypasses the image's CMD, so the
// tool has to set it itself.
func Main(tool string, run func(ctx context.Context, args []string, streams IO) error) {
	syscall.Umask(0o007)
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	code := Run(ctx, tool, os.Args[1:], IO{Stdin: os.Stdin, Stdout: os.Stdout, Stderr: os.Stderr}, run)
	stop()
	os.Exit(code)
}
