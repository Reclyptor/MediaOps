package ffprobe

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/reclyptor/mediaops/kit/proc"
)

type fakeRunner struct {
	res  proc.Result
	err  error
	argv []string
}

func (f *fakeRunner) Run(_ context.Context, argv ...string) (proc.Result, error) {
	f.argv = argv
	return f.res, f.err
}

func (f *fakeRunner) Stream(context.Context, []string, func(string)) (int, error) {
	panic("not used")
}

func TestDuration(t *testing.T) {
	ok := func(out string) proc.Result { return proc.Result{Stdout: []byte(out)} }
	tests := []struct {
		name    string
		res     proc.Result
		want    float64
		wantErr string
	}{
		{"normal", ok(`{"format": {"duration": "1432.768000"}}`), 1432.768, ""},
		{"zero", ok(`{"format": {"duration": "0.000000"}}`), 0, "unusable"},
		{"negative", ok(`{"format": {"duration": "-3.5"}}`), 0, "unusable"},
		{"nan", ok(`{"format": {"duration": "NaN"}}`), 0, "unusable"},
		{"inf", ok(`{"format": {"duration": "Inf"}}`), 0, "unusable"},
		{"missing format", ok(`{}`), 0, "no duration"},
		{"empty format", ok(`{"format": {}}`), 0, "no duration"},
		{"N/A", ok(`{"format": {"duration": "N/A"}}`), 0, "not a number"},
		{"non-numeric", ok(`{"format": {"duration": "abc"}}`), 0, "not a number"},
		{"numeric JSON type", ok(`{"format": {"duration": 12}}`), 0, "unreadable JSON"},
		{"bad JSON", ok(`not json`), 0, "unreadable JSON"},
		{"empty output", ok(``), 0, "unreadable JSON"},
		{"non-zero exit", proc.Result{ExitCode: 1, Stderr: []byte("moov atom not found\n")}, 0, "moov atom not found"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Duration(context.Background(), &fakeRunner{res: tc.res}, "/x/a.mkv")
			if tc.wantErr == "" {
				if err != nil || got != tc.want {
					t.Fatalf("got %v, %v; want %v", got, err, tc.want)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %v, want it to contain %q", err, tc.wantErr)
			}
			if got != 0 {
				t.Fatalf("duration %v returned alongside an error", got)
			}
		})
	}
}

func TestDurationArgv(t *testing.T) {
	r := &fakeRunner{res: proc.Result{Stdout: []byte(`{"format":{"duration":"1"}}`)}}
	if _, err := Duration(context.Background(), r, "/x/a.mkv"); err != nil {
		t.Fatal(err)
	}
	want := []string{"ffprobe", "-v", "error", "-show_entries", "format=duration", "-of", "json", "/x/a.mkv"}
	if !reflect.DeepEqual(r.argv, want) {
		t.Fatalf("argv = %q, want %q", r.argv, want)
	}
}

func TestDurationRunnerError(t *testing.T) {
	boom := errors.New("boom")
	_, err := Duration(context.Background(), &fakeRunner{err: boom}, "/x/a.mkv")
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want it to wrap the runner error", err)
	}
}

func ok(out string) proc.Result { return proc.Result{Stdout: []byte(out)} }

func TestStreams(t *testing.T) {
	r := &fakeRunner{res: ok(`{"streams": [
		{"index": 0, "codec_type": "video", "codec_name": "h264", "width": 1920, "height": 1080, "disposition": {"default": 1}},
		{"index": 1, "codec_type": "audio", "codec_name": "flac", "channels": 2, "disposition": {"default": 0},
		 "tags": {"LANGUAGE": "jpn", "language": "jpn", "TITLE": "Japanese"}},
		{"index": 2, "codec_type": "subtitle", "codec_name": "subrip", "disposition": {"default": 1}}]}`)}
	got, err := Streams(context.Background(), r, "/x/a.mkv")
	if err != nil {
		t.Fatal(err)
	}
	want := []Stream{
		{Index: 0, CodecType: "video", CodecName: "h264", Width: 1920, Height: 1080, Default: true, Tags: map[string]string{}, SampleAspect: 1},
		{Index: 1, CodecType: "audio", CodecName: "flac", Channels: 2, Tags: map[string]string{"language": "jpn", "title": "Japanese"}, SampleAspect: 1},
		{Index: 2, CodecType: "subtitle", CodecName: "subrip", Default: true, Tags: map[string]string{}, SampleAspect: 1},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("streams = %+v, want %+v", got, want)
	}
	wantArgv := []string{"ffprobe", "-v", "error", "-show_streams", "-of", "json", "/x/a.mkv"}
	if !reflect.DeepEqual(r.argv, wantArgv) {
		t.Fatalf("argv = %q, want %q", r.argv, wantArgv)
	}
}

