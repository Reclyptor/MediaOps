package frames

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reclyptor/mediaops/kit/ffprobe"
	"github.com/reclyptor/mediaops/kit/proc"
)

// These tests run the real ffmpeg and mkvmerge and are part of the default
// build: a missing binary fails them rather than skipping, because a skipped
// proof of the stream-copy checks is a proof nobody ran.

var realExec = &proc.Exec{}

// synth runs a tool directly to build test data. This is the only place in
// this package where an encoder runs: the test MKV needs lavfi test sources
// encoded into it, and no code outside _test files can reach it.
func synth(t *testing.T, name string, args ...string) {
	t.Helper()
	if _, err := exec.LookPath(name); err != nil {
		t.Fatalf("%s is required to run these tests: %v", name, err)
	}
	out, err := exec.Command(name, args...).CombinedOutput()
	if ee, ok := err.(*exec.ExitError); ok && ee.ExitCode() == 1 && strings.HasPrefix(name, "mkv") {
		err = nil // mkvtoolnix exits 1 for warnings
	}
	if err != nil {
		t.Fatalf("%s %q: %v\n%s", name, args, err, out)
	}
}

// buildSample writes a three second 320x180 MKV with an h264 video stream
// (access unit delimiters on, one closed GOP per second), a PCM audio stream
// and an SRT subtitle, and a copy of it remuxed by mkvmerge.
func buildSample(t *testing.T) (src, remux string) {
	t.Helper()
	dir := t.TempDir()
	srt := filepath.Join(dir, "subs.srt")
	body := "1\n00:00:00,500 --> 00:00:01,500\nHello\n\n2\n00:00:02,000 --> 00:00:02,800\nWorld\n"
	if err := os.WriteFile(srt, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	src = filepath.Join(dir, "src.mkv")
	synth(t, "ffmpeg", "-nostdin", "-v", "error",
		"-f", "lavfi", "-i", "testsrc2=duration=3:size=320x180:rate=24",
		"-f", "lavfi", "-i", "sine=frequency=440:duration=3",
		"-i", srt,
		"-map", "0:v", "-map", "1:a", "-map", "2:s",
		"-c:v", "libx264", "-preset", "ultrafast", "-pix_fmt", "yuv420p",
		"-x264-params", "aud=1:keyint=24:open-gop=0",
		"-c:a", "pcm_s16le", "-c:s", "srt", src)
	remux = filepath.Join(dir, "remux.mkv")
	synth(t, "mkvmerge", "-o", remux, src)
	return src, remux
}

func TestCopyHashesOfARealFile(t *testing.T) {
	src, _ := buildSample(t)
	h, err := CopyHashes(context.Background(), realExec, src)
	if err != nil {
		t.Fatal(err)
	}
	if got := len(h.Packets[0]); got != 72 {
		t.Errorf("video packets = %d, want 72", got)
	}
	if len(h.Packets[1]) == 0 || len(h.Packets[2]) != 2 {
		t.Errorf("audio packets = %d, subtitle packets = %d, want some and 2", len(h.Packets[1]), len(h.Packets[2]))
	}
	for i, mt := range []string{"video", "audio", "subtitle"} {
		if h.MediaType[i] != mt {
			t.Errorf("media type %d = %q, want %q", i, h.MediaType[i], mt)
		}
		if tb := h.TimeBase[i]; tb != (Rational{1, 1000}) {
			t.Errorf("time base %d = %v, want 1/1000 as Matroska stores it", i, tb)
		}
	}
}

func TestCopyHashesNonZeroExitOnAMissingFile(t *testing.T) {
	_, err := CopyHashes(context.Background(), realExec, filepath.Join(t.TempDir(), "missing.mkv"))
	if err == nil || !strings.Contains(err.Error(), "missing.mkv") {
		t.Fatalf("err = %v", err)
	}
}

func TestPayloadHashesMatchAStreamCopyOnceAUDsAreDropped(t *testing.T) {
	src, remux := buildSample(t)
	ctx := context.Background()

	digests := func(path string, filter NALFilter) ([][16]byte, int) {
		t.Helper()
		h, err := CopyHashes(ctx, realExec, path)
		if err != nil {
			t.Fatal(err)
		}
		streams, err := ffprobe.Streams(ctx, realExec, path)
		if err != nil {
			t.Fatal(err)
		}
		if streams[0].NALLengthSize != 4 {
			t.Fatalf("NALLengthSize = %d, want 4", streams[0].NALLengthSize)
		}
		sizes := make([]int, len(h.Packets[0]))
		for i, p := range h.Packets[0] {
			sizes[i] = p.Size
		}
		d, removed, err := PayloadHashes(ctx, realExec, path, 0, sizes, filter, streams[0].NALLengthSize)
		if err != nil {
			t.Fatal(err)
		}
		if len(d) != len(sizes) {
			t.Fatalf("%d digests for %d packets", len(d), len(sizes))
		}
		return d, removed
	}

	srcRaw, removed := digests(src, KeepAll)
	if removed != 0 {
		t.Fatalf("KeepAll removed %d units", removed)
	}
	srcDigests, removed := digests(src, DropH264AUD)
	if removed != 72 {
		t.Fatalf("removed %d AUDs from the source, want one per packet (72)", removed)
	}
	remuxRaw, _ := digests(remux, KeepAll)
	remuxDigests, removed := digests(remux, DropH264AUD)
	if removed != 0 {
		t.Fatalf("removed %d AUDs from the mkvmerge copy, which should carry none", removed)
	}

	if len(srcDigests) != len(remuxDigests) {
		t.Fatalf("%d source packets, %d remuxed", len(srcDigests), len(remuxDigests))
	}
	for i := range srcDigests {
		if srcDigests[i] != remuxDigests[i] {
			t.Fatalf("packet %d differs after the AUDs are dropped", i)
		}
	}
	if srcRaw[0] == remuxRaw[0] {
		t.Error("the unfiltered packets match, so the filter proves nothing")
	}
}

func TestDecodeHashesOfARealFile(t *testing.T) {
	src, _ := buildSample(t)
	ctx := context.Background()
	copyHashes, err := CopyHashes(ctx, realExec, src)
	if err != nil {
		t.Fatal(err)
	}
	packets := len(copyHashes.Packets[0])

	all, err := DecodeHashes(ctx, realExec, src, DecodeOptions{Maps: []string{"0:v:0"}})
	if err != nil {
		t.Fatal(err)
	}
	if got := len(all.Packets[0]); got != packets {
		t.Errorf("decoded %d frames from %d packets of a closed-GOP file", got, packets)
	}
	if len(all.Errors) != 0 {
		t.Errorf("Errors = %q on a clean file", all.Errors)
	}

	capped, err := DecodeHashes(ctx, realExec, src, DecodeOptions{Maps: []string{"0:v:0"}, MaxVideoFrames: 5})
	if err != nil {
		t.Fatal(err)
	}
	if got := len(capped.Packets[0]); got != 5 {
		t.Errorf("MaxVideoFrames 5 gave %d frames", got)
	}

	from, to := 1.0, 2.0
	firstPTS := func(o DecodeOptions) (int, float64) {
		t.Helper()
		o.Maps = []string{"0:v:0"}
		d, err := DecodeHashes(ctx, realExec, src, o)
		if err != nil {
			t.Fatal(err)
		}
		tb := d.TimeBase[0]
		return len(d.Packets[0]), float64(d.Packets[0][0].PTS) * float64(tb.Num) / float64(tb.Den)
	}
	n, first := firstPTS(DecodeOptions{SeekFrom: &from, To: &to})
	if n <= 0 || n >= packets {
		t.Errorf("a seek of 1s to 2s decoded %d of %d frames", n, packets)
	}
	if first > 0.1 {
		t.Errorf("without CopyTS the first frame is at %vs, want about 0", first)
	}
	_, first = firstPTS(DecodeOptions{SeekFrom: &from, CopyTS: true})
	if first < 0.9 {
		t.Errorf("with CopyTS the first frame is at %vs, want about 1", first)
	}
}

func TestGrayFrameOfARealFile(t *testing.T) {
	src, _ := buildSample(t)
	got, err := GrayFrame(context.Background(), realExec, src, 1, 32, 18)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 32*18 {
		t.Fatalf("got %d bytes, want %d", len(got), 32*18)
	}
	if bytes.Count(got, got[:1]) == len(got) {
		t.Error("the frame is one flat shade; testsrc2 is not")
	}
}

func TestPNGOfARealFile(t *testing.T) {
	src, _ := buildSample(t)
	out := filepath.Join(t.TempDir(), "frame.png")
	if err := PNG(context.Background(), realExec, src, 1, "scale=160:90", out); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(out)
	if err != nil || !bytes.HasPrefix(data, []byte("\x89PNG\r\n\x1a\n")) {
		t.Fatalf("out is not a PNG: %v", err)
	}

	err = PNG(context.Background(), realExec, src, 1, "", out)
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("err = %v, want a refusal to overwrite", err)
	}
	after, _ := os.ReadFile(out)
	if !bytes.Equal(after, data) {
		t.Fatal("the existing PNG was modified")
	}
}

