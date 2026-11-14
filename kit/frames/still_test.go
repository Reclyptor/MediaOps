package frames

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reclyptor/mediaops/kit/proc"
)

func TestGrayFrameChecksTheByteCount(t *testing.T) {
	r := &fakeRunner{t: t, res: proc.Result{Stdout: make([]byte, 10)}}
	_, err := GrayFrame(context.Background(), r, "/x/a.mkv", 12.5, 4, 3)
	if err == nil || !strings.Contains(err.Error(), "10 bytes") {
		t.Fatalf("err = %v", err)
	}
	r.res.Stdout = make([]byte, 12)
	if got, err := GrayFrame(context.Background(), r, "/x/a.mkv", 12.5, 4, 3); err != nil || len(got) != 12 {
		t.Fatalf("= %d bytes, %v", len(got), err)
	}
}

func TestGrayFrameRejectsASizeAndAFailedExit(t *testing.T) {
	r := &fakeRunner{t: t}
	if _, err := GrayFrame(context.Background(), r, "/x/a.mkv", 0, 0, 3); err == nil || r.called {
		t.Fatalf("err = %v, ran = %v", err, r.called)
	}
	r.res = proc.Result{ExitCode: 1, Stderr: []byte("no such file\n")}
	if _, err := GrayFrame(context.Background(), r, "/x/a.mkv", 0, 4, 3); err == nil || !strings.Contains(err.Error(), "no such file") {
		t.Fatalf("err = %v", err)
	}
}

func TestPNGRefusesBeforeRunning(t *testing.T) {
	dir := t.TempDir()
	existing := filepath.Join(dir, "a.png")
	if err := os.WriteFile(existing, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link.png")
	if err := os.Symlink(filepath.Join(dir, "nowhere"), link); err != nil {
		t.Fatal(err)
	}
	for name, out := range map[string]string{
		"existing":         existing,
		"dangling symlink": link,
		"not png":          filepath.Join(dir, "a.jpg"),
	} {
		t.Run(name, func(t *testing.T) {
			r := &fakeRunner{t: t}
			if err := PNG(context.Background(), r, "/x/a.mkv", 1, "", out); err == nil {
				t.Fatal("accepted")
			}
			if r.called {
				t.Fatal("ffmpeg ran")
			}
		})
	}
	if b, _ := os.ReadFile(existing); string(b) != "keep" {
		t.Fatal("existing file touched")
	}
}
