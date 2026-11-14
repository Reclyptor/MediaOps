// Package mkvtool edits Matroska files with the mkvtoolnix programs and checks
// that each edit took.
package mkvtool

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/reclyptor/mediaops/kit/proc"
)

// SetTitle sets the segment title of file, then reads it back with mkvmerge -J.
// mkvpropedit exits 1 for warnings, which are accepted; 2 and above is a
// failure. The read-back is what proves the title landed, so an unreadable or
// different title is an error too.
func SetTitle(ctx context.Context, r proc.Runner, file, title string) error {
	if err := runEdit(ctx, r, file, "mkvpropedit", file, "--edit", "info", "--set", "title="+title); err != nil {
		return err
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
	id, err := Identify(ctx, r, file)
	if err != nil {
		return "", err
	}
	return id.Title, nil
}

// Identification is what mkvmerge -J reports about a file.
type Identification struct {
	Title  string
	Tracks []Track
}

// Track is one track as mkvmerge numbers and types it.
type Track struct {
	ID int
	// Type is "video", "audio" or "subtitles", as mkvmerge prints it.
	Type string
	// UID is the track's Matroska UID in decimal, empty when the container has
	// none. It is 64 bits wide, so it never passes through a float.
	UID      string
	Name     string
	Language string
	Default  bool
}

// Identify reads file with mkvmerge -J. Exit 1 is warnings and accepted; 2 and
// above is a failure. A track without an id or type is an error.
func Identify(ctx context.Context, r proc.Runner, file string) (Identification, error) {
	res, err := r.Run(ctx, "mkvmerge", "-J", file)
	if err != nil {
		return Identification{}, fmt.Errorf("running mkvmerge -J on %s: %w", file, err)
	}
	if res.ExitCode >= 2 || res.ExitCode < 0 {
		return Identification{}, fmt.Errorf("mkvmerge -J failed on %s (exit %d): %s", file, res.ExitCode, tail(res.Stdout, res.Stderr))
	}
	var out struct {
		Container struct {
			Properties struct {
				Title string `json:"title"`
			} `json:"properties"`
		} `json:"container"`
		Tracks []struct {
			ID         *int    `json:"id"`
			Type       *string `json:"type"`
			Properties struct {
				UID      json.Number `json:"uid"`
				Name     string      `json:"track_name"`
				Language string      `json:"language"`
				Default  bool        `json:"default_track"`
			} `json:"properties"`
		} `json:"tracks"`
	}
	if err := json.Unmarshal(res.Stdout, &out); err != nil {
		return Identification{}, fmt.Errorf("mkvmerge -J printed unreadable JSON for %s: %w", file, err)
	}
	id := Identification{Title: out.Container.Properties.Title}
	for i, t := range out.Tracks {
		if t.ID == nil || t.Type == nil {
			return Identification{}, fmt.Errorf("mkvmerge -J lists track %d of %s without an id or type", i, file)
		}
		id.Tracks = append(id.Tracks, Track{
			ID: *t.ID, Type: *t.Type, UID: t.Properties.UID.String(),
			Name: t.Properties.Name, Language: t.Properties.Language, Default: t.Properties.Default,
		})
	}
	return id, nil
}

// Remux copies every stream of src into a new Matroska file at dst with
// mkvmerge, never re-encoding. Exit 1 is warnings and accepted; 2 and above, or
// an output that does not exist afterwards, is an error carrying the output.
func Remux(ctx context.Context, r proc.Runner, src, dst string) error {
	res, err := r.Run(ctx, "mkvmerge", "-q", "-o", dst, src)
	if err != nil {
		return fmt.Errorf("running mkvmerge on %s: %w", src, err)
	}
	if res.ExitCode >= 2 || res.ExitCode < 0 {
		return fmt.Errorf("mkvmerge failed remuxing %s (exit %d): %s", src, res.ExitCode, tail(res.Stdout, res.Stderr))
	}
	info, err := os.Stat(dst)
	if err != nil || !info.Mode().IsRegular() {
		return fmt.Errorf("mkvmerge left no file at %s after remuxing %s: %s", dst, src, tail(res.Stdout, res.Stderr))
	}
	return nil
}

// runEdit runs a mkvpropedit-style command on file. Exit 1 is warnings and
// accepted; 2 and above is a failure.
func runEdit(ctx context.Context, r proc.Runner, file string, argv ...string) error {
	res, err := r.Run(ctx, argv...)
	if err != nil {
		return fmt.Errorf("running %s on %s: %w", argv[0], file, err)
	}
	if res.ExitCode >= 2 || res.ExitCode < 0 {
		return fmt.Errorf("%s failed on %s (exit %d): %s", argv[0], file, res.ExitCode, tail(res.Stdout, res.Stderr))
	}
	return nil
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