func TestPNGPastTheEndWritesNothing(t *testing.T) {
	src, _ := buildSample(t)
	out := filepath.Join(t.TempDir(), "late.png")
	if err := PNG(context.Background(), realExec, src, 600, "", out); err == nil {
		t.Fatal("a frame past the end was reported written")
	}
}

// mkvmerge's split leaves the first packets of an AC-3 track with skip-samples
// side data, which ffmpeg 7 and later append to the packet's framemd5 line.
// ffmpeg 6.1 (the image's, and CI's) prints none for the same file, so this
// asserts the shape of whatever side data appears and that the whole document
// parses either way; TestParseFramemd5SideData covers the grammar itself.
func TestCopyHashesParsesRealSideData(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.mkv")
	synth(t, "ffmpeg", "-nostdin", "-v", "error",
		"-f", "lavfi", "-i", "testsrc2=duration=6:size=160x90:rate=24",
		"-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000:duration=6",
		"-c:v", "libx264", "-preset", "ultrafast", "-pix_fmt", "yuv420p", "-x264-params", "keyint=24",
		"-c:a", "ac3", "-b:a", "192k", "-ac", "2", src)
	part := filepath.Join(dir, "part.mkv")
	synth(t, "mkvmerge", "-o", part, "--split", "parts:00:00:02-00:00:04", src)

	h, err := CopyHashes(context.Background(), realExec, part)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range h.Packets[1] {
		for _, sd := range p.SideData {
			if sd.Size <= 0 || len(sd.MD5) != 32 {
				t.Errorf("side data = %+v", p.SideData)
			}
		}
	}
	if len(h.Packets[0]) == 0 || len(h.Packets[1]) == 0 {
		t.Fatalf("packets per stream: video %d, audio %d", len(h.Packets[0]), len(h.Packets[1]))
	}
}
