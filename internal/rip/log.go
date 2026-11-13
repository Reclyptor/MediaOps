package rip

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/reclyptor/mediaops/kit/fsutil"
)

// logf writes one controller.log line: an RFC 3339 timestamp, then the message.
// Only the loop logs, so lines never interleave. The log is best effort; there
// is nowhere to report that it failed.
func (r *Run) logf(format string, args ...any) {
	_, _ = fmt.Fprintf(r.deps.Log, "%s %s\n", stamp(r.now()).Format(time.RFC3339), fmt.Sprintf(format, args...))
}

// openControllerLog opens the run's controller.log for appending. A finalize
// adds to the log of the run it reopens.
func openControllerLog(runDir string) (*os.File, error) {
	path := filepath.Join(runDir, "controller.log")
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, fsutil.FileMode)
	if err != nil {
		return nil, fmt.Errorf("opening %s: %w", path, err)
	}
	if err := f.Chmod(fsutil.FileMode); err != nil {
		f.Close()
		return nil, fmt.Errorf("setting the mode of %s: %w", path, err)
	}
	return f, nil
}
