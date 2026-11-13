package mkvtool

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/reclyptor/mediaops/kit/proc"
)

// fakeRunner answers by program name and records every argv.
type fakeRunner struct {
	replies map[string]proc.Result
	calls   [][]string
}

func (f *fakeRunner) Run(_ context.Context, argv ...string) (proc.Result, error) {
	f.calls = append(f.calls, argv)
	return f.replies[argv[0]], nil
}

func (f *fakeRunner) Stream(context.Context, []string, func(string)) (int, error) {
	panic("not used")
}

func identify(title string) proc.Result {
	return proc.Result{Stdout: []byte(`{"container": {"properties": {"title": "` + title + `"}}}`)}
}

func TestSetTitleArgv(t *testing.T) {
	r := &fakeRunner{replies: map[string]proc.Result{"mkvmerge": identify("Show S01 D01")}}
	if err := SetTitle(context.Background(), r, "/x/t00.mkv", "Show S01 D01"); err != nil {
		t.Fatal(err)
	}
	want := [][]string{
		{"mkvpropedit", "/x/t00.mkv", "--edit", "info", "--set", "title=Show S01 D01"},
		{"mkvmerge", "-J", "/x/t00.mkv"},
	}
	if !reflect.DeepEqual(r.calls, want) {
		t.Fatalf("calls = %q, want %q", r.calls, want)
	}
}

func TestSetTitleExitCodes(t *testing.T) {
	tests := []struct {
		name    string
		exit    int
		wantErr string
	}{
		{"clean", 0, ""},
		{"warnings accepted", 1, ""},
		{"error rejected", 2, "exit 2"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := &fakeRunner{replies: map[string]proc.Result{
				"mkvpropedit": {ExitCode: tc.exit, Stdout: []byte("Error: the file is locked")},
				"mkvmerge":    identify("T"),
			}}
			err := SetTitle(context.Background(), r, "f.mkv", "T")
			if tc.wantErr == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) || !strings.Contains(err.Error(), "file is locked") {
				t.Fatalf("err = %v, want exit and output tail", err)
			}
			if len(r.calls) != 1 {
				t.Fatalf("identify ran after a failed edit: %q", r.calls)
			}
		})
	}
}

func TestSetTitleReadBackMismatch(t *testing.T) {
	r := &fakeRunner{replies: map[string]proc.Result{"mkvmerge": identify("Something Else")}}
	err := SetTitle(context.Background(), r, "f.mkv", "Wanted")
	if err == nil || !strings.Contains(err.Error(), "reads back") {
		t.Fatalf("err = %v, want a read-back mismatch", err)
	}
}

func TestSetTitleIdentifyFailures(t *testing.T) {
	tests := map[string]proc.Result{
		"non-zero exit": {ExitCode: 2, Stdout: []byte("Error: no such file")},
		"bad JSON":      {Stdout: []byte("garbage")},
		"no title":      {Stdout: []byte(`{"container": {"properties": {}}}`)},
	}
	for name, res := range tests {
		t.Run(name, func(t *testing.T) {
			r := &fakeRunner{replies: map[string]proc.Result{"mkvmerge": res}}
			if err := SetTitle(context.Background(), r, "f.mkv", "T"); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
}

type errRunner struct{ fakeRunner }

func (e *errRunner) Run(context.Context, ...string) (proc.Result, error) {
	return proc.Result{}, errors.New("refused")
}

func TestSetTitleRunnerError(t *testing.T) {
	if err := SetTitle(context.Background(), &errRunner{}, "f.mkv", "T"); err == nil || !strings.Contains(err.Error(), "refused") {
		t.Fatalf("err = %v", err)
	}
}
