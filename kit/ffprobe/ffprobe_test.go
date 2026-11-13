package ffprobe

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/reclyptor/mediaops/kit/proc"
)

type fakeRunner struct {
	res  proc.Result
	err  error
	argv []string
}

func (f *fakeRunner) Run(_ context.Context, argv ...string) (proc.Result, error) {
	f.argv = argv
	return f.res, f.err
}

func (f *fakeRunner) Stream(context.Context, []string, func(string)) (int, error) {
	panic("not used")
}

func TestDuration(t *testing.T) {
	ok := func(out string) proc.Result { return proc.Result{Stdout: []byte(out)} }
	tests := []struct {
		name    string
		res     proc.Result
		want    float64
		wantErr string
	}{
		{"normal", ok(`{"format": {"duration": "1432.768000"}}`), 1432.768, ""},
		{"zero", ok(`{"format": {"duration": "0.000000"}}`), 0, "unusable"},
		{"negative", ok(`{"format": {"duration": "-3.5"}}`), 0, "unusable"},
		{"nan", ok(`{"format": {"duration": "NaN"}}`), 0, "unusable"},
		{"inf", ok(`{"format": {"duration": "Inf"}}`), 0, "unusable"},
		{"missing format", ok(`{}`), 0, "no duration"},
		{"empty format", ok(`{"format": {}}`), 0, "no duration"},
		{"N/A", ok(`{"format": {"duration": "N/A"}}`), 0, "not a number"},
		{"non-numeric", ok(`{"format": {"duration": "abc"}}`), 0, "not a number"},
		{"numeric JSON type", ok(`{"format": {"duration": 12}}`), 0, "unreadable JSON"},
		{"bad JSON", ok(`not json`), 0, "unreadable JSON"},
		{"empty output", ok(``), 0, "unreadable JSON"},
		{"non-zero exit", proc.Result{ExitCode: 1, Stderr: []byte("moov atom not found\n")}, 0, "moov atom not found"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Duration(context.Background(), &fakeRunner{res: tc.res}, "/x/a.mkv")
			if tc.wantErr == "" {
				if err != nil || got != tc.want {
					t.Fatalf("got %v, %v; want %v", got, err, tc.want)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %v, want it to contain %q", err, tc.wantErr)
			}
			if got != 0 {
				t.Fatalf("duration %v returned alongside an error", got)
			}
		})
	}
}

func TestDurationArgv(t *testing.T) {
	r := &fakeRunner{res: proc.Result{Stdout: []byte(`{"format":{"duration":"1"}}`)}}
	if _, err := Duration(context.Background(), r, "/x/a.mkv"); err != nil {
		t.Fatal(err)
	}
	want := []string{"ffprobe", "-v", "error", "-show_entries", "format=duration", "-of", "json", "/x/a.mkv"}
	if !reflect.DeepEqual(r.argv, want) {
		t.Fatalf("argv = %q, want %q", r.argv, want)
	}
}

func TestDurationRunnerError(t *testing.T) {
	boom := errors.New("boom")
	_, err := Duration(context.Background(), &fakeRunner{err: boom}, "/x/a.mkv")
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want it to wrap the runner error", err)
	}
}
