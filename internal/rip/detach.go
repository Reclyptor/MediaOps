package rip

import (
	"cmp"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"time"

	"golang.org/x/sys/unix"

	"github.com/reclyptor/mediaops/internal/cli"
	"github.com/reclyptor/mediaops/internal/lease"
	"github.com/reclyptor/mediaops/internal/plan"
	"github.com/reclyptor/mediaops/internal/runstore"
)

// controllerArg is the hidden subcommand a detached controller runs as.
const controllerArg = "__controller"

// File descriptors a detached controller is born with, after stdin, stdout and
// stderr: the request to run, and the pipe it reports its start on.
const (
	requestFD   = 3
	handshakeFD = 4
)

// What a controller was started to do. They are the kinds the lease names.
const (
	kindRip      = "rip"
	kindIdentify = "identify"
	kindFinalize = "finalize"
)

// request is what the foreground hands the controller on fd 3.
type request struct {
	Kind     string           `json:"kind"`
	Plan     *plan.Plan       `json:"plan,omitempty"`     // rip
	Identify *identifyRequest `json:"identify,omitempty"` // identify
	RunID    string           `json:"run_id,omitempty"`   // finalize: the run to reopen
}

// handshake is the controller's one report to the foreground, on fd 4.
type handshake struct {
	OK    bool   `json:"ok"`
	RunID string `json:"run_id,omitempty"`
	Exit  int    `json:"exit,omitempty"`
	Error string `json:"error,omitempty"`
}

// detach starts a controller for req in a session of its own and returns the id
// of the run it began, or why it refused. The controller carries on whatever
// becomes of this process, including when it never answers.
func (d Deps) detach(ctx context.Context, req request) (string, error) {
	if d.Exe == "" {
		return "", errors.New("rip: Deps.Exe is required to detach a controller")
	}
	body, err := json.Marshal(req)
	if err != nil {
		return "", fmt.Errorf("encoding the request: %w", err)
	}
	reqR, reqW, err := os.Pipe()
	if err != nil {
		return "", fmt.Errorf("creating the request pipe: %w", err)
	}
	hsR, hsW, err := os.Pipe()
	if err != nil {
		reqR.Close()
		reqW.Close()
		return "", fmt.Errorf("creating the handshake pipe: %w", err)
	}
	defer hsR.Close()

	// Stdin, stdout and stderr stay nil, which is /dev/null.
	cmd := exec.Command(d.Exe, controllerArg)
	cmd.ExtraFiles = []*os.File{reqR, hsW}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	err = cmd.Start()
	reqR.Close()
	hsW.Close()
	if err != nil {
		reqW.Close()
		return "", fmt.Errorf("starting the controller: %w", err)
	}
	go func() { _ = cmd.Wait() }() // reaps it if it dies before this process exits
	go func() {
		// A write that fails means the controller already died, and the handshake
		// says so.
		_, _ = reqW.Write(body)
		_ = reqW.Close()
	}()

	type answer struct {
		hs  handshake
		err error
	}
	got := make(chan answer, 1)
	go func() {
		var hs handshake
		err := json.NewDecoder(hsR).Decode(&hs)
		got <- answer{hs, err}
	}()
	timeout := time.NewTimer(d.HandshakeTimeout)
	defer timeout.Stop()
	select {
	case a := <-got:
		switch {
		case a.err != nil:
			return "", fmt.Errorf("the controller exited without reporting: %w", a.err)
		case !a.hs.OK:
			return "", &cli.Error{Code: cmp.Or(a.hs.Exit, cli.ExitInternal), Err: errors.New(a.hs.Error)}
		}
		return a.hs.RunID, nil
	case <-timeout.C:
		return "", fmt.Errorf("the controller did not report within %s; it may still be starting, so check mkv-rip list before starting another run", d.HandshakeTimeout)
	case <-ctx.Done():
		return "", fmt.Errorf("waiting for the controller: %w", ctx.Err())
	}
}

// runController is the detached side: it takes the lease, creates the run,
// reports to the foreground, and drives the run to its verdict. The lease is
// held until the terminal state is on disk.
func (d Deps) runController(ctx context.Context) error {
	signal.Ignore(syscall.SIGHUP)
	hs := os.NewFile(handshakeFD, "handshake")
	run, held, err := d.takeOver(ctx)
	if err != nil {
		_ = reply(hs, handshake{Exit: cli.Code(err), Error: err.Error()}) // nobody to tell if the foreground is gone
		return err
	}
	defer held.Release()

	logFile, err := openControllerLog(d.Store.Dir(run.ID()))
	if err == nil {
		// Go's runtime writes its panics to fd 2, and they belong in the log.
		err = errors.Join(unix.Dup3(int(logFile.Fd()), 1, 0), unix.Dup3(int(logFile.Fd()), 2, 0))
	}
	if err != nil {
		_ = reply(hs, handshake{Exit: cli.ExitInternal, Error: err.Error()})
		return err
	}
	run.deps.Log = logFile
	if err := reply(hs, handshake{OK: true, RunID: run.ID()}); err != nil {
		run.logf("could not report to the foreground: %v", err)
	}
	return run.Execute(ctx)
}

// takeOver reads the request, acquires the lease and only then creates the run,
// so a refused start leaves nothing behind.
func (d Deps) takeOver(ctx context.Context) (*Run, *lease.Lease, error) {
	req, err := readRequest(os.NewFile(requestFD, "request"))
	if err != nil {
		return nil, nil, err
	}
	id := req.RunID
	if req.Kind != kindFinalize {
		if id, err = runstore.NewID(d.Clock.Now(), rand.Reader); err != nil {
			return nil, nil, err
		}
	}
	holder := lease.Holder{Kind: req.Kind, RunID: id, PID: os.Getpid(), StartedAt: stamp(d.Clock.Now())}
	l, err := lease.Acquire(ctx, d.LeasePath, holder, d.LeaseWait)
	var heldBy *lease.HeldError
	if errors.As(err, &heldBy) {
		return nil, nil, cli.Refused(err)
	}
	if err != nil {
		return nil, nil, err
	}
	run, err := d.newControlledRun(ctx, req, id)
	if err != nil {
		l.Release()
		return nil, nil, err
	}
	return run, l, nil
}

func (d Deps) newControlledRun(ctx context.Context, req request, id string) (*Run, error) {
	switch {
	case req.Kind == kindRip && req.Plan != nil:
		return NewRun(d, *req.Plan, id)
	case req.Kind == kindIdentify && req.Identify != nil:
		return newIdentifyRun(ctx, d, *req.Identify, id)
	case req.Kind == kindFinalize && req.RunID != "":
		return newFinalizeRun(ctx, d, id)
	}
	return nil, fmt.Errorf("malformed controller request for kind %q", req.Kind)
}

func readRequest(f *os.File) (request, error) {
	defer f.Close()
	var req request
	if err := json.NewDecoder(f).Decode(&req); err != nil {
		return request{}, fmt.Errorf("reading the controller request: %w", err)
	}
	return req, nil
}

func reply(f *os.File, h handshake) error {
	return errors.Join(json.NewEncoder(f).Encode(h), f.Close())
}
