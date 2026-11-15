package dhash

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/reclyptor/mediaops/kit/proc"
)

// Argv is the ffmpeg command line that decodes path at fps frames per second
// to raw 9x8 gray video on stdout, optionally seeking to from seconds first.
// It is the Python's argv byte for byte.
func Argv(path string, fps float64, from *float64) []string {
	argv := []string{"ffmpeg", "-nostdin", "-v", "error"}
	if from != nil {
		argv = append(argv, "-ss", pyFloat(*from))
	}
	return append(argv, "-i", path,
		"-vf", fmt.Sprintf("fps=%s,scale=%d:%d,format=gray", pyFloat(fps), Width, Height),
		"-f", "rawvideo", "-")
}

// pyFloat prints f as Python's str(float) does for the ordinary magnitudes
// seconds and frame rates take: the shortest round-trip digits, always with a
// decimal point.
func pyFloat(f float64) string {
	s := strconv.FormatFloat(f, 'f', -1, 64)
	if !strings.Contains(s, ".") {
		s += ".0"
	}
	return s
}

// Sample decodes path at fps frames per second (after seeking to from, when
// set) and returns the dHash of every frame. Frames are hashed as they
// stream, so memory does not grow with the length of the file. A trailing
// partial frame is dropped. A decode that fails is an error carrying the tail
// of ffmpeg's stderr, never a short list that reads as a quiet file.
func Sample(ctx context.Context, p proc.Piper, path string, fps float64, from *float64) ([]uint64, error) {
	var out []uint64
	stderr, exit, err := p.Pipe(ctx, Argv(path, fps, from), func(stdout io.Reader) error {
		out = out[:0]
		buf := make([]byte, FrameSize)
		for {
			_, err := io.ReadFull(stdout, buf)
			switch {
			case err == nil:
				out = append(out, Hash(buf))
			case errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF):
				return nil
			default:
				return err
			}
		}
	})
	if err != nil {
		return nil, fmt.Errorf("sampling %s: %w", path, err)
	}
	if exit != 0 {
		return nil, fmt.Errorf("ffmpeg exited %d sampling %s: %s", exit, path, tail(stderr))
	}
	return out, nil
}

func tail(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 200 {
		s = s[len(s)-200:]
	}
	return s
}
