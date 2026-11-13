// Package mkvtool edits Matroska files with the mkvtoolnix programs and checks
// that each edit took.
package mkvtool

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/reclyptor/mediaops/kit/proc"
)

// SetTitle sets the segment title of file, then reads it back with mkvmerge -J.
// mkvpropedit exits 1 for warnings, which are accepted; 2 and above is a
// failure. The read-back is what proves the title landed, so an unreadable or
// different title is an error too.
func SetTitle(ctx context.Context, r proc.Runner, file, title string) error {
	res, err := r.Run(ctx, "mkvpropedit", file, "--edit", "info", "--set", "title="+title)
	if err != nil {
		return fmt.Errorf("running mkvpropedit on %s: %w", file, err)
	}
	if res.ExitCode >= 2 || res.ExitCode < 0 {
		return fmt.Errorf("mkvpropedit failed on %s (exit %d): %s", file, res.ExitCode, tail(res.Stdout, res.Stderr))
	}

	got, err := readTitle(ctx, r, file)
	if err != nil {
		return err
	}
	if got != title {
		return fmt.Errorf("the title of %s reads back as %q after setting %q", file, got, title)
	}
	return nil
}

func readTitle(ctx context.Context, r proc.Runner, file string) (string, error) {
	res, err := r.Run(ctx, "mkvmerge", "-J", file)
	if err != nil {
		return "", fmt.Errorf("running mkvmerge -J on %s: %w", file, err)
	}
	if res.ExitCode >= 2 || res.ExitCode < 0 {
		return "", fmt.Errorf("mkvmerge -J failed on %s (exit %d): %s", file, res.ExitCode, tail(res.Stdout, res.Stderr))
	}
	var out struct {
		Container struct {
			Properties struct {
				Title string `json:"title"`
			} `json:"properties"`
		} `json:"container"`
	}
	if err := json.Unmarshal(res.Stdout, &out); err != nil {
		return "", fmt.Errorf("mkvmerge -J printed unreadable JSON for %s: %w", file, err)
	}
	return out.Container.Properties.Title, nil
}

// tail is the last 200 characters of what the program said, stdout first
// because mkvtoolnix reports errors there.
func tail(stdout, stderr []byte) string {
	s := strings.TrimSpace(string(stdout) + "\n" + string(stderr))
	if len(s) > 200 {
		s = s[len(s)-200:]
	}
	return s
}
