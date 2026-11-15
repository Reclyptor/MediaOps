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

// Stream is one stream of a media file. Tags has lower-cased keys.
type Stream struct {
	Index         int
	CodecType     string
	CodecName     string
	Channels      int
	Width, Height int
	Default       bool
	Tags          map[string]string
	// TimeBase is the stream's time base as ffprobe prints it, such as "1/1000".
	TimeBase string
	// NALLengthSize is ffprobe's nal_length_size for AVCC and HVCC h264 and
	// hevc, and 0 for every other stream.
	NALLengthSize int
	// SampleRate is the audio sample rate in Hz, 0 when ffprobe prints none.
	SampleRate int
	// SampleAspect is the sample (pixel) aspect ratio, 1 when ffprobe prints
	// none or an unset "0:1".
	SampleAspect float64
}

// rawStream is one entry of ffprobe's streams array.
type rawStream struct {
	Index         *int              `json:"index"`
	CodecType     *string           `json:"codec_type"`
	CodecName     string            `json:"codec_name"`
	Channels      int               `json:"channels"`
	Width         int               `json:"width"`
	Height        int               `json:"height"`
	TimeBase      string            `json:"time_base"`
	NALLengthSize string            `json:"nal_length_size"`
	SampleRate    string            `json:"sample_rate"`
	SampleAspect  string            `json:"sample_aspect_ratio"`
	Disposition   map[string]int    `json:"disposition"`
	Tags          map[string]string `json:"tags"`
}

// Streams lists the streams of path. A stream without an index or codec_type
// is an error, and so are two tag keys that collide once lower-cased with
// different values: Matroska track tags can shadow the Language element, and
// which of the two a reader shows is not ours to guess.
func Streams(ctx context.Context, r proc.Runner, path string) ([]Stream, error) {
	res, err := r.Run(ctx, "ffprobe", "-v", "error", "-show_streams", "-of", "json", path)
	if err != nil {
		return nil, fmt.Errorf("running ffprobe on %s: %w", path, err)
	}
	if res.ExitCode != 0 {
		return nil, fmt.Errorf("ffprobe exited %d on %s: %s", res.ExitCode, path, tail(res.Stderr))
	}

	var out struct {
		Streams *[]rawStream `json:"streams"`
	}
	if err := json.Unmarshal(res.Stdout, &out); err != nil {
		return nil, fmt.Errorf("ffprobe printed unreadable JSON for %s: %w", path, err)
	}
	if out.Streams == nil {
		return nil, fmt.Errorf("ffprobe lists no streams array for %s", path)
	}
	return convertStreams(*out.Streams, path)
}

func convertStreams(raws []rawStream, path string) ([]Stream, error) {
	streams := make([]Stream, 0, len(raws))
	for i, raw := range raws {
		if raw.Index == nil || raw.CodecType == nil {
			return nil, fmt.Errorf("stream %d of %s has no index or codec_type", i, path)
		}
		tags := make(map[string]string, len(raw.Tags))
		for k, v := range raw.Tags {
			lk := strings.ToLower(k)
			if prev, dup := tags[lk]; dup && prev != v {
				return nil, fmt.Errorf("stream %d of %s carries two %q tags that differ (%q and %q)", *raw.Index, path, lk, prev, v)
			}
			tags[lk] = v
		}
		nal := 0
		if raw.NALLengthSize != "" {
			n, err := strconv.Atoi(raw.NALLengthSize)
			if err != nil || n < 1 || n > 4 {
				return nil, fmt.Errorf("stream %d of %s reports a nal_length_size of %q, which is not 1 to 4", *raw.Index, path, raw.NALLengthSize)
			}
			nal = n
		}
		rate := 0
		if raw.SampleRate != "" {
			n, err := strconv.Atoi(raw.SampleRate)
			if err != nil || n < 1 {
				return nil, fmt.Errorf("stream %d of %s reports a sample_rate of %q, which is not a positive integer", *raw.Index, path, raw.SampleRate)
			}
			rate = n
		}
		sar, err := parseSampleAspect(raw.SampleAspect)
		if err != nil {
			return nil, fmt.Errorf("stream %d of %s reports a sample_aspect_ratio of %q, which is not N:D: %w", *raw.Index, path, raw.SampleAspect, err)
		}
		streams = append(streams, Stream{
			Index: *raw.Index, CodecType: *raw.CodecType, CodecName: raw.CodecName,
			Channels: raw.Channels, Width: raw.Width, Height: raw.Height,
			Default: raw.Disposition["default"] == 1, Tags: tags,
			TimeBase: raw.TimeBase, NALLengthSize: nal, SampleRate: rate, SampleAspect: sar,
		})
	}
	return streams, nil
}

