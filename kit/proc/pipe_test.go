package proc

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

func TestPipeStreamsLargeStdout(t *testing.T) {
	var n int64
	stderr, code, err := helper(t, "megabytes").Pipe(context.Background(), []string{"ffprobe"}, func(r io.Reader) error {
		var err error
		n, err = io.Copy(io.Discard, r)
		return err
	})
	if err != nil || code != 0 {
		t.Fatalf("Pipe = %d, %v", code, err)
	}
	if n != 64<<20 {
		t.Fatalf("consumed %d bytes, want %d", n, 64<<20)
	}
	if len(stderr) != 0 {
		t.Fatalf("stderr = %q, want none", stderr)
	}
}

func TestPipeConsumerErrorKillsChild(t *testing.T) {
	boom := errors.New("boom")
	start := time.Now()
	_, _, err := helper(t, "endless").Pipe(context.Background(), []string{"ffprobe"}, func(r io.Reader) error {
		if _, err := io.ReadFull(r, make([]byte, 10)); err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the consumer's error", err)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatal("child outlived a failed consumer")
	}
}

func TestPipeEarlyReturnDrains(t *testing.T) {
	_, code, err := helper(t, "megabytes").Pipe(context.Background(), []string{"ffprobe"}, func(r io.Reader) error {
		_, err := io.ReadFull(r, make([]byte, 4))
		return err
	})
	if err != nil || code != 0 {
		t.Fatalf("Pipe = %d, %v; a child whose output was drained must exit 0", code, err)
	}
}

func TestPipeStderrTailIsCapped(t *testing.T) {
	stderr, code, err := helper(t, "noisy").Pipe(context.Background(), []string{"ffprobe"}, func(r io.Reader) error {
		_, err := io.Copy(io.Discard, r)
		return err
	})
	if err != nil || code != 0 {
		t.Fatalf("Pipe = %d, %v", code, err)
	}
	if len(stderr) != stderrTailMax {
		t.Fatalf("kept %d bytes of stderr, want %d", len(stderr), stderrTailMax)
	}
	if !bytes.HasSuffix(stderr, []byte("THE END\n")) {
		t.Fatalf("stderr tail ends %q, want the child's last line", stderr[len(stderr)-20:])
	}
}

func TestPipeReportsExitInsteadOfError(t *testing.T) {
	_, code, err := helper(t, "exit3").Pipe(context.Background(), []string{"ffprobe"}, func(r io.Reader) error {
		_, err := io.Copy(io.Discard, r)
		return err
	})
	if err != nil || code != 3 {
		t.Fatalf("Pipe = %d, %v, want exit 3 and no error", code, err)
	}
}

func TestPipeRefusesBeforeStarting(t *testing.T) {
	called := false
	_, _, err := (&Exec{Resolve: func(string) (string, error) {
		t.Fatal("a refused argv must not be resolved")
		return "", nil
	}}).Pipe(context.Background(), []string{"ffmpeg", "-i", "a", "b.mkv"}, func(io.Reader) error {
		called = true
		return nil
	})
	if !errors.Is(err, ErrRefused) {
		t.Fatalf("err = %v, want ErrRefused", err)
	}
	if called {
		t.Fatal("consume ran for a refused argv")
	}
}

func TestPipeCancelTerminatesChild(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, _, err := helper(t, "sleep").Pipe(ctx, []string{"ffprobe"}, func(r io.Reader) error {
		_, err := io.Copy(io.Discard, r)
		return err
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want deadline exceeded", err)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatal("child outlived its context")
	}
}

func TestPipeStdoutIsTheChildsOwn(t *testing.T) {
	var got strings.Builder
	_, _, err := helper(t, "lines").Pipe(context.Background(), []string{"ffprobe"}, func(r io.Reader) error {
		_, err := io.Copy(&got, r)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.String() != "one\ntwo\xff\r\n" {
		t.Fatalf("stdout = %q", got.String())
	}
}
