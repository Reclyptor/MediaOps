package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestCode(t *testing.T) {
	cases := []struct {
		err  error
		want int
	}{
		{nil, ExitOK},
		{Exit(ExitIncomplete), ExitIncomplete},
		{Usage(errors.New("bad plan")), ExitUsage},
		{Refused(errors.New("leased")), ExitRefused},
		{fmt.Errorf("wrapped: %w", Refused(errors.New("leased"))), ExitRefused},
		{errors.New("disk on fire"), ExitInternal},
	}
	for _, c := range cases {
		if got := Code(c.err); got != c.want {
			t.Errorf("Code(%v) = %d, want %d", c.err, got, c.want)
		}
	}
}

func TestReport(t *testing.T) {
	var buf bytes.Buffer
	if code := Report(&buf, "mkv-rip", Usage(errors.New("discs[2]: serial is required"))); code != ExitUsage {
		t.Fatalf("code = %d", code)
	}
	if got, want := buf.String(), "mkv-rip: FATAL: discs[2]: serial is required\n"; got != want {
		t.Fatalf("stderr = %q, want %q", got, want)
	}

	buf.Reset()
	if code := Report(&buf, "mkv-rip", Exit(ExitIncomplete)); code != ExitIncomplete || buf.Len() != 0 {
		t.Fatalf("a silent exit printed %q with code %d", buf.String(), code)
	}
}

func TestRunVersion(t *testing.T) {
	var out, errOut bytes.Buffer
	code := Run(context.Background(), "mkv-drives", []string{"--version"}, IO{Stdout: &out, Stderr: &errOut},
		func(context.Context, []string, IO) error { t.Fatal("--version must not run the command"); return nil })
	if code != ExitOK || !strings.HasPrefix(out.String(), "mkv-drives ") {
		t.Fatalf("code %d, stdout %q", code, out.String())
	}
}

func TestPrintJSON(t *testing.T) {
	var buf bytes.Buffer
	if err := PrintJSON(&buf, []string{}); err != nil {
		t.Fatal(err)
	}
	if buf.String() != "[]\n" {
		t.Fatalf("got %q", buf.String())
	}
}

func TestParseFlagsAmongPositionals(t *testing.T) {
	var run, dry bool
	fs := NewFlagSet("t")
	fs.BoolVar(&run, "go", false, "")
	fs.BoolVar(&dry, "dry-run", false, "")

	got, err := ParseFlags(fs, []string{"a.json", "--go", "b.json", "--dry-run"})
	if err != nil || !run || !dry || strings.Join(got, ",") != "a.json,b.json" {
		t.Fatalf("got %v, go=%v dry=%v, err=%v", got, run, dry, err)
	}

	if _, err := ParseFlags(NewFlagSet("t"), []string{"--nope"}); err == nil {
		t.Fatal("an unknown flag was accepted")
	}
}
