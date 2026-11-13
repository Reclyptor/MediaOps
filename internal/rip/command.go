package rip

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"slices"
	"time"

	"golang.org/x/sys/unix"

	"github.com/reclyptor/mediaops/internal/cli"
	"github.com/reclyptor/mediaops/internal/lease"
	"github.com/reclyptor/mediaops/internal/makemkv"
	"github.com/reclyptor/mediaops/internal/plan"
	"github.com/reclyptor/mediaops/internal/runstore"
)

const usage = "usage: mkv-rip start PLAN.json|- | identify [SERIAL...] [--min-title-seconds N] [--spinup-gap-seconds N] [--scan-timeout-seconds N] | " +
	"status [RUN_ID] | wait RUN_ID | list | finalize RUN_ID [--dry-run] | stop RUN_ID | --version"

// runRef is what start, identify and finalize print: the run to follow.
type runRef struct {
	RunID string `json:"run_id"`
}

// Command is the mkv-rip body: args are what follows the program name.
func Command(ctx context.Context, d Deps, args []string, streams cli.IO) error {
	if len(args) == 0 {
		return cli.Usage(errors.New(usage))
	}
	d = d.withDefaults()
	name, rest := args[0], args[1:]
	if name == controllerArg {
		return d.runController(ctx)
	}
	run, ok := map[string]func(context.Context, []string, cli.IO) error{
		"start":    d.cmdStart,
		"identify": d.cmdIdentify,
		"status":   d.cmdStatus,
		"wait":     d.cmdWait,
		"list":     d.cmdList,
		"finalize": d.cmdFinalize,
		"stop":     d.cmdStop,
	}[name]
	if !ok {
		return cli.Usage(fmt.Errorf("unknown command %q; %s", name, usage))
	}
	if err := d.Paths.CheckStaging(); err != nil {
		return cli.Refused(err)
	}
	return run(ctx, rest, streams)
}

func (d Deps) cmdStart(ctx context.Context, args []string, streams cli.IO) error {
	if len(args) != 1 {
		return cli.Usage(errors.New("usage: mkv-rip start PLAN.json|-"))
	}
	var data []byte
	var err error
	if args[0] == "-" {
		data, err = io.ReadAll(streams.Stdin)
	} else {
		data, err = os.ReadFile(args[0])
	}
	if err != nil {
		return cli.Usage(fmt.Errorf("cannot read the plan %s: %w", args[0], err))
	}
	p, err := plan.Parse(data, d.Paths.Staging())
	if err != nil {
		return cli.Usage(fmt.Errorf("bad plan %s: %w", args[0], err))
	}
	return d.launch(ctx, streams, request{Kind: kindRip, Plan: &p})
}

func (d Deps) cmdIdentify(ctx context.Context, args []string, streams cli.IO) error {
	req := identifyRequest{}
	fs := newFlagSet("identify")
	fs.IntVar(&req.MinTitleSeconds, "min-title-seconds", 60, "")
	fs.IntVar(&req.SpinupGapSeconds, "spinup-gap-seconds", 30, "")
	fs.IntVar(&req.ScanTimeoutSeconds, "scan-timeout-seconds", 900, "")
	serials, err := parseFlags(fs, args)
	if err != nil {
		return cli.Usage(fmt.Errorf("identify: %w; usage: mkv-rip identify [SERIAL...] [--min-title-seconds N] [--spinup-gap-seconds N] [--scan-timeout-seconds N]", err))
	}
	switch {
	case req.MinTitleSeconds < 1:
		return cli.Usage(errors.New("identify: --min-title-seconds must be at least 1"))
	case req.SpinupGapSeconds < 0:
		return cli.Usage(errors.New("identify: --spinup-gap-seconds must not be negative"))
	case req.ScanTimeoutSeconds < 1:
		return cli.Usage(errors.New("identify: --scan-timeout-seconds must be at least 1"))
	}
	for i, serial := range serials {
		if slices.Contains(serials[:i], serial) {
			return cli.Usage(fmt.Errorf("identify: serial %s is given twice", serial))
		}
	}
	req.Serials = serials
	return d.launch(ctx, streams, request{Kind: kindIdentify, Identify: &req})
}

func (d Deps) cmdFinalize(ctx context.Context, args []string, streams cli.IO) error {
	var dryRun bool
	fs := newFlagSet("finalize")
	fs.BoolVar(&dryRun, "dry-run", false, "")
	ids, err := parseFlags(fs, args)
	if err != nil || len(ids) != 1 {
		return cli.Usage(errors.New("usage: mkv-rip finalize RUN_ID [--dry-run]"))
	}
	_, report, err := d.planFinalize(ctx, ids[0])
	if err != nil {
		return err
	}
	if len(report.WouldFinalize) == 0 {
		if err := cli.PrintJSON(streams.Stdout, report); err != nil {
			return err
		}
		return cli.Exit(cli.ExitIncomplete)
	}
	if dryRun {
		return cli.PrintJSON(streams.Stdout, report)
	}
	return d.launch(ctx, streams, request{Kind: kindFinalize, RunID: ids[0]})
}