// parseSampleAspect reads ffprobe's "N:D". Absent, "N/A" and an unset "0:1"
// (or any zero numerator or denominator) mean square pixels.
func parseSampleAspect(raw string) (float64, error) {
	if raw == "" || raw == "N/A" {
		return 1, nil
	}
	num, den, ok := strings.Cut(raw, ":")
	if !ok {
		return 0, fmt.Errorf("no colon")
	}
	n, err := strconv.ParseUint(num, 10, 32)
	if err != nil {
		return 0, err
	}
	d, err := strconv.ParseUint(den, 10, 32)
	if err != nil {
		return 0, err
	}
	if n == 0 || d == 0 {
		return 1, nil
	}
	return float64(n) / float64(d), nil
}

// CountFrames decodes the first video stream of path and returns how many
// frames and packets it read. A missing stream, or a count that is absent,
// not a number or negative, is an error.
func CountFrames(ctx context.Context, r proc.Runner, path string) (frames, packets int64, err error) {
	res, err := r.Run(ctx, "ffprobe", "-v", "error", "-count_frames", "-count_packets", "-select_streams", "v:0",
		"-show_entries", "stream=nb_read_frames,nb_read_packets", "-of", "json", path)
	if err != nil {
		return 0, 0, fmt.Errorf("running ffprobe on %s: %w", path, err)
	}
	if res.ExitCode != 0 {
		return 0, 0, fmt.Errorf("ffprobe exited %d on %s: %s", res.ExitCode, path, tail(res.Stderr))
	}

	var out struct {
		Streams []struct {
			Frames  *string `json:"nb_read_frames"`
			Packets *string `json:"nb_read_packets"`
		} `json:"streams"`
	}
	if err := json.Unmarshal(res.Stdout, &out); err != nil {
		return 0, 0, fmt.Errorf("ffprobe printed unreadable JSON for %s: %w", path, err)
	}
	if len(out.Streams) == 0 {
		return 0, 0, fmt.Errorf("%s has no video stream to count", path)
	}
	st := out.Streams[0]
	if st.Frames == nil || st.Packets == nil {
		return 0, 0, fmt.Errorf("ffprobe reports no frame or packet count for %s", path)
	}
	if frames, err = parseCount(*st.Frames, "frame", path); err != nil {
		return 0, 0, err
	}
	if packets, err = parseCount(*st.Packets, "packet", path); err != nil {
		return 0, 0, err
	}
	return frames, packets, nil
}

func parseCount(raw, what, path string) (int64, error) {
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("ffprobe reports a %s count of %q for %s, which is not a number", what, raw, path)
	}
	if n < 0 {
		return 0, fmt.Errorf("ffprobe reports a negative %s count of %d for %s", what, n, path)
	}
	return n, nil
}

// DecodeErrors decodes every stream of path to a null sink and returns the
// error lines ffmpeg printed, none for a clean file. A decode that does not
// exit 0 is an error rather than a list: a failure to run must never look like
// a clean file.
func DecodeErrors(ctx context.Context, r proc.Runner, path string) ([]string, error) {
	res, err := r.Run(ctx, "ffmpeg", "-nostdin", "-v", "error", "-i", path, "-f", "null", "-")
	if err != nil {
		return nil, fmt.Errorf("running ffmpeg on %s: %w", path, err)
	}
	if res.ExitCode != 0 {
		return nil, fmt.Errorf("ffmpeg exited %d decoding %s: %s", res.ExitCode, path, tail(res.Stderr))
	}
	var lines []string
	for _, line := range strings.Split(string(res.Stderr), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			lines = append(lines, line)
		}
	}
	return lines, nil
}
