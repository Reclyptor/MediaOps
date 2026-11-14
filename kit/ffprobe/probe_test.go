package ffprobe

import (
	"context"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/reclyptor/mediaops/kit/proc"
)

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestProbeRecordedFile(t *testing.T) {
	r := &fakeRunner{res: proc.Result{Stdout: fixture(t, "fate-apocrypha-s01d01.json")}}
	info, err := Probe(context.Background(), r, "/x/a.mkv")
	if err != nil {
		t.Fatal(err)
	}
	wantArgv := []string{"ffprobe", "-v", "error", "-show_format", "-show_streams", "-show_chapters", "-of", "json", "/x/a.mkv"}
	if !reflect.DeepEqual(r.argv, wantArgv) {
		t.Fatalf("argv = %q", r.argv)
	}

	f := info.Format
	if f.Size != 39094659279 || f.Duration != 8550.046 || f.Title != "Fate/Apocrypha" {
		t.Errorf("format = %+v", f)
	}
	if f.Tags["encoder"] == "" {
		t.Errorf("tags = %v, want lower-cased keys", f.Tags)
	}
	if len(info.Streams) != 5 || len(info.Chapters) != 26 {
		t.Fatalf("%d streams, %d chapters", len(info.Streams), len(info.Chapters))
	}
	v := info.Streams[0]
	if v.CodecName != "h264" || v.TimeBase != "1/1000" || v.NALLengthSize != 4 {
		t.Errorf("video stream = %+v", v)
	}
	if a := info.Streams[1]; a.NALLengthSize != 0 || a.TimeBase != "1/1000" || a.SampleRate != 48000 {
		t.Errorf("audio stream = %+v", a)
	}
	if c := info.Chapters[0]; c.Start != 0 || c.End != 254.587667 || c.Title != "Chapter 01" {
		t.Errorf("chapter 0 = %+v", c)
	}
}

func probeJSON(format string, extra string) string {
	return `{"streams": [], "format": ` + format + extra + `}`
}

func TestProbeFailsClosed(t *testing.T) {
	tests := []struct {
		name string
		out  string
		want string
	}{
		{"no size", probeJSON(`{"duration": "5.0"}`, ""), "no size"},
		{"zero size", probeJSON(`{"size": "0", "duration": "5.0"}`, ""), "size"},
		{"non-numeric size", probeJSON(`{"size": "big", "duration": "5.0"}`, ""), "size"},
		{"float size", probeJSON(`{"size": "1.5", "duration": "5.0"}`, ""), "size"},
		{"no duration", probeJSON(`{"size": "10"}`, ""), "no duration"},
		{"N/A duration", probeJSON(`{"size": "10", "duration": "N/A"}`, ""), "duration"},
		{"zero duration", probeJSON(`{"size": "10", "duration": "0.000000"}`, ""), "duration"},
		{"negative duration", probeJSON(`{"size": "10", "duration": "-1"}`, ""), "duration"},
		{"inf duration", probeJSON(`{"size": "10", "duration": "Inf"}`, ""), "duration"},
		{"no format", `{"streams": []}`, "no format"},
		{"no streams", `{"format": {"size": "10", "duration": "5"}}`, "no streams"},
		{"not JSON", `nope`, "unreadable JSON"},
		{"bad nal length size", `{"streams": [{"index": 0, "codec_type": "video", "nal_length_size": "x"}], "format": {"size": "10", "duration": "5"}}`, "nal_length_size"},
		{"bad sample rate", `{"streams": [{"index": 0, "codec_type": "audio", "sample_rate": "x"}], "format": {"size": "10", "duration": "5"}}`, "sample_rate"},
		{"zero sample rate", `{"streams": [{"index": 0, "codec_type": "audio", "sample_rate": "0"}], "format": {"size": "10", "duration": "5"}}`, "sample_rate"},
		{"zero nal length size", `{"streams": [{"index": 0, "codec_type": "video", "nal_length_size": "0"}], "format": {"size": "10", "duration": "5"}}`, "nal_length_size"},
		{"chapter without end", probeJSON(`{"size": "10", "duration": "5"}`, `, "chapters": [{"start_time": "0.0"}]`), "chapter 0"},
		{"chapter with bad start", probeJSON(`{"size": "10", "duration": "5"}`, `, "chapters": [{"start_time": "x", "end_time": "1"}]`), "start_time"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			info, err := Probe(context.Background(), &fakeRunner{res: proc.Result{Stdout: []byte(tc.out)}}, "/x/a.mkv")
			if err == nil || !strings.Contains(err.Error(), tc.want) || !strings.Contains(err.Error(), "/x/a.mkv") {
				t.Fatalf("err = %v, want one naming the file and containing %q", err, tc.want)
			}
			if !reflect.DeepEqual(info, Info{}) {
				t.Fatalf("returned %+v alongside an error", info)
			}
		})
	}
}

