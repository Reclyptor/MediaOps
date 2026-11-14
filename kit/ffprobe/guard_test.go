package ffprobe

import (
	"context"
	"testing"

	"github.com/reclyptor/mediaops/kit/proc"
)

// guardRunner answers every call with an empty document and runs each argv through
// the real guard.
type guardRunner struct {
	t *testing.T
}

func (g guardRunner) Run(_ context.Context, argv ...string) (proc.Result, error) {
	g.t.Helper()
	if err := proc.Check(argv); err != nil {
		g.t.Errorf("guard refused %q: %v", argv, err)
	}
	return proc.Result{Stdout: []byte(`{}`)}, nil
}

func (g guardRunner) Stream(context.Context, []string, func(string)) (int, error) {
	panic("not used")
}

func TestEveryArgvPassesTheGuard(t *testing.T) {
	ctx, r := context.Background(), guardRunner{t}
	Duration(ctx, r, "/x/a.mkv")
	Streams(ctx, r, "/x/a.mkv")
	CountFrames(ctx, r, "/x/a.mkv")
	DecodeErrors(ctx, r, "/x/a.mkv")
	Probe(ctx, r, "/x/a.mkv")
	Chapters(ctx, r, "/x/a.mkv")
	Integrity(ctx, r, "/x/a.mkv")
	for _, c := range [][2]string{{"", ""}, {"v:0", ""}, {"", "%+#64"}, {"v:0", "1425.0%+6"}} {
		Packets(ctx, r, "/x/a.mkv", c[0], c[1])
	}
	Keyframes(ctx, r, "/x/a.mkv", 0, 12.5)
	Keyframes(ctx, r, "/x/a.mkv", 1425.25, 1431)
}
