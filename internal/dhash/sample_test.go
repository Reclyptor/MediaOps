package dhash

import (
	"bytes"
	"context"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"

	"github.com/reclyptor/mediaops/kit/proc"
)

// fakePiper feeds canned stdout to consume and records every argv after
// running it through the real guard, so a refused argv fails the test.
type fakePiper struct {
	t      *testing.T
	stdout []byte
	stderr []byte
	exit   int
	argv   []string
}

func (f *fakePiper) Pipe(_ context.Context, argv []string, consume func(io.Reader) error) ([]byte, int, error) {
	f.t.Helper()
	f.argv = argv
	if err := proc.Check(argv); err != nil {
		f.t.Errorf("guard refused %q: %v", argv, err)
	}
	if err := consume(bytes.NewReader(f.stdout)); err != nil {
		return f.stderr, f.exit, err
	}
	return f.stderr, f.exit, nil
}

func TestSampleHashesStreamedFrames(t *testing.T) {
	f, raw := loadFixture(t)
	p := &fakePiper{t: t, stdout: raw}
	from := 1234.5
	got, err := Sample(context.Background(), p, "/x.mkv", 4, &from)
	if err != nil {
		t.Fatal(err)
	}
	if want := parseAll(t, f.Hashes); !reflect.DeepEqual(got, want) {
		t.Errorf("Sample disagrees with the Python hashes: %d frames, want %d", len(got), len(want))
	}
	if want := f.FingerprintArgv[1]; !reflect.DeepEqual(p.argv, want) {
		t.Errorf("argv = %q, want %q", p.argv, want)
	}
}

// oneByteReader returns stdout a byte at a time, so a frame always arrives
// split across reads.
type oneByteReader struct{ r io.Reader }

func (o oneByteReader) Read(b []byte) (int, error) { return o.r.Read(b[:min(1, len(b))]) }

func TestSampleReassemblesFramesAcrossReads(t *testing.T) {
	_, raw := loadFixture(t)
	p := pipeFunc(func(argv []string, consume func(io.Reader) error) ([]byte, int, error) {
		return nil, 0, consume(oneByteReader{bytes.NewReader(raw)})
	})
	got, err := Sample(context.Background(), p, "/x.mkv", 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	if want := Hashes(raw); !reflect.DeepEqual(got, want) {
		t.Errorf("got %d hashes, want %d", len(got), len(want))
	}
}

type pipeFunc func(argv []string, consume func(io.Reader) error) ([]byte, int, error)

func (f pipeFunc) Pipe(_ context.Context, argv []string, consume func(io.Reader) error) ([]byte, int, error) {
	return f(argv, consume)
}

func TestSampleNoFramesIsEmptyNotError(t *testing.T) {
	got, err := Sample(context.Background(), &fakePiper{t: t}, "/x.mkv", 1, nil)
	if err != nil || len(got) != 0 {
		t.Errorf("got %v, %v", got, err)
	}
}

func TestSampleNonZeroExitCarriesStderrTail(t *testing.T) {
	_, raw := loadFixture(t)
	p := &fakePiper{t: t, stdout: raw, exit: 1, stderr: []byte(strings.Repeat("noise\n", 100) + "Invalid data found when processing input\n")}
	got, err := Sample(context.Background(), p, "/x.mkv", 1, nil)
	if err == nil {
		t.Fatalf("a failed decode returned %d hashes and no error", len(got))
	}
	if got != nil {
		t.Errorf("hashes returned alongside an error: %d", len(got))
	}
	for _, want := range []string{"/x.mkv", "exited 1", "Invalid data found when processing input"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("err = %q, missing %q", err, want)
		}
	}
	if len(err.Error()) > 400 {
		t.Errorf("stderr tail is not capped: %d bytes", len(err.Error()))
	}
}

func TestSamplePipeErrorIsWrapped(t *testing.T) {
	boom := errors.New("boom")
	p := pipeFunc(func([]string, func(io.Reader) error) ([]byte, int, error) { return nil, -1, boom })
	if _, err := Sample(context.Background(), p, "/x.mkv", 1, nil); !errors.Is(err, boom) {
		t.Errorf("err = %v", err)
	}
}

func TestSampleReadErrorStopsTheStream(t *testing.T) {
	boom := errors.New("read failed")
	p := pipeFunc(func(_ []string, consume func(io.Reader) error) ([]byte, int, error) {
		return nil, 0, consume(io.MultiReader(bytes.NewReader(make([]byte, FrameSize)), errReader{boom}))
	})
	if _, err := Sample(context.Background(), p, "/x.mkv", 1, nil); !errors.Is(err, boom) {
		t.Errorf("err = %v", err)
	}
}

type errReader struct{ err error }

func (e errReader) Read([]byte) (int, error) { return 0, e.err }
