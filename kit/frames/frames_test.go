package frames

import (
	"context"
	"reflect"
	"strings"
	"testing"
)

const sampleFramemd5 = `#format: frame checksums
#version: 2
#hash: MD5
#tb 0: 1/1000
#media_type 0: video
#codec_id 0: rawvideo
#dimensions 0: 320x180
#sar 0: 1/1
#stream#, dts,        pts, duration,     size, hash
#tb 1: 1/48000
#media_type 1: audio
0,          0,          0,       41,     7524, 7a28c4f640e59715f750d625db6f177f
1,          0,          0,     1024,     4096, 00000000000000000000000000000001
0,         41,         83,       41,      912, 7a28c4f640e59715f750d625db6f1780
`

func TestParseFramemd5(t *testing.T) {
	h, err := parseFramemd5(strings.NewReader(sampleFramemd5))
	if err != nil {
		t.Fatal(err)
	}
	if want := map[int]Rational{0: {1, 1000}, 1: {1, 48000}}; !reflect.DeepEqual(h.TimeBase, want) {
		t.Errorf("TimeBase = %v, want %v", h.TimeBase, want)
	}
	if want := map[int]string{0: "video", 1: "audio"}; !reflect.DeepEqual(h.MediaType, want) {
		t.Errorf("MediaType = %v, want %v", h.MediaType, want)
	}
	want0 := []PacketHash{
		{Stream: 0, DTS: 0, PTS: 0, Duration: 41, Size: 7524, MD5: "7a28c4f640e59715f750d625db6f177f"},
		{Stream: 0, DTS: 41, PTS: 83, Duration: 41, Size: 912, MD5: "7a28c4f640e59715f750d625db6f1780"},
	}
	if !reflect.DeepEqual(h.Packets[0], want0) {
		t.Errorf("stream 0 = %+v, want %+v", h.Packets[0], want0)
	}
	if len(h.Packets[1]) != 1 || h.Packets[1][0].Size != 4096 {
		t.Errorf("stream 1 = %+v", h.Packets[1])
	}
}

func TestParseFramemd5Rejects(t *testing.T) {
	for name, doc := range map[string]string{
		"few fields":     "0, 0, 0, 41, 7524\n",
		"non integer":    "0, x, 0, 41, 7524, 7a28c4f640e59715f750d625db6f177f\n",
		"short md5":      "0, 0, 0, 41, 7524, 7a28\n",
		"upper md5":      "0, 0, 0, 41, 7524, 7A28C4F640E59715F750D625DB6F177F\n",
		"negative size":  "0, 0, 0, 41, -1, 7a28c4f640e59715f750d625db6f177f\n",
		"bad tb":         "#tb 0: 1/0\n",
		"tb no index":    "#tb x: 1/1000\n",
		"media no colon": "#media_type 0 video\n",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := parseFramemd5(strings.NewReader(doc)); err == nil {
				t.Fatalf("parsed %q", doc)
			}
		})
	}
}

func TestCopyHashesStreamsThroughThePiper(t *testing.T) {
	p := &fakePiper{t: t, stdout: []byte(sampleFramemd5)}
	h, err := CopyHashes(context.Background(), p, "/x/a.mkv")
	if err != nil {
		t.Fatal(err)
	}
	if len(h.Packets[0]) != 2 {
		t.Fatalf("packets = %+v", h.Packets)
	}
	want := []string{"ffmpeg", "-nostdin", "-v", "error", "-i", "/x/a.mkv", "-map", "0", "-c", "copy", "-f", "framemd5", "-"}
	if !reflect.DeepEqual(p.argv, want) {
		t.Fatalf("argv = %q", p.argv)
	}
}

func TestCopyHashesNonZeroExitCarriesStderr(t *testing.T) {
	p := &fakePiper{t: t, exit: 1, stderr: []byte("moov atom not found\n")}
	_, err := CopyHashes(context.Background(), p, "/x/a.mkv")
	if err == nil || !strings.Contains(err.Error(), "moov atom not found") || !strings.Contains(err.Error(), "/x/a.mkv") {
		t.Fatalf("err = %v", err)
	}
}

func TestCopyHashesRefusesUnparseableOutput(t *testing.T) {
	p := &fakePiper{t: t, stdout: []byte("garbage line\n")}
	if _, err := CopyHashes(context.Background(), p, "/x/a.mkv"); err == nil {
		t.Fatal("garbage parsed")
	}
}

func TestDecodeHashesArgvAndErrors(t *testing.T) {
	from, to, dur, eof := 1.5, 2.0, 0.25, -3.0
	p := &fakePiper{t: t, stdout: []byte(sampleFramemd5), stderr: []byte("[h264] error while decoding\nLast message repeated 3 times\n\n")}
	d, err := DecodeHashes(context.Background(), p, "/x/a.mkv", DecodeOptions{
		CopyTS: true, SeekFrom: &from, SeekEOF: &eof, To: &to, Duration: &dur,
		Maps: []string{"0:v:0", "0:a?"}, MaxVideoFrames: 12,
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"ffmpeg", "-nostdin", "-v", "error", "-copyts", "-ss", "1.5", "-sseof", "-3", "-i", "/x/a.mkv",
		"-to", "2", "-t", "0.25", "-frames:v", "12", "-map", "0:v:0", "-map", "0:a?", "-f", "framemd5", "-"}
	if !reflect.DeepEqual(p.argv, want) {
		t.Fatalf("argv = %q\nwant   %q", p.argv, want)
	}
	if !reflect.DeepEqual(d.Errors, []string{"[h264] error while decoding"}) {
		t.Fatalf("Errors = %q", d.Errors)
	}
}

func TestDecodeHashesNonZeroExit(t *testing.T) {
	p := &fakePiper{t: t, exit: 69, stderr: []byte("Invalid data\n")}
	if _, err := DecodeHashes(context.Background(), p, "/x/a.mkv", DecodeOptions{}); err == nil || !strings.Contains(err.Error(), "Invalid data") {
		t.Fatalf("err = %v", err)
	}
}

const (
	md5a = "654eca423d90270b4642cc4390832ac9"
	md5b = "4bce5775f7be5f45922731da9a33b4f3"
	md5c = "00000000000000000000000000000002"
)

func TestParseFramemd5SideData(t *testing.T) {
	doc := "0, 0, 0, 41, 7524, " + md5a + "\n" +
		"1, -5, -5, 32, 768, " + md5a + ", S=1, 10, " + md5b + "\n" +
		"1, 27, 27, 32, 768, " + md5a + ", S=2, 10, " + md5b + ", 4, " + md5c + "\n"
	h, err := parseFramemd5(strings.NewReader(doc))
	if err != nil {
		t.Fatal(err)
	}
	if got := h.Packets[0][0].SideData; got != nil {
		t.Errorf("no side data parsed as %v", got)
	}
	one := h.Packets[1][0]
	if one.DTS != -5 || one.Size != 768 || one.MD5 != md5a || !reflect.DeepEqual(one.SideData, []SideDataHash{{10, md5b}}) {
		t.Errorf("one entry = %+v", one)
	}
	two := h.Packets[1][1]
	if !reflect.DeepEqual(two.SideData, []SideDataHash{{10, md5b}, {4, md5c}}) || two.MD5 != md5a {
		t.Errorf("two entries = %+v", two)
	}
}