func TestStreamsSampleAspect(t *testing.T) {
	for _, tc := range []struct {
		raw  string
		want float64
	}{{`"64:45"`, 64.0 / 45}, {`"1:1"`, 1}, {`"0:1"`, 1}, {`"N/A"`, 1}, {`""`, 1}, {"", 1}} {
		field := ""
		if tc.raw != "" {
			field = `, "sample_aspect_ratio": ` + tc.raw
		}
		got, err := Streams(context.Background(), &fakeRunner{res: ok(`{"streams": [{"index": 0, "codec_type": "video"` + field + `}]}`)}, "/x/a.mkv")
		if err != nil || got[0].SampleAspect != tc.want {
			t.Errorf("%q: got %+v, %v; want %v", tc.raw, got, err, tc.want)
		}
	}
	for _, raw := range []string{`"abc"`, `"4"`, `"a:b"`, `"-1:2"`, `"1:x"`, `"1:2:3"`} {
		_, err := Streams(context.Background(), &fakeRunner{res: ok(`{"streams": [{"index": 0, "codec_type": "video", "sample_aspect_ratio": ` + raw + `}]}`)}, "/x/a.mkv")
		if err == nil || !strings.Contains(err.Error(), "sample_aspect_ratio") {
			t.Errorf("%s: err = %v, want a sample_aspect_ratio error", raw, err)
		}
	}
}

func TestStreamsFailClosed(t *testing.T) {
	tests := []struct {
		name    string
		res     proc.Result
		wantErr string
	}{
		{"non-zero exit", proc.Result{ExitCode: 1, Stderr: []byte("No such file")}, "No such file"},
		{"bad JSON", ok(`nope`), "unreadable JSON"},
		{"no streams array", ok(`{}`), "no streams array"},
		{"missing index", ok(`{"streams": [{"codec_type": "audio"}]}`), "no index or codec_type"},
		{"missing codec_type", ok(`{"streams": [{"index": 0}]}`), "no index or codec_type"},
		{"colliding tags", ok(`{"streams": [{"index": 3, "codec_type": "audio", "tags": {"language": "jpn", "LANGUAGE": "eng"}}]}`), "differ"},
		{"non-string tag", ok(`{"streams": [{"index": 0, "codec_type": "audio", "tags": {"language": 1}}]}`), "unreadable JSON"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Streams(context.Background(), &fakeRunner{res: tc.res}, "/x/a.mkv")
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %v, want it to contain %q", err, tc.wantErr)
			}
			if got != nil {
				t.Fatalf("streams %v returned alongside an error", got)
			}
		})
	}
}

func TestStreamsEmptyList(t *testing.T) {
	got, err := Streams(context.Background(), &fakeRunner{res: ok(`{"streams": []}`)}, "/x/a.mkv")
	if err != nil || len(got) != 0 {
		t.Fatalf("got %v, %v; want no streams and no error", got, err)
	}
}

func TestStreamsRunnerError(t *testing.T) {
	boom := errors.New("boom")
	if _, err := Streams(context.Background(), &fakeRunner{err: boom}, "/x/a.mkv"); !errors.Is(err, boom) {
		t.Fatalf("err = %v, want it to wrap the runner error", err)
	}
}

