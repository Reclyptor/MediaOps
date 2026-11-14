package inventory

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/reclyptor/mediaops/kit/proc"
)

// fakeProbe answers `ffprobe ... -of json PATH` from a table keyed by path and
// records every path it was asked about.
type fakeProbe struct {
	mu     sync.Mutex
	answer map[string]proc.Result
	asked  []string
}

func (f *fakeProbe) Run(_ context.Context, argv ...string) (proc.Result, error) {
	if argv[0] != "ffprobe" {
		return proc.Result{}, fmt.Errorf("unexpected program %q", argv[0])
	}
	path := argv[len(argv)-1]
	f.mu.Lock()
	defer f.mu.Unlock()
	f.asked = append(f.asked, path)
	res, ok := f.answer[path]
	if !ok {
		return proc.Result{ExitCode: 1, Stderr: []byte("No such file")}, nil
	}
	return res, nil
}

func (f *fakeProbe) Stream(context.Context, []string, func(string)) (int, error) {
	panic("mkv-probe never streams")
}

// ffprobeJSON is what ffprobe prints for a file of duration seconds with the
// given chapter starts (seconds), one video and one Japanese audio stream.
func ffprobeJSON(duration float64, title string, chapters ...float64) proc.Result {
	var ch []string
	for i, c := range chapters {
		end := duration
		if i+1 < len(chapters) {
			end = chapters[i+1]
		}
		ch = append(ch, fmt.Sprintf(`{"id": %d, "start_time": "%.6f", "end_time": "%.6f"}`, i, c, end))
	}
	tags := ""
	if title != "" {
		tags = fmt.Sprintf(`, "tags": {"TITLE": %q}`, title)
	}
	return proc.Result{Stdout: []byte(fmt.Sprintf(`{
 "streams": [
  {"index": 0, "codec_type": "video", "codec_name": "h264", "width": 1920, "height": 1080, "disposition": {"default": 1}},
  {"index": 1, "codec_type": "audio", "codec_name": "flac", "channels": 2, "disposition": {"default": 1}, "tags": {"language": "jpn", "title": "Japanese"}}
 ],
 "chapters": [%s],
 "format": {"filename": "x", "size": "1000", "duration": "%.6f"%s}
}`, strings.Join(ch, ","), duration, tags))}
}
