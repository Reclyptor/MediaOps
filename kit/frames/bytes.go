package frames

import (
	"context"
	"fmt"
	"io"
	"strconv"

	"github.com/reclyptor/mediaops/kit/proc"
)

// StreamBytes copies one stream of path out as raw bytes into w and returns
// how many it wrote. For PCM that is the sample data with every container
// framing removed, which is what stays identical when a remux re-frames the
// stream. A write error from w kills the child and is returned; a non-zero
// exit is an error carrying the tail of stderr.
func StreamBytes(ctx context.Context, p proc.Piper, path string, stream int, w io.Writer) (int64, error) {
	argv := []string{"ffmpeg", "-nostdin", "-v", "error", "-i", path, "-map", "0:" + strconv.Itoa(stream), "-c", "copy", "-f", "rawvideo", "-"}
	var n int64
	stderr, exit, err := p.Pipe(ctx, argv, func(stdout io.Reader) error {
		var err error
		n, err = io.Copy(w, stdout)
		return err
	})
	if err != nil {
		return 0, fmt.Errorf("copying stream %d of %s: %w", stream, path, err)
	}
	if exit != 0 {
		return 0, fmt.Errorf("ffmpeg exited %d copying stream %d of %s: %s", exit, stream, path, tail(stderr))
	}
	return n, nil
}
