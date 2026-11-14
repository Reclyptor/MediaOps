package frames

import (
	"bytes"
	"context"
	"crypto/md5"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestStreamBytesCopiesStdout(t *testing.T) {
	piper := &fakePiper{t: t, stdout: []byte("raw sample bytes")}
	var out bytes.Buffer
	n, err := StreamBytes(context.Background(), piper, "/x/a.mkv", 2, &out)
	if err != nil || n != 16 || out.String() != "raw sample bytes" {
		t.Fatalf("n=%d out=%q err=%v", n, out.String(), err)
	}
	want := []string{"ffmpeg", "-nostdin", "-v", "error", "-i", "/x/a.mkv", "-map", "0:2", "-c", "copy", "-f", "rawvideo", "-"}
	if strings.Join(piper.argv, " ") != strings.Join(want, " ") {
		t.Fatalf("argv = %q", piper.argv)
	}
}

type failWriter struct{}

func (failWriter) Write([]byte) (int, error) { return 0, errors.New("disk full") }

func TestStreamBytesFailures(t *testing.T) {
	piper := &fakePiper{t: t, exit: 1, stderr: []byte("Stream map '0:9' matches no streams\n")}
	if _, err := StreamBytes(context.Background(), piper, "/x/a.mkv", 9, io.Discard); err == nil || !strings.Contains(err.Error(), "matches no streams") {
		t.Fatalf("err = %v", err)
	}
	piper = &fakePiper{t: t, stdout: []byte("x")}
	if _, err := StreamBytes(context.Background(), piper, "/x/a.mkv", 1, failWriter{}); err == nil || !strings.Contains(err.Error(), "disk full") {
		t.Fatalf("err = %v", err)
	}
}

// A remux re-frames PCM packets but never changes the samples.
func TestStreamBytesOfPCMSurviveARemux(t *testing.T) {
	src, remux := buildSample(t)
	sum := func(path string) ([16]byte, int64) {
		t.Helper()
		h := md5.New()
		n, err := StreamBytes(context.Background(), realExec, path, 1, h)
		if err != nil {
			t.Fatal(err)
		}
		return [16]byte(h.Sum(nil)), n
	}
	a, an := sum(src)
	b, bn := sum(remux)
	if an == 0 || an != bn || a != b {
		t.Fatalf("source %d bytes %x, remux %d bytes %x", an, a, bn, b)
	}
}
