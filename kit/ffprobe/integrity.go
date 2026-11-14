package ffprobe

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/reclyptor/mediaops/kit/proc"
)

// Integrity reads the container duration of path and every line ffprobe
// printed to stderr while parsing, none for a clean file. A failed run or a
// duration that is not positive is an error: a file ffprobe could not read
// must never look clean.
func Integrity(ctx context.Context, r proc.Runner, path string) (duration float64, warnings []string, err error) {
	res, err := r.Run(ctx, "ffprobe", "-v", "error", "-show_entries", "format=duration", "-of", "json", path)
	if err != nil {
		return 0, nil, fmt.Errorf("running ffprobe on %s: %w", path, err)
	}
	if res.ExitCode != 0 {
		return 0, nil, fmt.Errorf("ffprobe exited %d on %s: %s", res.ExitCode, path, tail(res.Stderr))
	}
	var out struct {
		Format *struct {
			Duration *string `json:"duration"`
		} `json:"format"`
	}
	if err := json.Unmarshal(res.Stdout, &out); err != nil {
		return 0, nil, fmt.Errorf("ffprobe printed unreadable JSON for %s: %w", path, err)
	}
	if out.Format == nil || out.Format.Duration == nil {
		return 0, nil, fmt.Errorf("ffprobe reports no duration for %s", path)
	}
	duration, err = parseSeconds(*out.Format.Duration)
	if err != nil || duration <= 0 {
		return 0, nil, fmt.Errorf("ffprobe reports a duration of %q for %s, which is not a positive number", *out.Format.Duration, path)
	}
	for _, line := range strings.Split(string(res.Stderr), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			warnings = append(warnings, line)
		}
	}
	return duration, warnings, nil
}
