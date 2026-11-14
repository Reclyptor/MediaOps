package frames

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/reclyptor/mediaops/kit/proc"
)

// GrayFrame decodes the first frame at or after at seconds, scaled to w by h
// and reduced to 8-bit gray. It returns exactly w*h bytes or an error.
func GrayFrame(ctx context.Context, r proc.Runner, path string, at float64, w, h int) ([]byte, error) {
	if w <= 0 || h <= 0 {
		return nil, fmt.Errorf("a %dx%d frame of %s is not a size", w, h, path)
	}
	res, err := r.Run(ctx, "ffmpeg", "-nostdin", "-v", "error", "-ss", seconds(at), "-i", path,
		"-frames:v", "1", "-vf", fmt.Sprintf("scale=%d:%d,format=gray", w, h), "-f", "rawvideo", "-")
	if err != nil {
		return nil, fmt.Errorf("running ffmpeg on %s: %w", path, err)
	}
	if res.ExitCode != 0 {
		return nil, fmt.Errorf("ffmpeg exited %d reading a frame of %s at %vs: %s", res.ExitCode, path, at, tail(res.Stderr))
	}
	if len(res.Stdout) != w*h {
		return nil, fmt.Errorf("ffmpeg returned %d bytes for a %dx%d frame of %s at %vs, want %d", len(res.Stdout), w, h, path, at, w*h)
	}
	return res.Stdout, nil
}

// PNG writes the frame at at seconds to out. out must end in .png and must not
// exist, and ffmpeg is also told -n, so nothing is ever overwritten. vf is an
// optional video filter chain.
func PNG(ctx context.Context, r proc.Runner, path string, at float64, vf, out string) error {
	if !strings.EqualFold(filepath.Ext(out), ".png") {
		return fmt.Errorf("%s does not end in .png", out)
	}
	if _, err := os.Lstat(out); err == nil {
		return fmt.Errorf("%s already exists", out)
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("checking %s: %w", out, err)
	}

	argv := []string{"ffmpeg", "-nostdin", "-v", "error", "-n", "-ss", seconds(at), "-i", path, "-frames:v", "1"}
	if vf != "" {
		argv = append(argv, "-vf", vf)
	}
	res, err := r.Run(ctx, append(argv, out)...)
	if err != nil {
		return fmt.Errorf("running ffmpeg on %s: %w", path, err)
	}
	if res.ExitCode != 0 {
		return fmt.Errorf("ffmpeg exited %d writing a frame of %s at %vs: %s", res.ExitCode, path, at, tail(res.Stderr))
	}
	// Seeking past the end exits 0 without writing anything.
	if fi, err := os.Lstat(out); err != nil || fi.Size() == 0 {
		return fmt.Errorf("ffmpeg wrote no frame of %s at %vs to %s", path, at, out)
	}
	return nil
}
