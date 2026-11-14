// Package frames reads per-packet and per-frame hashes out of ffmpeg, to prove
// that a stream copy or a cut left the media untouched. Nothing here encodes:
// every argv goes through kit/proc's guard.
package frames

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/reclyptor/mediaops/kit/proc"
)

// Rational is a time base such as 1/1000.
type Rational struct{ Num, Den int64 }

// PacketHash is one data line of ffmpeg's framemd5 output.
type PacketHash struct {
	Stream             int
	DTS, PTS, Duration int64
	Size               int
	// MD5 is 32 lowercase hex digits.
	MD5 string
	// SideData is the packet side data framemd5 lists after the payload hash
	// (skip samples, say). It is not part of the payload, so callers comparing
	// payloads leave it out and decide for themselves whether it matters.
	SideData []SideDataHash
}

// SideDataHash is one side data entry of a packet.
type SideDataHash struct {
	Size int
	MD5  string
}

// Hashes is a parsed framemd5 document.
type Hashes struct {
	TimeBase  map[int]Rational
	MediaType map[int]string
	// Packets holds each stream's lines in output order.
	Packets map[int][]PacketHash
}

// maxLine bounds one framemd5 line; real ones are under 100 bytes.
const maxLine = 1 << 20

// CopyHashes hashes every demuxed packet of path, payload after the
// demuxer's content decompression. The output is parsed as it streams, and a
// non-zero exit is an error carrying the tail of stderr.
func CopyHashes(ctx context.Context, p proc.Piper, path string) (Hashes, error) {
	argv := []string{"ffmpeg", "-nostdin", "-v", "error", "-i", path, "-map", "0", "-c", "copy", "-f", "framemd5", "-"}
	h, _, err := pipeFramemd5(ctx, p, argv, path)
	return h, err
}

// pipeFramemd5 runs argv and parses its stdout as framemd5. It returns stderr
// alongside, which a decode reports as findings.
func pipeFramemd5(ctx context.Context, p proc.Piper, argv []string, path string) (Hashes, []byte, error) {
	var h Hashes
	stderr, exit, err := p.Pipe(ctx, argv, func(stdout io.Reader) error {
		var perr error
		h, perr = parseFramemd5(stdout)
		if perr == nil {
			_, perr = io.Copy(io.Discard, stdout)
		}
		return perr
	})
	if err != nil {
		return Hashes{}, nil, fmt.Errorf("running ffmpeg on %s: %w", path, err)
	}
	if exit != 0 {
		return Hashes{}, nil, fmt.Errorf("ffmpeg exited %d hashing %s: %s", exit, path, tail(stderr))
	}
	return h, bytes.TrimSpace(stderr), nil
}

// parseFramemd5 reads a framemd5 document. Header lines other than #tb and
// #media_type are ignored; a data line that does not parse is an error.
func parseFramemd5(r io.Reader) (Hashes, error) {
	h := Hashes{TimeBase: map[int]Rational{}, MediaType: map[int]string{}, Packets: map[int][]PacketHash{}}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64<<10), maxLine)
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		switch {
		case line == "":
		case strings.HasPrefix(line, "#tb "):
			idx, val, err := parseHeader(line, "#tb ")
			if err != nil {
				return Hashes{}, fmt.Errorf("framemd5 line %d: %w", n, err)
			}
			num, den, ok := strings.Cut(val, "/")
			a, errA := strconv.ParseInt(num, 10, 64)
			b, errB := strconv.ParseInt(den, 10, 64)
			if !ok || errA != nil || errB != nil || a <= 0 || b <= 0 {
				return Hashes{}, fmt.Errorf("framemd5 line %d: %q is not a time base", n, val)
			}
			h.TimeBase[idx] = Rational{a, b}
		case strings.HasPrefix(line, "#media_type "):
			idx, val, err := parseHeader(line, "#media_type ")
			if err != nil {
				return Hashes{}, fmt.Errorf("framemd5 line %d: %w", n, err)
			}
			h.MediaType[idx] = val
		case line[0] == '#':
		default:
			ph, err := parseDataLine(line)
			if err != nil {
				return Hashes{}, fmt.Errorf("framemd5 line %d: %w", n, err)
			}
			h.Packets[ph.Stream] = append(h.Packets[ph.Stream], ph)
		}
	}
	if err := sc.Err(); err != nil {
		return Hashes{}, fmt.Errorf("reading framemd5: %w", err)
	}
	return h, nil
}

// parseHeader splits "#tb 0: 1/1000" into the stream index and its value.
func parseHeader(line, prefix string) (int, string, error) {
	idx, val, ok := strings.Cut(strings.TrimPrefix(line, prefix), ":")
	i, err := strconv.Atoi(strings.TrimSpace(idx))
	if !ok || err != nil {
		return 0, "", fmt.Errorf("%q is not a %s header", line, strings.TrimSpace(prefix))
	}
	return i, strings.TrimSpace(val), nil
}

