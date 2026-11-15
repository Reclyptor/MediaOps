package dhash

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/reclyptor/mediaops/kit/proc"
)

// synth runs ffmpeg directly to build lavfi test media. Like the helper in
// kit/frames, it lives in a _test file so no shipped code can reach an encoder.
func synth(t *testing.T, args ...string) {
	t.Helper()
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Fatalf("ffmpeg is required to run these tests: %v", err)
	}
	if out, err := exec.Command("ffmpeg", append([]string{"-nostdin", "-v", "error"}, args...)...).CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg %q: %v\n%s", args, err, out)
	}
}

func distinct(h []uint64) int {
	seen := map[uint64]bool{}
	for _, x := range h {
		seen[x] = true
	}
	return len(seen)
}

func TestSampleRealStillAndMotion(t *testing.T) {
	dir := t.TempDir()
	still := filepath.Join(dir, "still.mkv")
	motion := filepath.Join(dir, "motion.mkv")
	synth(t, "-f", "lavfi", "-i", "color=c=red:size=160x90:rate=24:duration=4",
		"-c:v", "libx264", "-preset", "ultrafast", "-pix_fmt", "yuv420p", still)
	synth(t, "-f", "lavfi", "-i", "testsrc2=duration=4:size=160x90:rate=24",
		"-c:v", "libx264", "-preset", "ultrafast", "-pix_fmt", "yuv420p", motion)

	ctx := context.Background()
	stillHashes, err := Sample(ctx, &proc.Exec{}, still, 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	motionHashes, err := Sample(ctx, &proc.Exec{}, motion, 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	for name, h := range map[string][]uint64{"still": stillHashes, "motion": motionHashes} {
		if len(h) < 3 || len(h) > 5 {
			t.Errorf("%s: %d frames from a 4 second clip at 1 fps", name, len(h))
		}
	}
	if n := distinct(stillHashes); n != 1 {
		t.Errorf("a still gave %d distinct hashes, want 1", n)
	}
	if n := distinct(motionHashes); n < 2 {
		t.Errorf("testsrc2 gave %d distinct hashes, want several", n)
	}

	from := 2.0
	later, err := Sample(ctx, &proc.Exec{}, motion, 1, &from)
	if err != nil {
		t.Fatal(err)
	}
	if len(later) == 0 || len(later) >= len(motionHashes) {
		t.Errorf("seeking to 2s gave %d frames, whole file gave %d", len(later), len(motionHashes))
	}
}

func TestSampleRealUndecodableFileIsAnError(t *testing.T) {
	junk := filepath.Join(t.TempDir(), "junk.mkv")
	if err := os.WriteFile(junk, []byte("this is not a matroska file at all"), 0o644); err != nil {
		t.Fatal(err)
	}
	if h, err := Sample(context.Background(), &proc.Exec{}, junk, 1, nil); err == nil {
		t.Errorf("an undecodable file gave %d hashes and no error", len(h))
	}
	if _, err := Sample(context.Background(), &proc.Exec{}, filepath.Join(t.TempDir(), "missing.mkv"), 1, nil); err == nil {
		t.Error("a missing file gave no error")
	}
}
