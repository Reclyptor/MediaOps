package frames

import (
	"context"
	"io"
	"testing"
)

// The fakes run every argv through proc.Check and fail the test when it is
// refused, so driving each entry point is the guard test.
func TestEveryArgvPassesTheGuard(t *testing.T) {
	ctx := context.Background()
	from, eof, to, dur := 10.0, -30.0, 20.0, 5.5

	piper := &fakePiper{t: t, stdout: []byte(sampleFramemd5)}
	CopyHashes(ctx, piper, "/x/a.mkv")
	for _, o := range []DecodeOptions{
		{},
		{CopyTS: true, SeekFrom: &from, To: &to, Maps: []string{"0:v:0", "0:a?"}},
		{SeekEOF: &eof, Duration: &dur, MaxVideoFrames: 100, Maps: []string{"0:v:0"}},
	} {
		DecodeHashes(ctx, piper, "/x/a.mkv", o)
	}
	for _, f := range []struct {
		filter NALFilter
		n      int
	}{{KeepAll, 0}, {DropH264AUD, 4}, {DropHEVCAUD, 4}} {
		PayloadHashes(ctx, &fakePiper{t: t}, "/x/a.mkv", 3, nil, f.filter, f.n)
	}

	StreamBytes(ctx, &fakePiper{t: t}, "/x/a.mkv", 1, io.Discard)

	runner := &fakeRunner{t: t}
	GrayFrame(ctx, runner, "/x/a.mkv", 1.25, 64, 36)
	PNG(ctx, runner, "/x/a.mkv", 90.5, "", t.TempDir()+"/a.png")
	PNG(ctx, runner, "/x/a.mkv", 90.5, "scale=-2:360,format=rgb24", t.TempDir()+"/b.png")
	if !runner.called || piper.argv == nil {
		t.Fatal("an entry point never ran")
	}
}
