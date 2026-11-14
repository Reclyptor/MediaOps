package split

import (
	"context"
	"crypto/md5"
	"fmt"
	"strings"

	"github.com/reclyptor/mediaops/kit/ffprobe"
	"github.com/reclyptor/mediaops/kit/frames"
)

// mkvmerge re-frames PCM into packets of its own size even on a plain remux,
// so PCM cannot be accounted packet by packet. Its sample bytes do survive, so
// a PCM stream is compared as one byte stream: the source's against the
// parts' laid end to end.

func isPCM(s ffprobe.Stream) bool {
	return s.CodecType == "audio" && strings.HasPrefix(s.CodecName, "pcm_")
}

// pcmSampleBytes is the size of one sample of each PCM layout ffmpeg names.
// Anything else is unknown, and V4 cannot place a part's audio without it.
var pcmSampleBytes = map[string]int{
	"u8": 1, "s8": 1,
	"s16le": 2, "s16be": 2, "s24le": 3, "s24be": 3, "s32le": 4, "s32be": 4,
	"f32le": 4, "f32be": 4, "f64le": 8, "f64be": 8,
}

// pcmBytesPerSecond is how many bytes one second of the stream occupies.
func pcmBytesPerSecond(codec string, rate, channels int) (int, error) {
	size, ok := pcmSampleBytes[strings.TrimPrefix(codec, "pcm_")]
	if !ok {
		return 0, fmt.Errorf("the PCM layout %s has no known sample size", codec)
	}
	if rate < 1 || channels < 1 {
		return 0, fmt.Errorf("the %s stream reports a sample rate of %d and %d channels", codec, rate, channels)
	}
	return rate * channels * size, nil
}

// pcmResult is the byte comparison of one PCM stream.
type pcmResult struct {
	srcBytes  int64
	srcSum    [16]byte
	partBytes []int64
	partsSum  [16]byte
}

func (r *pcmResult) total() (n int64) {
	for _, b := range r.partBytes {
		n += b
	}
	return n
}

func (r *pcmResult) identical() bool {
	return r.srcBytes == r.total() && r.srcSum == r.partsSum
}

// offsets is where each part's bytes begin in the source stream.
func (r *pcmResult) offsets() []int64 {
	out := make([]int64, len(r.partBytes))
	var at int64
	for i, n := range r.partBytes {
		out[i], at = at, at+n
	}
	return out
}

// comparePCM streams the source's raw bytes through one MD5 and the parts'
// through another, in order, without holding either in memory.
func comparePCM(ctx context.Context, ex Tools, src string, parts []string, stream int) (*pcmResult, error) {
	res := &pcmResult{partBytes: make([]int64, len(parts))}
	err := parallel(ctx, 2, func(ctx context.Context, i int) error {
		h := md5.New()
		if i == 0 {
			n, err := frames.StreamBytes(ctx, ex, src, stream, h)
			res.srcBytes, res.srcSum = n, [16]byte(h.Sum(nil))
			return err
		}
		for p, path := range parts {
			n, err := frames.StreamBytes(ctx, ex, path, stream, h)
			if err != nil {
				return err
			}
			res.partBytes[p] = n
		}
		res.partsSum = [16]byte(h.Sum(nil))
		return nil
	})
	if err != nil {
		return nil, err
	}
	return res, nil
}
