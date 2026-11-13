package makemkv

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const testKey = "T-fakefakefakefakefakefakefake_0123456789"

func readConf(t *testing.T, home string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(home, ".MakeMKV", "settings.conf"))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestSettings(t *testing.T) {
	t.Run("writes a 0600 file with the key and data dir", func(t *testing.T) {
		home := t.TempDir()
		if err := (Settings{Key: testKey, Home: home}).Write(); err != nil {
			t.Fatal(err)
		}
		want := "app_Key = \"" + testKey + "\"\napp_DataDir = \"" + filepath.Join(home, "makemkv") + "\"\n"
		if got := readConf(t, home); got != want {
			t.Errorf("settings.conf = %q, want %q", got, want)
		}
		assertMode(t, filepath.Join(home, ".MakeMKV", "settings.conf"), 0o600)
		assertMode(t, filepath.Join(home, ".MakeMKV"), 0o700)
		if st, err := os.Stat(filepath.Join(home, "makemkv")); err != nil || !st.IsDir() {
			t.Errorf("data dir was not created: %v", err)
		}
	})

	t.Run("replaces an existing file atomically and leaves no temp sibling", func(t *testing.T) {
		home := t.TempDir()
		for _, key := range []string{"T-first", "T-second"} {
			if err := (Settings{Key: key, Home: home}).Write(); err != nil {
				t.Fatal(err)
			}
		}
		if got := readConf(t, home); !strings.Contains(got, `"T-second"`) || strings.Contains(got, "T-first") {
			t.Errorf("settings.conf = %q", got)
		}
		entries, err := os.ReadDir(filepath.Join(home, ".MakeMKV"))
		if err != nil {
			t.Fatal(err)
		}
		if len(entries) != 1 {
			t.Errorf(".MakeMKV holds %d entries, want only settings.conf", len(entries))
		}
		assertMode(t, filepath.Join(home, ".MakeMKV", "settings.conf"), 0o600)
	})

	t.Run("refuses every character settings.conf cannot hold", func(t *testing.T) {
		for _, bad := range []string{`"`, `\`, "\n", "\r"} {
			home := t.TempDir()
			key := "T-abc" + bad + "def"
			err := (Settings{Key: key, Home: home}).Write()
			if err == nil {
				t.Fatalf("key with %q was accepted", bad)
			}
			assertNoKey(t, err, key)
			if _, statErr := os.Stat(filepath.Join(home, ".MakeMKV")); !os.IsNotExist(statErr) {
				t.Errorf("refused key still touched the disk under %q", home)
			}
		}
	})

	t.Run("a key with all refused characters leaks nothing", func(t *testing.T) {
		key := "T-sekrit\"\\\n\r-tail"
		err := (Settings{Key: key, Home: t.TempDir()}).Write()
		if err == nil {
			t.Fatal("accepted")
		}
		assertNoKey(t, err, key)
		assertNoKey(t, err, "sekrit")
	})

	t.Run("an empty key is refused", func(t *testing.T) {
		if err := (Settings{Home: t.TempDir()}).Write(); err == nil {
			t.Fatal("empty key accepted")
		}
	})

	t.Run("write failures never contain the key", func(t *testing.T) {
		home := t.TempDir()
		// A directory where the file must go makes the rename fail.
		if err := os.MkdirAll(filepath.Join(home, ".MakeMKV", "settings.conf"), 0o700); err != nil {
			t.Fatal(err)
		}
		err := (Settings{Key: testKey, Home: home}).Write()
		if err == nil {
			t.Fatal("expected a write failure")
		}
		assertNoKey(t, err, testKey)

		blocker := filepath.Join(t.TempDir(), "file")
		if err := os.WriteFile(blocker, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		err = (Settings{Key: testKey, Home: blocker}).Write()
		if err == nil {
			t.Fatal("expected a mkdir failure")
		}
		assertNoKey(t, err, testKey)
	})
}

func TestSettingsFromEnv(t *testing.T) {
	t.Run("trims the key and reads HOME", func(t *testing.T) {
		t.Setenv("MAKEMKV_KEY", "  "+testKey+"\n")
		t.Setenv("HOME", "/somewhere")
		s, err := SettingsFromEnv()
		if err != nil {
			t.Fatal(err)
		}
		if s.Key != testKey || s.Home != "/somewhere" {
			t.Errorf("got %+v", s)
		}
	})

	t.Run("HOME defaults to /work", func(t *testing.T) {
		t.Setenv("MAKEMKV_KEY", testKey)
		t.Setenv("HOME", "")
		s, err := SettingsFromEnv()
		if err != nil {
			t.Fatal(err)
		}
		if s.Home != "/work" {
			t.Errorf("Home = %q", s.Home)
		}
	})

	t.Run("a missing or blank key is an error", func(t *testing.T) {
		for _, v := range []string{"", "   \n"} {
			t.Setenv("MAKEMKV_KEY", v)
			if _, err := SettingsFromEnv(); err == nil {
				t.Errorf("MAKEMKV_KEY=%q accepted", v)
			}
		}
	})

	t.Run("a refused key errors without the key", func(t *testing.T) {
		for _, key := range []string{`T-ab"cd`, `T-ab\cd`, "T-ab\ncd"} {
			t.Setenv("MAKEMKV_KEY", key)
			_, err := SettingsFromEnv()
			if err == nil {
				t.Fatalf("key %q accepted", key)
			}
			assertNoKey(t, err, key)
			assertNoKey(t, err, "T-ab")
		}
	})
}

func assertMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := st.Mode().Perm(); got != want {
		t.Errorf("%s mode = %o, want %o", path, got, want)
	}
}

func assertNoKey(t *testing.T, err error, key string) {
	t.Helper()
	if strings.Contains(err.Error(), key) {
		t.Errorf("error %q contains the key", err)
	}
}
