package cli

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"
)

func TestRequireRipRole(t *testing.T) {
	cases := []struct {
		name  string
		set   bool
		value string
		want  int
	}{
		{"rip", true, "rip", ExitOK},
		{"unset", false, "", ExitRefused},
		{"empty", true, "", ExitRefused},
		{"library", true, "library", ExitRefused},
		{"not case folded", true, "RIP", ExitRefused},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv(RoleEnv, c.value) // registers the restore
			if !c.set {
				os.Unsetenv(RoleEnv)
			}
			err := RequireRipRole("mkv-drives")
			if got := Code(err); got != c.want {
				t.Fatalf("exit %d (%v), want %d", got, err, c.want)
			}
			if c.want == ExitRefused && !strings.Contains(err.Error(), "kubectl -n mediaops exec deploy/mediaops-rip -- mkv-drives") {
				t.Errorf("the refusal does not name the ripper: %v", err)
			}
		})
	}
}

func TestVersionNeedsNoRole(t *testing.T) {
	t.Setenv(RoleEnv, "")
	var out, errOut bytes.Buffer
	code := Run(context.Background(), "mkv-rip", []string{"--version"}, IO{Stdout: &out, Stderr: &errOut},
		func(context.Context, []string, IO) error { return RequireRipRole("mkv-rip") })
	if code != ExitOK || !strings.HasPrefix(out.String(), "mkv-rip ") || errOut.Len() != 0 {
		t.Fatalf("code %d, stdout %q, stderr %q", code, out.String(), errOut.String())
	}
}
