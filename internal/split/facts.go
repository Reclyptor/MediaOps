package split

import (
	"context"
	"encoding/hex"
	"fmt"
	"os"

	"github.com/reclyptor/mediaops/kit/ffprobe"
	"github.com/reclyptor/mediaops/kit/frames"
	"github.com/reclyptor/mediaops/kit/proc"
)

// Tools is what split needs to start children: ffprobe and mkvmerge through
// Run, and streamed ffmpeg output through Pipe. *proc.Exec is both.
type Tools interface {
	proc.Runner
	proc.Piper
}

// statRegular returns the size of path, which must be a regular file.
func statRegular(path string) (int64, error) {
	info, err := os.Stat(path)
	if err != nil {
		return 0, err
	}
	if !info.Mode().IsRegular() {
		return 0, fmt.Errorf("%s is not a regular file", path)
	}
	return info.Size(), nil
}

// hashedStream reports whether the stream is one of the three kinds a split
// carries and so takes part in packet accounting.
func hashedStream(s ffprobe.Stream) bool {
	switch s.CodecType {
	case "video", "audio", "subtitle":
		return true
	}
	return false
}

// payloadFilter is the NAL filter that makes a video stream comparable across
// a mkvmerge remux: it drops the access unit delimiters mkvmerge removes.
func payloadFilter(s ffprobe.Stream) frames.NALFilter {
	if s.CodecType != "video" || s.NALLengthSize == 0 {
		return frames.KeepAll
	}
	switch s.CodecName {
	case "h264":
		return frames.DropH264AUD
	case "hevc":
		return frames.DropHEVCAUD
	}
	return frames.KeepAll
}

// facts is everything verify reads from one file.
type facts struct {
	path   string
	size   int64
	info   ffprobe.Info
	hashes frames.Hashes
	// seqs holds each accounted stream's per-packet digest, in file order.
	seqs map[int][][16]byte
	// aud counts the NAL units the filter removed, per stream.
	aud map[int]int
}

// load reads path's probe data and per-packet digests. filters says, per
// stream index, which NAL units to remove before hashing a payload; the source
// decides it and every part follows.
func load(ctx context.Context, ex Tools, path string, size int64, filters map[int]frames.NALFilter) (*facts, error) {
	info, err := ffprobe.Probe(ctx, ex, path)
	if err != nil {
		return nil, err
	}
	f := &facts{path: path, size: size, info: info, seqs: map[int][][16]byte{}, aud: map[int]int{}}
	if f.hashes, err = frames.CopyHashes(ctx, ex, path); err != nil {
		return nil, err
	}
	for _, s := range info.Streams {
		if !hashedStream(s) {
			continue
		}
		packets := f.hashes.Packets[s.Index]
		if len(packets) == 0 {
			continue
		}
		if tb := f.hashes.TimeBase[s.Index]; tb.Num <= 0 || tb.Den <= 0 {
			return nil, fmt.Errorf("framemd5 of %s has packets for stream %d but no time base for it", path, s.Index)
		}
		if isPCM(s) {
			// Compared as bytes, not packets; see pcm.go.
			continue
		}
		filter := filters[s.Index]
		if filter != frames.KeepAll {
			sizes := make([]int, len(packets))
			for i, p := range packets {
				sizes[i] = p.Size
			}
			digests, removed, err := frames.PayloadHashes(ctx, ex, path, s.Index, sizes, filter, s.NALLengthSize)
			if err != nil {
				return nil, err
			}
			f.seqs[s.Index], f.aud[s.Index] = digests, removed
			continue
		}
		digests := make([][16]byte, len(packets))
		for i, p := range packets {
			raw, err := hex.DecodeString(p.MD5)
			if err != nil || len(raw) != len(digests[i]) {
				return nil, fmt.Errorf("framemd5 of %s has a digest %q that is not an MD5", path, p.MD5)
			}
			digests[i] = [16]byte(raw)
		}
		f.seqs[s.Index] = digests
	}
	return f, nil
}

// seconds converts a pts in a stream's time base to seconds. A stream with no
// time base line has no packets worth converting.
func (f *facts) seconds(stream int, pts int64) float64 {
	tb := f.hashes.TimeBase[stream]
	return float64(pts) * float64(tb.Num) / float64(tb.Den)
}

// pts returns the framemd5 pts of packet i of the stream, in seconds.
func (f *facts) pts(stream, i int) float64 {
	return f.seconds(stream, f.hashes.Packets[stream][i].PTS)
}
