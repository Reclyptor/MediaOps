package ffprobe

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/reclyptor/mediaops/kit/proc"
)

func TestPacketsRecordedOutput(t *testing.T) {
	r := &fakeRunner{res: proc.Result{Stdout: fixture(t, "packets-video.json")}}
	got, err := Packets(context.Background(), r, "/x/a.mkv", "v:0", "%+#6")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"ffprobe", "-v", "error", "-select_streams", "v:0", "-read_intervals", "%+#6",
		"-show_entries", "packet=stream_index,pts,dts,pts_time,size,flags", "-of", "json", "/x/a.mkv"}
	if !reflect.DeepEqual(r.argv, want) {
		t.Fatalf("argv = %q", r.argv)
	}
	if len(got) == 0 {
		t.Fatal("no packets")
	}
	first := got[0]
	if first != (Packet{Stream: 0, PTS: 0, DTS: 0, HasPTS: true, HasDTS: true, PTSTime: 0, Size: 4327, Keyframe: true}) {
		t.Errorf("first = %+v", first)
	}
	if second := got[1]; second.PTS != 83 || second.PTSTime != 0.083 || second.Keyframe {
		t.Errorf("second = %+v", second)
	}
}

func TestPacketsOmitsEmptySelectorAndInterval(t *testing.T) {
	r := &fakeRunner{res: proc.Result{Stdout: []byte(`{"packets": []}`)}}
	got, err := Packets(context.Background(), r, "/x/a.mkv", "", "")
	if err != nil || got == nil || len(got) != 0 {
		t.Fatalf("got %#v, %v", got, err)
	}
	if strings.Contains(strings.Join(r.argv, " "), "-select_streams") || strings.Contains(strings.Join(r.argv, " "), "-read_intervals") {
		t.Fatalf("argv = %q", r.argv)
	}
}

func TestPacketsAbsentTimestamps(t *testing.T) {
	out := `{"packets": [
		{"stream_index": 1, "size": "100", "flags": "K__"},
		{"stream_index": 1, "dts": 40, "size": "7", "flags": "___"},
		{"stream_index": 0, "pts": 80, "pts_time": "0.080000", "size": "9", "flags": "K_D"}]}`
	got, err := Packets(context.Background(), &fakeRunner{res: proc.Result{Stdout: []byte(out)}}, "/x/a.mkv", "", "")
	if err != nil {
		t.Fatal(err)
	}
	want := []Packet{
		{Stream: 1, Size: 100, Keyframe: true},
		{Stream: 1, DTS: 40, HasDTS: true, Size: 7},
		{Stream: 0, PTS: 80, HasPTS: true, PTSTime: 0.08, Size: 9, Keyframe: true},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v\nwant %+v", got, want)
	}
}

func TestPacketsFailClosed(t *testing.T) {
	for name, out := range map[string]string{
		"no array":        `{}`,
		"no size":         `{"packets": [{"stream_index": 0}]}`,
		"no stream":       `{"packets": [{"size": "1"}]}`,
		"bad size":        `{"packets": [{"stream_index": 0, "size": "x"}]}`,
		"pts no pts_time": `{"packets": [{"stream_index": 0, "size": "1", "pts": 3}]}`,
		"bad pts_time":    `{"packets": [{"stream_index": 0, "size": "1", "pts": 3, "pts_time": "N/A"}]}`,
		"not JSON":        `x`,
	} {
		t.Run(name, func(t *testing.T) {
			got, err := Packets(context.Background(), &fakeRunner{res: proc.Result{Stdout: []byte(out)}}, "/x/a.mkv", "", "")
			if err == nil || !strings.Contains(err.Error(), "/x/a.mkv") {
				t.Fatalf("err = %v", err)
			}
			if got != nil {
				t.Fatalf("returned %v alongside an error", got)
			}
		})
	}
	if _, err := Packets(context.Background(), &fakeRunner{res: proc.Result{ExitCode: 1, Stderr: []byte("boom")}}, "/x/a.mkv", "", ""); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("err = %v", err)
	}
}

func TestKeyframes(t *testing.T) {
	out := `{"packets": [
		{"stream_index": 0, "pts": 0, "pts_time": "10.000000", "size": "9", "flags": "K__"},
		{"stream_index": 0, "pts": 1, "pts_time": "11.500000", "size": "9", "flags": "___"},
		{"stream_index": 0, "pts": 2, "pts_time": "14.250000", "size": "9", "flags": "K__"},
		{"stream_index": 0, "pts": 3, "pts_time": "12.000000", "size": "9", "flags": "K__"},
		{"stream_index": 0, "pts": 4, "pts_time": "16.000000", "size": "9", "flags": "K__"},
		{"stream_index": 0, "size": "9", "flags": "K__"}]}`
	r := &fakeRunner{res: proc.Result{Stdout: []byte(out)}}
	got, err := Keyframes(context.Background(), r, "/x/a.mkv", 10.5, 15)
	if err != nil {
		t.Fatal(err)
	}
	if want := []float64{12, 14.25}; !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v (in range, sorted, no keyframe outside)", got, want)
	}
	if !reflect.DeepEqual(r.argv[3:7], []string{"-select_streams", "v:0", "-read_intervals", "10.5%15"}) {
		t.Fatalf("argv = %q", r.argv)
	}
}

func TestKeyframesIntervalFormatting(t *testing.T) {
	r := &fakeRunner{res: proc.Result{Stdout: []byte(`{"packets": []}`)}}
	got, err := Keyframes(context.Background(), r, "/x/a.mkv", 1425, 1431.125)
	if err != nil || got == nil || len(got) != 0 {
		t.Fatalf("got %#v, %v", got, err)
	}
	if iv := r.argv[6]; iv != "1425%1431.125" {
		t.Fatalf("interval = %q, want plain decimals", iv)
	}
}
