package manifest

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/reclyptor/mediaops/internal/cli"
)

func lkConfig(t *testing.T) Config {
	t.Helper()
	return Config{LockDir: t.TempDir()}
}

func TestLockExcludesSecondHolder(t *testing.T) {
	c := lkConfig(t)
	m := &Manifest{Path: "/data/a.json"}
	release, err := c.Lock(m)
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.Lock(m)
	jrRefused(t, err, lockPath(c, m), "/data/a.json", "another exec --go")
	if cli.Code(err) != cli.ExitRefused {
		t.Errorf("code = %d", cli.Code(err))
	}

	if err := release(); err != nil {
		t.Fatal(err)
	}
	again, err := c.Lock(m)
	if err != nil {
		t.Fatalf("lock after release: %v", err)
	}
	if err := again(); err != nil {
		t.Fatal(err)
	}
}

func TestLockReleaseTwice(t *testing.T) {
	c := lkConfig(t)
	release, err := c.Lock(&Manifest{Path: "/data/a.json"})
	if err != nil {
		t.Fatal(err)
	}
	if err := release(); err != nil {
		t.Fatal(err)
	}
	if err := release(); err != nil {
		t.Errorf("second release = %v, want nil", err)
	}
}

func TestLockIndependentManifests(t *testing.T) {
	c := lkConfig(t)
	r1, err := c.Lock(&Manifest{Path: "/data/a.json"})
	if err != nil {
		t.Fatal(err)
	}
	defer r1()
	r2, err := c.Lock(&Manifest{Path: "/data/b.json"})
	if err != nil {
		t.Fatalf("second manifest: %v", err)
	}
	defer r2()
}

func TestLockUncreatableRefused(t *testing.T) {
	c := Config{LockDir: filepath.Join(t.TempDir(), "missing")}
	_, err := c.Lock(&Manifest{Path: "/data/a.json"})
	jrRefused(t, err, "cannot create the lock file")
}

func TestLockPathShape(t *testing.T) {
	c := Config{LockDir: "/locks"}
	a := lockPath(c, &Manifest{Path: "/data/a.json"})
	if !regexp.MustCompile(`^/locks/mkv-manifest-[0-9a-f]{16}\.lock$`).MatchString(a) {
		t.Errorf("lock path %q has the wrong shape", a)
	}
	if again := lockPath(c, &Manifest{Path: "/data/a.json"}); again != a {
		t.Errorf("not deterministic: %q vs %q", a, again)
	}
	if b := lockPath(c, &Manifest{Path: "/data/b.json"}); b == a {
		t.Error("different manifests share a lock file")
	}
	if def := lockPath(Config{}, &Manifest{Path: "/data/a.json"}); !strings.HasPrefix(def, "/tmp/") {
		t.Errorf("default lock dir: %q", def)
	}
}

func TestLockSymlinkRefused(t *testing.T) {
	c := Config{LockDir: t.TempDir()}
	m := &Manifest{Path: "/data/a.json"}
	if err := os.Symlink("/dev/zero", lockPath(c, m)); err != nil {
		t.Fatal(err)
	}
	_, err := c.Lock(m)
	jrRefused(t, err, "cannot create the lock file")
}
