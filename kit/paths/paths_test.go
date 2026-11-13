package paths

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// devs maps "/" and everything else to different devices unless same is set.
func devs(same bool) func(string) (uint64, error) {
	return func(path string) (uint64, error) {
		if path == "/" || same {
			return 1, nil
		}
		return 2, nil
	}
}

func TestStagingGuard(t *testing.T) {
	mk := func(t *testing.T) Config {
		return Config{MediaRoot: t.TempDir(), StatDev: devs(false)}
	}
	mkStaging := func(t *testing.T, c Config, mode os.FileMode) {
		t.Helper()
		if err := os.MkdirAll(c.Staging(), 0o770); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(c.Staging(), mode); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("missing", func(t *testing.T) {
		c := mk(t)
		err := c.CheckStaging()
		if err == nil || !strings.Contains(err.Error(), "does not exist") {
			t.Fatalf("err = %v, want does-not-exist", err)
		}
		if _, statErr := os.Stat(c.Staging()); !os.IsNotExist(statErr) {
			t.Fatalf("the guard created the staging tree: %v", statErr)
		}
	})

	t.Run("not a directory", func(t *testing.T) {
		c := mk(t)
		if err := os.MkdirAll(filepath.Dir(c.Staging()), 0o770); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(c.Staging(), nil, 0o660); err != nil {
			t.Fatal(err)
		}
		err := c.CheckStaging()
		if err == nil || !strings.Contains(err.Error(), "not a directory") {
			t.Fatalf("err = %v, want not-a-directory", err)
		}
	})

	t.Run("same device as root", func(t *testing.T) {
		c := Config{MediaRoot: t.TempDir(), StatDev: devs(true)}
		mkStaging(t, c, 0o770)
		err := c.CheckStaging()
		if err == nil || !strings.Contains(err.Error(), "root filesystem") {
			t.Fatalf("err = %v, want root-filesystem", err)
		}
	})

	t.Run("unwritable", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root bypasses permission bits")
		}
		c := mk(t)
		mkStaging(t, c, 0o500)
		err := c.CheckStaging()
		if err == nil || !strings.Contains(err.Error(), "not writable") {
			t.Fatalf("err = %v, want not-writable", err)
		}
	})

	t.Run("healthy", func(t *testing.T) {
		c := mk(t)
		mkStaging(t, c, 0o770)
		if err := c.CheckStaging(); err != nil {
			t.Fatalf("healthy staging refused: %v", err)
		}
	})
}

func TestEnsureRipsRoot(t *testing.T) {
	c := Config{MediaRoot: t.TempDir(), StatDev: devs(false)}
	if err := c.EnsureRipsRoot(); err == nil {
		t.Fatal("EnsureRipsRoot created state without a staging tree")
	}
	if _, err := os.Stat(c.Staging()); !os.IsNotExist(err) {
		t.Fatalf("staging tree was created: %v", err)
	}

	if err := os.MkdirAll(c.Staging(), 0o770); err != nil {
		t.Fatal(err)
	}
	if err := c.EnsureRipsRoot(); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(c.RipsRoot()); err != nil || !info.IsDir() {
		t.Fatalf("rips root missing: %v", err)
	}
}

func TestLayoutAndEnv(t *testing.T) {
	c := Config{MediaRoot: "/m"}
	if c.Staging() != "/m/dxp6800/_pending" || c.RipsRoot() != "/m/dxp6800/_pending/.rips" {
		t.Fatalf("layout wrong: %s %s", c.Staging(), c.RipsRoot())
	}
	t.Setenv("MEDIA_ROOT", "")
	if got := FromEnv().MediaRoot; got != DefaultMediaRoot {
		t.Fatalf("default = %q", got)
	}
	t.Setenv("MEDIA_ROOT", "/elsewhere")
	if got := FromEnv().MediaRoot; got != "/elsewhere" {
		t.Fatalf("env = %q", got)
	}
}
