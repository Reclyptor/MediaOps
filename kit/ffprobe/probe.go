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

// Chapter is one chapter, in seconds.
type Chapter struct {
	Start, End float64
	// Title is the chapter's title tag, "" when it has none.
	Title string
}

// Format is the container-level facts of a file.
type Format struct {
	Filename string
	Size     int64
	Duration float64
	// Title is the container title tag, "" when absent.
	Title string
	// Tags has lower-cased keys.
	Tags map[string]string
}

// Info is everything Probe reads from one file.
type Info struct {
	Format   Format
	Streams  []Stream
	Chapters []Chapter
}

type rawChapter struct {
	StartTime *string           `json:"start_time"`
	EndTime   *string           `json:"end_time"`
	Tags      map[string]string `json:"tags"`
}

// runJSON runs ffprobe with args and returns its stdout. A refused or failed
// start and a non-zero exit are errors that name the file.
func runJSON(ctx context.Context, r proc.Runner, path string, args ...string) ([]byte, error) {
	argv := append([]string{"ffprobe", "-v", "error"}, args...)
	res, err := r.Run(ctx, append(argv, path)...)
	if err != nil {
		return nil, fmt.Errorf("running ffprobe on %s: %w", path, err)
	}
	if res.ExitCode != 0 {
		return nil, fmt.Errorf("ffprobe exited %d on %s: %s", res.ExitCode, path, tail(res.Stderr))
	}
	return res.Stdout, nil
}

// Probe reads format, streams and chapters of path in one ffprobe run. It
// fails closed: a size or duration that is missing, not a number, or not
// positive is an error rather than a zero that looks like an empty file.
func Probe(ctx context.Context, r proc.Runner, path string) (Info, error) {
	stdout, err := runJSON(ctx, r, path, "-show_format", "-show_streams", "-show_chapters", "-of", "json")
	if err != nil {
		return Info{}, err
	}
	var out struct {
		Format *struct {
			Filename string            `json:"filename"`
			Size     *string           `json:"size"`
			Duration *string           `json:"duration"`
			Tags     map[string]string `json:"tags"`
		} `json:"format"`
		Streams  *[]rawStream `json:"streams"`
		Chapters []rawChapter `json:"chapters"`
	}
	if err := json.Unmarshal(stdout, &out); err != nil {
		return Info{}, fmt.Errorf("ffprobe printed unreadable JSON for %s: %w", path, err)
	}
	if out.Format == nil {
		return Info{}, fmt.Errorf("ffprobe reports no format for %s", path)
	}
	if out.Streams == nil {
		return Info{}, fmt.Errorf("ffprobe lists no streams array for %s", path)
	}

	f := out.Format
	if f.Size == nil {
		return Info{}, fmt.Errorf("ffprobe reports no size for %s", path)
	}
	size, err := strconv.ParseInt(*f.Size, 10, 64)
	if err != nil || size <= 0 {
		return Info{}, fmt.Errorf("ffprobe reports a size of %q for %s, which is not a positive integer", *f.Size, path)
	}
	if f.Duration == nil {
		return Info{}, fmt.Errorf("ffprobe reports no duration for %s", path)
	}
	duration, err := parseSeconds(*f.Duration)
	if err != nil || duration <= 0 {
		return Info{}, fmt.Errorf("ffprobe reports a duration of %q for %s, which is not a positive number", *f.Duration, path)
	}

	tags := make(map[string]string, len(f.Tags))
	for k, v := range f.Tags {
		tags[strings.ToLower(k)] = v
	}
	streams, err := convertStreams(*out.Streams, path)
	if err != nil {
		return Info{}, err
	}
	chapters, err := convertChapters(out.Chapters, path)
	if err != nil {
		return Info{}, err
	}
	return Info{
		Format:   Format{Filename: f.Filename, Size: size, Duration: duration, Title: tags["title"], Tags: tags},
		Streams:  streams,
		Chapters: chapters,
	}, nil
}

// Chapters lists the chapters of path, an empty slice when it has none.
func Chapters(ctx context.Context, r proc.Runner, path string) ([]Chapter, error) {
	stdout, err := runJSON(ctx, r, path, "-show_chapters", "-of", "json")
	if err != nil {
		return nil, err
	}
	var out struct {
		Chapters []rawChapter `json:"chapters"`
	}
	if err := json.Unmarshal(stdout, &out); err != nil {
		return nil, fmt.Errorf("ffprobe printed unreadable JSON for %s: %w", path, err)
	}
	return convertChapters(out.Chapters, path)
}

func convertChapters(raws []rawChapter, path string) ([]Chapter, error) {
	chapters := make([]Chapter, 0, len(raws))
	for i, raw := range raws {
		if raw.StartTime == nil || raw.EndTime == nil {
			return nil, fmt.Errorf("chapter %d of %s has no start_time or end_time", i, path)
		}
		start, err := parseSeconds(*raw.StartTime)
		if err != nil {
			return nil, fmt.Errorf("chapter %d of %s has a start_time of %q, which is not a number", i, path, *raw.StartTime)
		}
		end, err := parseSeconds(*raw.EndTime)
		if err != nil {
			return nil, fmt.Errorf("chapter %d of %s has an end_time of %q, which is not a number", i, path, *raw.EndTime)
		}
		title := ""
		for k, v := range raw.Tags {
			if strings.EqualFold(k, "title") {
				title = v
			}
		}
		chapters = append(chapters, Chapter{Start: start, End: end, Title: title})
	}
	return chapters, nil
}

// parseSeconds parses a finite number of seconds.
func parseSeconds(raw string) (float64, error) {
	v, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return 0, err
	}
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return 0, fmt.Errorf("%q is not finite", raw)
	}
	return v, nil
}
