package frames

import (
	"bytes"
	"context"
	"io"
	"testing"

	"github.com/reclyptor/mediaops/kit/proc"
)

// fakePiper feeds canned stdout to consume and records every argv after
// running it through the real guard, so a refused argv fails the test.
type fakePiper struct {
	t      *testing.T
	stdout []byte
	stderr []byte
	exit   int
	argv   []string
}

func (f *fakePiper) Pipe(_ context.Context, argv []string, consume func(io.Reader) error) ([]byte, int, error) {
	f.t.Helper()
	f.argv = argv
	if err := proc.Check(argv); err != nil {
		f.t.Errorf("guard refused %q: %v", argv, err)
	}
	if err := consume(bytes.NewReader(f.stdout)); err != nil {
		return f.stderr, f.exit, err
	}
	return f.stderr, f.exit, nil
}

// fakeRunner records every argv after running it through the real guard.
type fakeRunner struct {
	t      *testing.T
	res    proc.Result
	argv   []string
	called bool
}

func (f *fakeRunner) Run(_ context.Context, argv ...string) (proc.Result, error) {
	f.t.Helper()
	f.called, f.argv = true, argv
	if err := proc.Check(argv); err != nil {
		f.t.Errorf("guard refused %q: %v", argv, err)
	}
	return f.res, nil
}

func (f *fakeRunner) Stream(context.Context, []string, func(string)) (int, error) {
	panic("not used")
}