func TestCountFrames(t *testing.T) {
	r := &fakeRunner{res: ok(`{"streams": [{"nb_read_frames": "48", "nb_read_packets": "50"}]}`)}
	frames, packets, err := CountFrames(context.Background(), r, "/x/a.mkv")
	if err != nil || frames != 48 || packets != 50 {
		t.Fatalf("got %d, %d, %v; want 48, 50", frames, packets, err)
	}
	want := []string{"ffprobe", "-v", "error", "-count_frames", "-count_packets", "-select_streams", "v:0",
		"-show_entries", "stream=nb_read_frames,nb_read_packets", "-of", "json", "/x/a.mkv"}
	if !reflect.DeepEqual(r.argv, want) {
		t.Fatalf("argv = %q, want %q", r.argv, want)
	}
}

func TestCountFramesFailClosed(t *testing.T) {
	tests := []struct {
		name    string
		res     proc.Result
		wantErr string
	}{
		{"non-zero exit", proc.Result{ExitCode: 1, Stderr: []byte("broken")}, "broken"},
		{"bad JSON", ok(`x`), "unreadable JSON"},
		{"no stream", ok(`{"streams": []}`), "no video stream"},
		{"no streams key", ok(`{}`), "no video stream"},
		{"missing frames", ok(`{"streams": [{"nb_read_packets": "5"}]}`), "no frame or packet count"},
		{"missing packets", ok(`{"streams": [{"nb_read_frames": "5"}]}`), "no frame or packet count"},
		{"N/A frames", ok(`{"streams": [{"nb_read_frames": "N/A", "nb_read_packets": "5"}]}`), "not a number"},
		{"non-numeric packets", ok(`{"streams": [{"nb_read_frames": "5", "nb_read_packets": "x"}]}`), "not a number"},
		{"negative frames", ok(`{"streams": [{"nb_read_frames": "-1", "nb_read_packets": "5"}]}`), "negative"},
		{"negative packets", ok(`{"streams": [{"nb_read_frames": "1", "nb_read_packets": "-5"}]}`), "negative"},
		{"numeric JSON type", ok(`{"streams": [{"nb_read_frames": 5, "nb_read_packets": "5"}]}`), "unreadable JSON"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f, p, err := CountFrames(context.Background(), &fakeRunner{res: tc.res}, "/x/a.mkv")
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %v, want it to contain %q", err, tc.wantErr)
			}
			if f != 0 || p != 0 {
				t.Fatalf("counts %d, %d returned alongside an error", f, p)
			}
		})
	}
}

func TestDecodeErrors(t *testing.T) {
	tests := []struct {
		name   string
		stderr string
		want   []string
	}{
		{"clean", "", nil},
		{"blank lines only", "\n  \n", nil},
		{"lines", "[h264] error one\n\n  [aac] error two \r\n", []string{"[h264] error one", "[aac] error two"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := &fakeRunner{res: proc.Result{Stderr: []byte(tc.stderr)}}
			got, err := DecodeErrors(context.Background(), r, "/x/a.mkv")
			if err != nil || !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %q, %v; want %q", got, err, tc.want)
			}
			wantArgv := []string{"ffmpeg", "-nostdin", "-v", "error", "-i", "/x/a.mkv", "-f", "null", "-"}
			if !reflect.DeepEqual(r.argv, wantArgv) {
				t.Fatalf("argv = %q, want %q", r.argv, wantArgv)
			}
			if err := proc.Check(r.argv); err != nil {
				t.Fatalf("the guard refuses the decode check: %v", err)
			}
		})
	}
}

func TestDecodeErrorsFailClosed(t *testing.T) {
	_, err := DecodeErrors(context.Background(), &fakeRunner{res: proc.Result{ExitCode: 1, Stderr: []byte("Invalid data")}}, "/x/a.mkv")
	if err == nil || !strings.Contains(err.Error(), "Invalid data") {
		t.Fatalf("err = %v, want exit and stderr tail", err)
	}
	boom := errors.New("boom")
	if _, err := DecodeErrors(context.Background(), &fakeRunner{err: boom}, "/x/a.mkv"); !errors.Is(err, boom) {
		t.Fatalf("err = %v, want it to wrap the runner error", err)
	}
}
