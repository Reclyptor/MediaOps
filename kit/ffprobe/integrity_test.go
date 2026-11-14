package ffprobe

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/reclyptor/mediaops/kit/proc"
)

func TestIntegrityCleanFile(t *testing.T) {
	r := &fakeRunner{res: proc.Result{Stdout: []byte(`{"format": {"duration": "1432.768000"}}`)}}
	d, warnings, err := Integrity(context.Background(), r, "/x/a.mkv")
	if err != nil || d != 1432.768 || len(warnings) != 0 {
		t.Fatalf("= %v, %q, %v", d, warnings, err)
	}
	if want := []string{"ffprobe", "-v", "error", "-show_entries", "format=duration", "-of", "json", "/x/a.mkv"}; !reflect.DeepEqual(r.argv, want) {
		t.Fatalf("argv = %q", r.argv)
	}
}

func TestIntegrityReturnsEveryStderrLine(t *testing.T) {
	r := &fakeRunner{res: proc.Result{
		Stdout: []byte(`{"format": {"duration": "10.0"}}`),
		Stderr: []byte("[matroska,webm] Read error at pos. 100\n\n  [matroska,webm] Seek failed  \n"),
	}}
	d, warnings, err := Integrity(context.Background(), r, "/x/a.mkv")
	if err != nil || d != 10 {
		t.Fatalf("= %v, %v", d, err)
	}
	want := []string{"[matroska,webm] Read error at pos. 100", "[matroska,webm] Seek failed"}
	if !reflect.DeepEqual(warnings, want) {
		t.Fatalf("warnings = %q", warnings)
	}
}

func TestIntegrityErrors(t *testing.T) {
	for name, res := range map[string]proc.Result{
		"non-zero exit": {ExitCode: 1, Stderr: []byte("moov atom not found")},
		"no duration":   {Stdout: []byte(`{"format": {}}`)},
		"no format":     {Stdout: []byte(`{}`)},
		"zero duration": {Stdout: []byte(`{"format": {"duration": "0"}}`)},
		"N/A":           {Stdout: []byte(`{"format": {"duration": "N/A"}}`)},
		"not JSON":      {Stdout: []byte(`x`)},
	} {
		t.Run(name, func(t *testing.T) {
			d, w, err := Integrity(context.Background(), &fakeRunner{res: res}, "/x/a.mkv")
			if err == nil || !strings.Contains(err.Error(), "/x/a.mkv") {
				t.Fatalf("err = %v", err)
			}
			if d != 0 || w != nil {
				t.Fatalf("returned %v, %q alongside an error", d, w)
			}
		})
	}
}
