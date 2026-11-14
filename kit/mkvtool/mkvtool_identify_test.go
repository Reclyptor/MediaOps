package mkvtool

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/reclyptor/mediaops/kit/proc"
)

func TestIdentify(t *testing.T) {
	r := &fakeRunner{replies: map[string]proc.Result{"mkvmerge": {ExitCode: 1, Stdout: []byte(`{
		"container": {"properties": {"title": "Show S01"}},
		"tracks": [
			{"id": 0, "type": "video", "properties": {"uid": 1, "default_track": true}},
			{"id": 1, "type": "audio", "properties": {"uid": 18446744073709551557, "track_name": "English", "language": "eng"}},
			{"id": 2, "type": "subtitles", "properties": {"language": "jpn"}}]}`)}}}
	got, err := Identify(context.Background(), r, "/x/a.mkv")
	if err != nil {
		t.Fatal(err)
	}
	want := Identification{Title: "Show S01", Tracks: []Track{
		{ID: 0, Type: "video", UID: "1", Default: true},
		{ID: 1, Type: "audio", UID: "18446744073709551557", Name: "English", Language: "eng"},
		{ID: 2, Type: "subtitles", Language: "jpn"},
	}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
	if !reflect.DeepEqual(r.calls, [][]string{{"mkvmerge", "-J", "/x/a.mkv"}}) {
		t.Fatalf("calls = %q", r.calls)
	}
}

func TestIdentifyFailClosed(t *testing.T) {
	tests := []struct {
		name    string
		res     proc.Result
		wantErr string
	}{
		{"exit 2", proc.Result{ExitCode: 2, Stdout: []byte("Error: no such file")}, "no such file"},
		{"signal", proc.Result{ExitCode: -1}, "exit -1"},
		{"bad JSON", proc.Result{Stdout: []byte("garbage")}, "unreadable JSON"},
		{"track without id", proc.Result{Stdout: []byte(`{"tracks": [{"type": "audio"}]}`)}, "without an id or type"},
		{"track without type", proc.Result{Stdout: []byte(`{"tracks": [{"id": 0}]}`)}, "without an id or type"},
		{"string uid", proc.Result{Stdout: []byte(`{"tracks": [{"id": 0, "type": "audio", "properties": {"uid": "abc"}}]}`)}, "unreadable JSON"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := &fakeRunner{replies: map[string]proc.Result{"mkvmerge": tc.res}}
			_, err := Identify(context.Background(), r, "f.mkv")
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %v, want it to contain %q", err, tc.wantErr)
			}
		})
	}
}

func TestRemux(t *testing.T) {
	dst := filepath.Join(t.TempDir(), "out.mkv")
	r := &writingRunner{fakeRunner: fakeRunner{replies: map[string]proc.Result{"mkvmerge": {ExitCode: 1}}}, write: dst}
	if err := Remux(context.Background(), r, "/x/in.avi", dst); err != nil {
		t.Fatal(err)
	}
	want := []string{"mkvmerge", "-q", "-o", dst, "/x/in.avi"}
	if !reflect.DeepEqual(r.calls, [][]string{want}) {
		t.Fatalf("calls = %q, want %q", r.calls, want)
	}
	if err := proc.Check(want); err != nil {
		t.Fatalf("the guard refuses the remux: %v", err)
	}
}

// writingRunner creates a file when mkvmerge runs, as a successful remux does.
type writingRunner struct {
	fakeRunner
	write string
}

func (w *writingRunner) Run(ctx context.Context, argv ...string) (proc.Result, error) {
	res, err := w.fakeRunner.Run(ctx, argv...)
	if w.write != "" && res.ExitCode < 2 {
		if werr := os.WriteFile(w.write, []byte("x"), 0o644); werr != nil {
			return res, werr
		}
	}
	return res, err
}

func TestRemuxFailures(t *testing.T) {
	tests := []struct {
		name    string
		res     proc.Result
		write   bool
		wantErr string
	}{
		{"exit 2 carries the output", proc.Result{ExitCode: 2, Stdout: []byte("Error: unsupported container")}, true, "unsupported container"},
		{"signal", proc.Result{ExitCode: -1}, true, "exit -1"},
		{"no output file", proc.Result{}, false, "left no file"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dst := filepath.Join(t.TempDir(), "out.mkv")
			r := &writingRunner{fakeRunner: fakeRunner{replies: map[string]proc.Result{"mkvmerge": tc.res}}}
			if tc.write {
				r.write = dst
			}
			err := Remux(context.Background(), r, "/x/in.avi", dst)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %v, want it to contain %q", err, tc.wantErr)
			}
		})
	}
	dst := t.TempDir()
	r := &fakeRunner{replies: map[string]proc.Result{}}
	if err := Remux(context.Background(), r, "/x/in.avi", dst); err == nil {
		t.Fatal("a directory at dst is not a remuxed file")
	}
}

func TestRemuxRunnerError(t *testing.T) {
	if err := Remux(context.Background(), &errRunner{}, "a", "b"); err == nil || !strings.Contains(err.Error(), "refused") {
		t.Fatalf("err = %v", err)
	}
}

func TestExtractTags(t *testing.T) {
	tests := []struct {
		name    string
		out     string
		tags    int
		wantErr string
	}{
		{"empty output", "", 0, ""},
		{"whitespace", " \n", 0, ""},
		{"BOM and a tag", "\xef\xbb\xbf<?xml version=\"1.0\"?><Tags><Tag><Simple><Name>A</Name></Simple></Tag></Tags>", 1, ""},
		{"not XML", "Error: x", 0, "unreadable tags"},
		{"wrong root", "<Chapters/>", 0, "not <Tags>"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := &fakeRunner{replies: map[string]proc.Result{"mkvextract": {Stdout: []byte(tc.out)}}}
			root, err := extractTags(context.Background(), r, "f.mkv")
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want it to contain %q", err, tc.wantErr)
				}
				return
			}
			if err != nil || len(root.Children) != tc.tags {
				t.Fatalf("got %d tags, %v; want %d", len(root.Children), err, tc.tags)
			}
		})
	}
	r := &fakeRunner{replies: map[string]proc.Result{"mkvextract": {ExitCode: 2, Stdout: []byte("Error: no")}}}
	if _, err := extractTags(context.Background(), r, "f.mkv"); err == nil || !strings.Contains(err.Error(), "exit 2") {
		t.Fatalf("err = %v", err)
	}
}