func TestProbeNonZeroExit(t *testing.T) {
	_, err := Probe(context.Background(), &fakeRunner{res: proc.Result{ExitCode: 1, Stderr: []byte("Invalid data\n")}}, "/x/a.mkv")
	if err == nil || !strings.Contains(err.Error(), "Invalid data") {
		t.Fatalf("err = %v", err)
	}
}

func TestProbeWithoutChaptersOrTitle(t *testing.T) {
	out := probeJSON(`{"filename": "/x/a.mkv", "size": "10", "duration": "5.5", "tags": {"TITLE": "Mixed", "Encoder": "x"}}`, "")
	info, err := Probe(context.Background(), &fakeRunner{res: proc.Result{Stdout: []byte(out)}}, "/x/a.mkv")
	if err != nil {
		t.Fatal(err)
	}
	if info.Chapters == nil || len(info.Chapters) != 0 {
		t.Errorf("Chapters = %#v, want an empty slice", info.Chapters)
	}
	if info.Format.Title != "Mixed" || info.Format.Tags["encoder"] != "x" {
		t.Errorf("format = %+v", info.Format)
	}
	bare, err := Probe(context.Background(), &fakeRunner{res: proc.Result{Stdout: []byte(probeJSON(`{"size": "1", "duration": "1"}`, ""))}}, "/x/a.mkv")
	if err != nil || bare.Format.Title != "" {
		t.Fatalf("title = %q, %v", bare.Format.Title, err)
	}
}

func TestChapters(t *testing.T) {
	out := `{"chapters": [
		{"start_time": "0.000000", "end_time": "10.5", "tags": {"title": "One"}},
		{"start_time": "10.5", "end_time": "20.25", "tags": {"TITLE": "Two"}},
		{"start_time": "20.25", "end_time": "30"}]}`
	r := &fakeRunner{res: proc.Result{Stdout: []byte(out)}}
	got, err := Chapters(context.Background(), r, "/x/a.mkv")
	if err != nil {
		t.Fatal(err)
	}
	want := []Chapter{{0, 10.5, "One"}, {10.5, 20.25, "Two"}, {20.25, 30, ""}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
	if want := []string{"ffprobe", "-v", "error", "-show_chapters", "-of", "json", "/x/a.mkv"}; !reflect.DeepEqual(r.argv, want) {
		t.Fatalf("argv = %q", r.argv)
	}
}

func TestChaptersNoneIsEmptyNotError(t *testing.T) {
	for _, out := range []string{`{"chapters": []}`, `{}`} {
		got, err := Chapters(context.Background(), &fakeRunner{res: proc.Result{Stdout: []byte(out)}}, "/x/a.mkv")
		if err != nil || got == nil || len(got) != 0 {
			t.Fatalf("%s: got %#v, %v", out, got, err)
		}
	}
}

func TestStreamsGainsTimeBaseAndNALLengthSize(t *testing.T) {
	r := &fakeRunner{res: proc.Result{Stdout: fixture(t, "fate-apocrypha-s01d01.json")}}
	streams, err := Streams(context.Background(), r, "/x/a.mkv")
	if err != nil {
		t.Fatal(err)
	}
	if streams[0].NALLengthSize != 4 || streams[0].TimeBase != "1/1000" || streams[3].NALLengthSize != 0 || streams[1].SampleRate != 48000 || streams[0].SampleRate != 0 {
		t.Fatalf("streams = %+v", streams)
	}
}
