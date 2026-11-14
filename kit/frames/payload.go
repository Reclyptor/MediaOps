package frames

import (
	"context"
	"crypto/md5"
	"fmt"
	"hash"
	"io"
	"strconv"

	"github.com/reclyptor/mediaops/kit/proc"
)

// NALFilter names the NAL units removed from a packet before it is hashed.
// Access unit delimiters carry no picture data and mkvmerge drops them on a
// remux, so a copy is only comparable to its source once they are gone.
type NALFilter int

const (
	KeepAll     NALFilter = iota
	DropH264AUD           // AVCC h264: NAL type 9
	DropHEVCAUD           // HVCC hevc: NAL type 35
)

// PayloadHashes copies one stream of path out as raw packets and hashes each
// one, cut by sizes (the stream's packet sizes from CopyHashes of the same
// file). The filter removes its NAL units first, reading nalLengthSize-byte
// big-endian lengths, and the count of removed units is returned.
//
// It fails closed: NAL lengths that do not tile a packet exactly, output that
// ends before the last size, and bytes left after it are all errors.
func PayloadHashes(ctx context.Context, p proc.Piper, path string, stream int, sizes []int,
	filter NALFilter, nalLengthSize int) (digests [][16]byte, removed int, err error) {
	if filter != KeepAll && (nalLengthSize < 1 || nalLengthSize > 4) {
		return nil, 0, fmt.Errorf("nal length size %d for %s is not 1 to 4", nalLengthSize, path)
	}
	argv := []string{"ffmpeg", "-nostdin", "-v", "error", "-i", path, "-map", "0:" + strconv.Itoa(stream), "-c", "copy", "-f", "rawvideo", "-"}

	stderr, exit, err := p.Pipe(ctx, argv, func(stdout io.Reader) error {
		digests = make([][16]byte, 0, len(sizes))
		var buf []byte
		for i, size := range sizes {
			if size < 0 {
				return fmt.Errorf("packet %d has a negative size %d", i, size)
			}
			if cap(buf) < size {
				buf = make([]byte, size)
			}
			buf = buf[:size]
			if _, err := io.ReadFull(stdout, buf); err != nil {
				return fmt.Errorf("stream %d ended inside packet %d of %d: %w", stream, i, len(sizes), err)
			}
			h := md5.New()
			n, err := hashPacket(h, buf, filter, nalLengthSize)
			if err != nil {
				return fmt.Errorf("packet %d: %w", i, err)
			}
			removed += n
			digests = append(digests, [16]byte(h.Sum(nil)))
		}
		var extra [1]byte
		if n, _ := io.ReadFull(stdout, extra[:]); n != 0 {
			return fmt.Errorf("stream %d has bytes after its last packet", stream)
		}
		return nil
	})
	if err != nil {
		return nil, 0, fmt.Errorf("hashing stream %d of %s: %w", stream, path, err)
	}
	if exit != 0 {
		return nil, 0, fmt.Errorf("ffmpeg exited %d copying stream %d of %s: %s", exit, stream, path, tail(stderr))
	}
	return digests, removed, nil
}

// hashPacket writes pkt to h less the NAL units filter drops, and returns how
// many it dropped. Kept units are hashed with their length prefix.
func hashPacket(h hash.Hash, pkt []byte, filter NALFilter, lenSize int) (int, error) {
	if filter == KeepAll {
		h.Write(pkt)
		return 0, nil
	}
	removed := 0
	for pos := 0; pos < len(pkt); {
		if pos+lenSize > len(pkt) {
			return 0, fmt.Errorf("a NAL length at byte %d runs past the packet", pos)
		}
		var l int
		for _, b := range pkt[pos : pos+lenSize] {
			l = l<<8 | int(b)
		}
		end := pos + lenSize + l
		if l == 0 || end > len(pkt) {
			return 0, fmt.Errorf("a NAL of %d bytes at byte %d does not fit the %d byte packet", l, pos, len(pkt))
		}
		if nalDropped(pkt[pos+lenSize], filter) {
			removed++
		} else {
			h.Write(pkt[pos:end])
		}
		pos = end
	}
	return removed, nil
}

func nalDropped(header byte, filter NALFilter) bool {
	switch filter {
	case DropH264AUD:
		return header&0x1f == 9
	case DropHEVCAUD:
		return (header>>1)&0x3f == 35
	}
	return false
}
