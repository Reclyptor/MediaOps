package manifest

import (
	"context"
	"errors"
	"fmt"

	"github.com/reclyptor/mediaops/internal/cli"
)

// ExecOptions are the switches of an exec.
type ExecOptions struct {
	// Go performs the ops. Without it exec is a dry run that touches nothing.
	Go bool
	// Films passes through to the check that gates the run.
	Films bool
}

// Op result statuses.
const (
	StatusPending = "pending"
	StatusDone    = "done"
	StatusSettled = "settled"
	StatusSkipped = "skipped"
	StatusFailed  = "failed"
)

// OpResult is what became of one pending op.
type OpResult struct {
	Op     int    `json:"op"`
	Kind   string `json:"kind"`
	Target string `json:"target"`
	Status string `json:"status"`
	Bytes  int64  `json:"bytes"`
	Detail string `json:"detail"`
}

// ExecReport is what exec prints. Only pending ops appear in Results. Error
// and FailedOp are set when a run stopped early; FailedOp only when an op
// itself failed.
type ExecReport struct {
	Manifest    string      `json:"manifest"`
	DryRun      bool        `json:"dry_run"`
	Check       CheckReport `json:"check"`
	Ops         int         `json:"ops"`
	AlreadyDone int         `json:"already_done"`
	Applied     int         `json:"applied"`
	Results     []OpResult  `json:"results"`
	Bytes       int64       `json:"bytes"`
	Journal     string      `json:"journal"`
	Error       string      `json:"error,omitempty"`
	FailedOp    *int        `json:"failed_op,omitempty"`
}

// journalError is a failure to write the journal. It is never an op's fault,
// and the run cannot go on without a record of what it does.
type journalError struct{ err error }

func (e *journalError) Error() string { return e.err.Error() }
func (e *journalError) Unwrap() error { return e.err }

// Exec checks m and, with opts.Go, performs its pending ops in order. A check
// with any finding is the whole result: the report carries it, nothing was
// touched and the error is exit 1. A refusal (the lock, the journal, ARCHIVE_ROOT)
// returns an error with no report worth printing. When an op fails or ctx ends,
// the report says how far the run got, the journal holds the progress, and the
// error is exit 1; a rerun resumes.
func (c Config) Exec(ctx context.Context, m *Manifest, schema []Finding, opts ExecOptions) (ExecReport, error) {
	rep := ExecReport{
		Manifest: m.Path, DryRun: !opts.Go, Ops: len(m.Ops),
		Results: []OpResult{}, Journal: JournalPath(m.Path),
	}
	if opts.Go {
		if c.Runner == nil {
			return rep, errors.New("no process runner is configured")
		}
		release, err := c.Lock(m)
		if err != nil {
			return rep, err
		}
		defer func() { _ = release() }()
	}

	j, err := ReadJournal(m)
	if err != nil {
		return rep, err
	}
	check, err := c.Check(m, schema, j, CheckOptions{Films: opts.Films})
	if err != nil {
		return rep, err
	}
	rep.Check = check
	rep.AlreadyDone = check.Done
	if !check.Clean {
		return rep, cli.Exit(cli.ExitIncomplete)
	}

	var pending []Op
	for _, op := range m.Ops {
		if j.Done(op.N) {
			continue
		}
		pending = append(pending, op)
		res := OpResult{Op: op.N, Kind: op.Kind, Target: target(op), Status: StatusPending}
		for _, d := range check.InDoubt {
			if d.Op == op.N {
				res.Detail = fmt.Sprintf("interrupted earlier; %s: %s", d.Outcome, d.Reason)
			}
		}
		rep.Results = append(rep.Results, res)
	}
	if !opts.Go {
		rep.Bytes = check.Bytes
		return rep, nil
	}
	return c.apply(ctx, m, j, pending, rep)
}

// target is the path an op is about, for the report.
func target(op Op) string {
	if op.Dst != "" {
		return op.Dst
	}
	return op.Path
}

func (c Config) apply(ctx context.Context, m *Manifest, j *Journal, pending []Op, rep ExecReport) (ExecReport, error) {
	w, err := OpenJournal(m, c)
	if err != nil {
		return rep, err
	}
	defer w.Close()
	if err := w.Start(); err != nil {
		return rep, err
	}

	x := executor{c: c, w: w}
	for i, op := range pending {
		if err := ctx.Err(); err != nil {
			rep.Error = fmt.Sprintf("stopped before op %d: %v", op.N, err)
			return rep, stopped(w, rep)
		}
		st, doubt := j.InDoubt(op.N)
		a := Assessment{Outcome: Redo}
		if doubt {
			a = Assess(op, st)
		}

		step, err := x.perform(ctx, op, a, st)
		if err != nil {
			var je *journalError
			if errors.As(err, &je) {
				return rep, je
			}
			err = fmt.Errorf("op %d (%s): %w", op.N, op.Kind, err)
			rep.Results[i].Status = StatusFailed
			rep.Results[i].Detail = err.Error()
			rep.Error, rep.FailedOp = err.Error(), &op.N
			if werr := w.Failed(op.N, err); werr != nil {
				return rep, werr
			}
			return rep, &cli.Error{Code: cli.ExitIncomplete, Err: err}
		}
		rep.Results[i] = OpResult{Op: op.N, Kind: op.Kind, Target: target(op), Status: step.status, Bytes: step.bytes, Detail: step.detail}
		rep.Applied++
		rep.Bytes += step.bytes
	}
	if err := w.Finish(rep.Applied); err != nil {
		return rep, err
	}
	return rep, nil
}

// stopped ends a run cancelled between ops: the journal says how far it got.
func stopped(w *JournalWriter, rep ExecReport) error {
	if err := w.Finish(rep.Applied); err != nil {
		return err
	}
	return &cli.Error{Code: cli.ExitIncomplete, Err: errors.New(rep.Error)}
}
