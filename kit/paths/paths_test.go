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

func TestArchiveRoot(t *testing.T) {
	archive := func(t *testing.T, same bool, mode os.FileMode) Config {
		t.Helper()
		dir := filepath.Join(t.TempDir(), "archive")
		if err := os.Mkdir(dir, mode); err != nil {
			t.Fatal(err)
		}
		return Config{Archive: dir, StatDev: devs(same)}
	}

	t.Run("unset", func(t *testing.T) {
		_, err := Config{StatDev: devs(false)}.ArchiveRoot()
		if err == nil || !strings.Contains(err.Error(), "ARCHIVE_ROOT is not set") {
			t.Fatalf("err = %v, want not-set", err)
		}
	})

	t.Run("missing is never created", func(t *testing.T) {
		c := Config{Archive: filepath.Join(t.TempDir(), "nowhere"), StatDev: devs(false)}
		_, err := c.ArchiveRoot()
		if err == nil || !strings.Contains(err.Error(), "does not exist") {
			t.Fatalf("err = %v, want does-not-exist", err)
		}
		if _, statErr := os.Stat(c.Archive); !os.IsNotExist(statErr) {
			t.Fatalf("the guard created the archive root: %v", statErr)
		}
	})

	t.Run("a file", func(t *testing.T) {
		file := filepath.Join(t.TempDir(), "file")
		if err := os.WriteFile(file, nil, 0o660); err != nil {
			t.Fatal(err)
		}
		_, err := Config{Archive: file, StatDev: devs(false)}.ArchiveRoot()
		if err == nil || !strings.Contains(err.Error(), "not a directory") {
			t.Fatalf("err = %v, want not-a-directory", err)
		}
	})

	t.Run("same device as root", func(t *testing.T) {
		_, err := archive(t, true, 0o770).ArchiveRoot()
		if err == nil || !strings.Contains(err.Error(), "root filesystem") {
			t.Fatalf("err = %v, want root-filesystem", err)
		}
	})

	t.Run("unwritable", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root bypasses permission bits")
		}
		_, err := archive(t, false, 0o500).ArchiveRoot()
		if err == nil || !strings.Contains(err.Error(), "not writable") || !strings.Contains(err.Error(), "/etc/exports") {
			t.Fatalf("err = %v, want not-writable naming the export", err)
		}
	})

	t.Run("healthy", func(t *testing.T) {
		c := archive(t, false, 0o770)
		got, err := c.ArchiveRoot()
		if err != nil || got != c.Archive {
			t.Fatalf("ArchiveRoot() = %q, %v", got, err)
		}
	})

	t.Run("relative is made absolute", func(t *testing.T) {
		c := archive(t, false, 0o770)
		t.Chdir(filepath.Dir(c.Archive))
		c.Archive = "archive"
		got, err := c.ArchiveRoot()
		if err != nil || !filepath.IsAbs(got) {
			t.Fatalf("ArchiveRoot() = %q, %v", got, err)
		}
	})
}

func TestArchiveRootFromEnv(t *testing.T) {
	t.Setenv("ARCHIVE_ROOT", "")
	if got := FromEnv().Archive; got != "" {
		t.Fatalf("ARCHIVE_ROOT has a default: %q", got)
	}
	t.Setenv("ARCHIVE_ROOT", "/archive")
	if got := FromEnv().Archive; got != "/archive" {
		t.Fatalf("env = %q", got)
	}
}

func TestShareRoot(t *testing.T) {
	// Two shares under the media root, each its own device; the category
	// directories inside a share share its device, as child exports do.
	dev := func(path string) (uint64, error) {
		switch {
		case path == "/mnt/media/shareA" || strings.HasPrefix(path, "/mnt/media/shareA/"):
			return 10, nil
		case path == "/mnt/media/shareB" || strings.HasPrefix(path, "/mnt/media/shareB/"):
			return 20, nil
		case strings.HasPrefix(path, "/mnt/media/gone"):
			return 0, os.ErrNotExist
		}
		return 1, nil
	}
	c := Config{MediaRoot: "/mnt/media", StatDev: dev}

	cases := []struct{ path, want string }{
		{"/mnt/media/shareA/anime/X/a.mkv", "/mnt/media/shareA"},
		{"/mnt/media/shareA", "/mnt/media/shareA"},
		{"/mnt/media/shareB/film", "/mnt/media/shareB"},
		// the share is found for a path that no longer exists, which is the
		// usual question after a move
		{"/mnt/media/shareA/anime/X/moved.mkv", "/mnt/media/shareA"},
		// a sibling that merely starts with a share's name is not that share, and
		// neither is the media root, which is not a mount; with no mount between
		// the path and "/", the answer is "/"
		{"/mnt/media/shareAB/x", "/"},
		{"/mnt/media/gone/x", "/"},
	}
	for _, tc := range cases {
		if got := c.ShareRoot(tc.path); got != tc.want {
			t.Errorf("ShareRoot(%s) = %s, want %s", tc.path, got, tc.want)
		}
	}
}