// parseDataLine parses `stream, dts, pts, duration, size, hash`, optionally
// followed by `S=N` and N pairs of `size, hash` for the packet's side data.
func parseDataLine(line string) (PacketHash, error) {
	f := strings.Split(line, ",")
	for i := range f {
		f[i] = strings.TrimSpace(f[i])
	}
	if len(f) < 6 {
		return PacketHash{}, fmt.Errorf("%q has %d fields, want at least 6", line, len(f))
	}
	var nums [5]int64
	for i := range nums {
		v, err := strconv.ParseInt(f[i], 10, 64)
		if err != nil {
			return PacketHash{}, fmt.Errorf("%q has a field %q that is not an integer", line, f[i])
		}
		nums[i] = v
	}
	if !isMD5(f[5]) || nums[4] < 0 || nums[0] < 0 {
		return PacketHash{}, fmt.Errorf("%q is not a framemd5 data line", line)
	}
	ph := PacketHash{
		Stream: int(nums[0]), DTS: nums[1], PTS: nums[2], Duration: nums[3], Size: int(nums[4]), MD5: f[5],
	}
	if len(f) == 6 {
		return ph, nil
	}

	count, ok := strings.CutPrefix(f[6], "S=")
	n, err := strconv.Atoi(count)
	if !ok || err != nil || n < 1 {
		return PacketHash{}, fmt.Errorf("%q has %q after the hash, want S=N with N of at least 1", line, f[6])
	}
	if len(f) != 7+2*n {
		return PacketHash{}, fmt.Errorf("%q declares %d side data entries but has %d fields after S=%d, want %d", line, n, len(f)-7, n, 2*n)
	}
	for i := 0; i < n; i++ {
		size, err := strconv.Atoi(f[7+2*i])
		sum := f[8+2*i]
		if err != nil || size < 0 || !isMD5(sum) {
			return PacketHash{}, fmt.Errorf("%q has a malformed side data entry %q, %q", line, f[7+2*i], sum)
		}
		ph.SideData = append(ph.SideData, SideDataHash{Size: size, MD5: sum})
	}
	return ph, nil
}

func isMD5(s string) bool {
	if len(s) != 32 {
		return false
	}
	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

func tail(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 200 {
		s = s[len(s)-200:]
	}
	return s
}

// DecodeOptions shapes a decode. Seeking options go before the input, as ffmpeg
// treats them, and the end options after it.
type DecodeOptions struct {
	CopyTS   bool     // -copyts
	SeekFrom *float64 // -ss
	SeekEOF  *float64 // -sseof, negative seconds
	To       *float64 // -to
	Duration *float64 // -t
	// Maps become -map options, such as "0:v:0" or "0:a?".
	Maps []string
	// MaxVideoFrames becomes -frames:v when positive.
	MaxVideoFrames int
}

// Decoded is the framemd5 of a decode.
type Decoded struct {
	Hashes
	// Errors is every non-empty stderr line except "Last message repeated".
	Errors []string
}

// DecodeHashes decodes path, with no codec option at all, and hashes every
// frame. The decoder's own complaints come back in Errors rather than failing
// the call; a non-zero exit does fail it.
func DecodeHashes(ctx context.Context, p proc.Piper, path string, o DecodeOptions) (Decoded, error) {
	argv := []string{"ffmpeg", "-nostdin", "-v", "error"}
	if o.CopyTS {
		argv = append(argv, "-copyts")
	}
	if o.SeekFrom != nil {
		argv = append(argv, "-ss", seconds(*o.SeekFrom))
	}
	if o.SeekEOF != nil {
		argv = append(argv, "-sseof", seconds(*o.SeekEOF))
	}
	argv = append(argv, "-i", path)
	if o.To != nil {
		argv = append(argv, "-to", seconds(*o.To))
	}
	if o.Duration != nil {
		argv = append(argv, "-t", seconds(*o.Duration))
	}
	if o.MaxVideoFrames > 0 {
		argv = append(argv, "-frames:v", strconv.Itoa(o.MaxVideoFrames))
	}
	for _, m := range o.Maps {
		argv = append(argv, "-map", m)
	}
	argv = append(argv, "-f", "framemd5", "-")

	h, stderr, err := pipeFramemd5(ctx, p, argv, path)
	if err != nil {
		return Decoded{}, err
	}
	d := Decoded{Hashes: h}
	for _, line := range strings.Split(string(stderr), "\n") {
		line = strings.TrimSpace(line)
		if line != "" && !strings.HasPrefix(line, "Last message repeated") {
			d.Errors = append(d.Errors, line)
		}
	}
	return d, nil
}

func seconds(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }
