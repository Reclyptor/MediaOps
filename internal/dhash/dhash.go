// Package dhash is the 64-bit difference hash the fingerprint tools compare
// frames with. It reproduces the Python tools' hash bit for bit, which
// testdata/ proves against their recorded output.
package dhash

import (
	"fmt"
	"math/bits"
	"strconv"
)

// Width and Height are the size ffmpeg scales every sampled frame to. A row
// has Width pixels and so Width-1 comparisons: eight rows of eight bits.
const (
	Width  = 9
	Height = 8
)

// FrameSize is the byte length of one gray frame.
const FrameSize = Width * Height

// Threshold is the Hamming distance up to which two hashes count as the same
// picture.
const Threshold = 6

// Hash returns the dHash of one Width x Height gray frame: for each row in
// order, one bit per pixel that is brighter than its right neighbour, first
// pixel in the most significant position. px must be exactly FrameSize bytes.
func Hash(px []byte) uint64 {
	if len(px) != FrameSize {
		panic(fmt.Sprintf("dhash: frame is %d bytes, want %d", len(px), FrameSize))
	}
	var h uint64
	for row := 0; row < Height; row++ {
		base := row * Width
		for col := 0; col < Width-1; col++ {
			h <<= 1
			if px[base+col] > px[base+col+1] {
				h |= 1
			}
		}
	}
	return h
}

// Hashes hashes consecutive frames of raw gray video. A trailing partial
// frame is dropped, as the Python did.
func Hashes(raw []byte) []uint64 {
	out := make([]uint64, 0, len(raw)/FrameSize)
	for i := 0; i+FrameSize <= len(raw); i += FrameSize {
		out = append(out, Hash(raw[i:i+FrameSize]))
	}
	return out
}

// Distance is the number of bits in which a and b differ.
func Distance(a, b uint64) int {
	return bits.OnesCount64(a ^ b)
}

// Overlap is the fraction of a that has some hash in b within thresh bits. It
// is 0 when either side is empty.
func Overlap(a, b []uint64, thresh int) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	hits := 0
	for _, p := range a {
		for _, q := range b {
			if Distance(p, q) <= thresh {
				hits++
				break
			}
		}
	}
	return float64(hits) / float64(len(a))
}

// Format renders h as the 16 lowercase hex digits fingerprints are stored in.
func Format(h uint64) string {
	return fmt.Sprintf("%016x", h)
}

// Parse reads a hash written by Format: exactly 16 hex digits.
func Parse(s string) (uint64, error) {
	if len(s) != 16 {
		return 0, fmt.Errorf("hash %q is not 16 hex digits", s)
	}
	h, err := strconv.ParseUint(s, 16, 64)
	if err != nil {
		return 0, fmt.Errorf("hash %q is not 16 hex digits", s)
	}
	return h, nil
}
