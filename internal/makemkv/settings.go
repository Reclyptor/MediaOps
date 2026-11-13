package makemkv

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/reclyptor/mediaops/kit/fsutil"
)

const defaultHome = "/work"

// Settings is what makemkvcon needs to run under a licence. Key is the licence
// key and must never appear in an error, a log or anything on the NAS.
type Settings struct {
	Key  string
	Home string
}

// SettingsFromEnv reads MAKEMKV_KEY, and HOME (default /work).
func SettingsFromEnv() (Settings, error) {
	home := os.Getenv("HOME")
	if home == "" {
		home = defaultHome
	}
	s := Settings{Key: strings.TrimSpace(os.Getenv("MAKEMKV_KEY")), Home: home}
	if err := s.validate(); err != nil {
		return Settings{}, err
	}
	return s, nil
}

// validate never echoes the key: settings.conf quotes it, so a quote, a
// backslash or a line break would corrupt the file.
func (s Settings) validate() error {
	if s.Key == "" {
		return errors.New("MAKEMKV_KEY is not set, so makemkvcon has no licence to run under")
	}
	if strings.ContainsAny(s.Key, "\"\\\r\n") {
		return errors.New("MAKEMKV_KEY contains a double quote, backslash or line break, which settings.conf cannot hold")
	}
	return nil
}

// Write puts the key in $HOME/.MakeMKV/settings.conf (0600, replaced
// atomically so a starting makemkvcon never reads half a key) and points
// app_DataDir at $HOME/makemkv.
func (s Settings) Write() error {
	if err := s.validate(); err != nil {
		return err
	}
	if s.Home == "" {
		return errors.New("cannot write the MakeMKV settings: no home directory")
	}
	confDir := filepath.Join(s.Home, ".MakeMKV")
	dataDir := filepath.Join(s.Home, "makemkv")
	if err := os.MkdirAll(confDir, 0o700); err != nil {
		return fmt.Errorf("creating the MakeMKV settings directory: %w", err)
	}
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return fmt.Errorf("creating the MakeMKV data directory: %w", err)
	}
	conf := "app_Key = \"" + s.Key + "\"\napp_DataDir = \"" + dataDir + "\"\n"
	if err := fsutil.WriteFile(filepath.Join(confDir, "settings.conf"), []byte(conf), 0o600); err != nil {
		return fmt.Errorf("writing the MakeMKV settings: %w", err)
	}
	return nil
}
