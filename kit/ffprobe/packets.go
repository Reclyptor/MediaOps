package ffprobe

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/reclyptor/mediaops/kit/proc"
)

// Packet is one demuxed packet. PTS and DTS are in the stream's time base and
// valid only when HasPTS and HasDTS say so: ffprobe omits them for packets the
// container gave no timestamp.
type Packet struct {
	Stream   int
	PTS, DTS int64
	HasPTS   bool
	HasDTS   bool
	// PTSTime is the presentation time in seconds, valid only when HasPTS.
	PTSTime  float64
	Size     int
	Keyframe bool
}

// Packets lists the packets of path in file order. sel and interval are
// ffprobe's -select_streams and -read_intervals values and are passed through
// verbatim when non-empty.
func Packets(ctx context.Context, r proc.Runner, path, sel, interval string) ([]Packet, error) {
	var args []string
	if sel != "" {
		args = append(args, "-select_streams", sel)
	}
	if interval != "" {
		args = append(args, "-read_intervals", interval)
	}
	args = append(args, "-show_entries", "packet=stream_index,pts,dts,pts_time,size,flags", "-of", "json")
	stdout, err := runJSON(ctx, r, path, args...)
	if err != nil {
		return nil, err
	}

	var out struct {
		Packets *[]struct {
			StreamIndex *int    `json:"stream_index"`
			PTS         *int64  `json:"pts"`
			DTS         *int64  `json:"dts"`
			PTSTime     *string `json:"pts_time"`
			Size        *string `json:"size"`
			Flags       string  `json:"flags"`
		} `json:"packets"`
	}
	if err := json.Unmarshal(stdout, &out); err != nil {
		return nil, fmt.Errorf("ffprobe printed unreadable JSON for %s: %w", path, err)
	}
	if out.Packets == nil {
		return nil, fmt.Errorf("ffprobe lists no packets array for %s", path)
	}

	packets := make([]Packet, 0, len(*out.Packets))
	for i, raw := range *out.Packets {
		if raw.StreamIndex == nil || raw.Size == nil {
			return nil, fmt.Errorf("packet %d of %s has no stream_index or size", i, path)
		}
		size, err := strconv.Atoi(*raw.Size)
		if err != nil || size < 0 {
			return nil, fmt.Errorf("packet %d of %s has a size of %q, which is not a size", i, path, *raw.Size)
		}
		p := Packet{
			Stream: *raw.StreamIndex, Size: size,
			Keyframe: strings.Contains(raw.Flags, "K"),
		}
		if raw.PTS != nil {
			p.PTS, p.HasPTS = *raw.PTS, true
			if raw.PTSTime == nil {
				return nil, fmt.Errorf("packet %d of %s has a pts but no pts_time", i, path)
			}
			if p.PTSTime, err = parseSeconds(*raw.PTSTime); err != nil {
				return nil, fmt.Errorf("packet %d of %s has a pts_time of %q, which is not a number", i, path, *raw.PTSTime)
			}
		}
		if raw.DTS != nil {
			p.DTS, p.HasDTS = *raw.DTS, true
		}
		packets = append(packets, p)
	}
	return packets, nil
}

// Keyframes returns the pts_time of every keyframe of the first video stream
// with from <= pts_time <= to, sorted. Only the interval [from, to] is read.
func Keyframes(ctx context.Context, r proc.Runner, path string, from, to float64) ([]float64, error) {
	interval := strconv.FormatFloat(from, 'f', -1, 64) + "%" + strconv.FormatFloat(to, 'f', -1, 64)
	packets, err := Packets(ctx, r, path, "v:0", interval)
	if err != nil {
		return nil, err
	}
	times := []float64{}
	for _, p := range packets {
		if p.Keyframe && p.HasPTS && p.PTSTime >= from && p.PTSTime <= to {
			times = append(times, p.PTSTime)
		}
	}
	sort.Float64s(times)
	return times, nil
}