func (d Deps) cmdStatus(_ context.Context, args []string, streams cli.IO) error {
	if len(args) > 1 {
		return cli.Usage(errors.New("usage: mkv-rip status [RUN_ID]"))
	}
	var id string
	if len(args) == 1 {
		id = args[0]
	} else {
		var err error
		if id, err = d.defaultRun(); err != nil {
			return err
		}
	}
	st, err := d.loadRun(id)
	if err != nil {
		return err
	}
	return cli.PrintJSON(streams.Stdout, st)
}

// defaultRun is the run holding the drives, else the newest.
func (d Deps) defaultRun() (string, error) {
	held, err := lease.Probe(d.LeasePath)
	if err != nil {
		return "", err
	}
	if held.Held && held.Holder.RunID != "" {
		return held.Holder.RunID, nil
	}
	runs, err := d.Store.List()
	if err != nil {
		return "", err
	}
	if len(runs) == 0 {
		return "", cli.Usage(errors.New("there are no runs"))
	}
	return runs[0].RunID, nil
}

func (d Deps) cmdWait(ctx context.Context, args []string, streams cli.IO) error {
	if len(args) != 1 {
		return cli.Usage(errors.New("usage: mkv-rip wait RUN_ID"))
	}
	for {
		st, err := d.loadRun(args[0])
		if err != nil {
			return err
		}
		if st.State.Terminal() {
			if err := cli.PrintJSON(streams.Stdout, st); err != nil {
				return err
			}
			if st.State != runstore.RunComplete {
				return cli.Exit(cli.ExitIncomplete)
			}
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("waiting for run %s: %w", args[0], ctx.Err())
		case <-time.After(d.PollEvery):
		}
	}
}

func (d Deps) cmdList(_ context.Context, args []string, streams cli.IO) error {
	if len(args) != 0 {
		return cli.Usage(errors.New("usage: mkv-rip list"))
	}
	runs, err := d.Store.List()
	if err != nil {
		return err
	}
	return cli.PrintJSON(streams.Stdout, runs)
}

func (d Deps) cmdStop(_ context.Context, args []string, streams cli.IO) error {
	if len(args) != 1 {
		return cli.Usage(errors.New("usage: mkv-rip stop RUN_ID"))
	}
	id := args[0]
	st, err := d.loadRun(id)
	if err != nil {
		return err
	}
	held, err := lease.Probe(d.LeasePath)
	if err != nil {
		return err
	}
	if !held.Held || held.Holder.RunID != id {
		return cli.Refused(fmt.Errorf("run %s does not hold the drives (it is %s), so there is nothing to stop", id, st.State))
	}
	if err := unix.Kill(held.Holder.PID, unix.SIGTERM); err != nil {
		return fmt.Errorf("signalling the controller of run %s (pid %d): %w", id, held.Holder.PID, err)
	}
	return cli.PrintJSON(streams.Stdout, struct {
		RunID    string `json:"run_id"`
		Stopping bool   `json:"stopping"`
	}{id, true})
}

// launch is the foreground half of start, identify and finalize: everything
// that can be refused is refused here, before a controller exists.
func (d Deps) launch(ctx context.Context, streams cli.IO, req request) error {
	settings, err := makemkv.SettingsFromEnv()
	if err == nil {
		err = settings.Write()
	}
	if err != nil {
		return cli.Refused(err)
	}
	if err := d.Paths.EnsureRipsRoot(); err != nil {
		return cli.Refused(err)
	}
	// The controller's own acquire is the authority; this only spares a wait for
	// the answer.
	held, err := lease.Probe(d.LeasePath)
	if err != nil {
		return err
	}
	if held.Held {
		return cli.Refused(&lease.HeldError{Holder: held.Holder})
	}
	id, err := d.detach(ctx, req)
	if err != nil {
		return err
	}
	return cli.PrintJSON(streams.Stdout, runRef{RunID: id})
}

// loadRun is Store.Load with an unknown run as invalid input.
func (d Deps) loadRun(id string) (*runstore.State, error) {
	st, err := d.Store.Load(id)
	if errors.Is(err, runstore.ErrNoRun) {
		return nil, cli.Usage(err)
	}
	return st, err
}

func newFlagSet(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	return fs
}

// parseFlags parses args with flags allowed among the positional arguments, as
// `finalize RUN_ID --dry-run` and `identify SERIAL --spinup-gap-seconds 0` are
// written, and returns the positional ones.
func parseFlags(fs *flag.FlagSet, args []string) ([]string, error) {
	var positional []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		args = fs.Args()
		if len(args) == 0 {
			return positional, nil
		}
		positional, args = append(positional, args[0]), args[1:]
	}
}
