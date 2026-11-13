// Package ffprobe reads media facts through ffprobe's JSON output.
package ffprobe

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/reclyptor/mediaops/kit/proc"
)

// Duration returns the container duration of path in seconds. Anything that
// does not yield a positive, finite number is an error: a duration that cannot
// be read must never look like a short file.
func Duration(ctx context.Context, r proc.Runner, path string) (float64, error) {
	res, err := r.Run(ctx, "ffprobe", "-v", "error", "-show_entries", "format=duration", "-of", "json", path)
	if err != nil {
		return 0, fmt.Errorf("running ffprobe on %s: %w", path, err)
	}
	if res.ExitCode != 0 {
		return 0, fmt.Errorf("ffprobe exited %d on %s: %s", res.ExitCode, path, tail(res.Stderr))
	}

	var out struct {
		Format *struct {
			Duration *string `json:"duration"`
		} `json:"format"`
	}
	if err := json.Unmarshal(res.Stdout, &out); err != nil {
		return 0, fmt.Errorf("ffprobe printed unreadable JSON for %s: %w", path, err)
	}
	if out.Format == nil || out.Format.Duration == nil {
		return 0, fmt.Errorf("ffprobe reports no duration for %s", path)
	}
	raw := *out.Format.Duration
	secs, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return 0, fmt.Errorf("ffprobe reports a duration of %q for %s, which is not a number", raw, path)
	}
	if math.IsNaN(secs) || math.IsInf(secs, 0) || secs <= 0 {
		return 0, fmt.Errorf("ffprobe reports an unusable duration of %q for %s", raw, path)
	}
	return secs, nil
}

func tail(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 200 {
		s = s[len(s)-200:]
	}
	return s
}
