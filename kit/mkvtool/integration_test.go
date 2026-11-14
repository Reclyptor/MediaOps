//go:build integration

package mkvtool

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/reclyptor/mediaops/kit/ffprobe"
	"github.com/reclyptor/mediaops/kit/proc"
)

// realRunner is the real proc.Exec, as production code uses.
var realRunner proc.Runner = &proc.Exec{}

// synth runs a tool directly to build test data. This is the only place in the
// kit where an encoder runs: the test MKV needs lavfi test sources encoded
// into it, and no code outside _test files, nor any non-integration build, can
// reach it.
func synth(t *testing.T, name string, args ...string) {
	t.Helper()
	out, err := exec.Command(name, args...).CombinedOutput()
	if ee, ok := err.(*exec.ExitError); ok && ee.ExitCode() == 1 && strings.HasPrefix(name, "mkv") {
		err = nil // mkvtoolnix exits 1 for warnings
	}
	if err != nil {
		t.Fatalf("%s %q: %v\n%s", name, args, err, out)
	}
}

// buildSample writes a two second MKV with one video, two audio tracks, two
// SRT subtitle tracks and track names, and gives the first audio track a
// Matroska LANGUAGE track tag. It also writes the same content as an AVI.
func buildSample(t *testing.T) (mkv, avi string) {
	t.Helper()
	dir := t.TempDir()
	srt := func(name, text string) string {
		p := filepath.Join(dir, name)
		body := "1\n00:00:00,000 --> 00:00:01,500\n" + text + "\n"
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	full, signs := srt("full.srt", "Hello"), srt("signs.srt", "Sign")

	mkv = filepath.Join(dir, "sample.mkv")
	synth(t, "ffmpeg", "-nostdin", "-v", "error",
		"-f", "lavfi", "-i", "testsrc=duration=2:size=160x120:rate=24",
		"-f", "lavfi", "-i", "sine=frequency=440:duration=2",
		"-f", "lavfi", "-i", "sine=frequency=660:duration=2",
		"-i", full, "-i", signs,
		"-map", "0:v", "-map", "1:a", "-map", "2:a", "-map", "3:s", "-map", "4:s",
		"-c:v", "libx264", "-preset", "ultrafast", "-pix_fmt", "yuv420p", "-c:a", "aac", "-c:s", "srt",
		"-metadata:s:a:0", "title=English 2.0", "-metadata:s:a:0", "language=eng",
		"-metadata:s:a:1", "title=Japanese 2.0", "-metadata:s:a:1", "language=jpn",
		"-metadata:s:s:0", "title=Full", "-metadata:s:s:0", "language=eng",
		"-metadata:s:s:1", "title=Signs", "-metadata:s:s:1", "language=jpn",
		"-disposition:a:0", "default", "-disposition:a:1", "0", "-disposition:s:0", "0", "-disposition:s:1", "0",
		mkv)

	tags := filepath.Join(dir, "tags.xml")
	doc := `<?xml version="1.0"?><Tags><Tag><Targets/>` +
		`<Simple><Name>LANGUAGE</Name><String>eng</String></Simple>` +
		`<Simple><Name>ENCODER</Name><String>libfoo</String><Simple><Name>VERSION</Name><String>1.2</String></Simple></Simple>` +
		`</Tag></Tags>`
	if err := os.WriteFile(tags, []byte(doc), 0o644); err != nil {
		t.Fatal(err)
	}
	synth(t, "mkvpropedit", mkv, "--tags", "track:a1:"+tags)

	avi = filepath.Join(dir, "sample.avi")
	synth(t, "ffmpeg", "-nostdin", "-v", "error",
		"-f", "lavfi", "-i", "testsrc=duration=2:size=160x120:rate=24",
		"-f", "lavfi", "-i", "sine=frequency=440:duration=2",
		"-c:v", "mpeg4", "-c:a", "mp3", avi)
	return mkv, avi
}

func TestIntegration(t *testing.T) {
	ctx := context.Background()
	mkv, avi := buildSample(t)

	t.Run("Streams", func(t *testing.T) {
		streams, err := ffprobe.Streams(ctx, realRunner, mkv)
		if err != nil {
			t.Fatal(err)
		}
		var types []string
		for _, s := range streams {
			types = append(types, s.CodecType)
		}
		if want := []string{"video", "audio", "audio", "subtitle", "subtitle"}; !reflect.DeepEqual(types, want) {
			t.Fatalf("types = %v, want %v", types, want)
		}
		t.Logf("audio 1 tags as ffprobe shows them: %v", streams[1].Tags)
		if streams[1].Tags["title"] != "English 2.0" || streams[1].Tags["language"] != "eng" || !streams[1].Default {
			t.Fatalf("audio 1 = %+v", streams[1])
		}
	})

	t.Run("Remux", func(t *testing.T) {
		for _, src := range []string{avi, mkv} {
			dst := filepath.Join(t.TempDir(), "out.mkv")
			if err := Remux(ctx, realRunner, src, dst); err != nil {
				t.Fatal(err)
			}
			sf, sp, err := ffprobe.CountFrames(ctx, realRunner, src)
			if err != nil {
				t.Fatal(err)
			}
			df, dp, err := ffprobe.CountFrames(ctx, realRunner, dst)
			if err != nil {
				t.Fatal(err)
			}
			if sf != df || sf == 0 || sp != dp {
				t.Fatalf("%s: frames %d -> %d, packets %d -> %d", filepath.Base(src), sf, df, sp, dp)
			}
		}
	})

	t.Run("DecodeErrors", func(t *testing.T) {
		lines, err := ffprobe.DecodeErrors(ctx, realRunner, mkv)
		if err != nil || len(lines) != 0 {
			t.Fatalf("got %q, %v; want a clean decode", lines, err)
		}
		if _, err := ffprobe.DecodeErrors(ctx, realRunner, filepath.Join(t.TempDir(), "missing.mkv")); err == nil {
			t.Fatal("a missing file must not decode clean")
		}
	})

	t.Run("Identify", func(t *testing.T) {
		id, err := Identify(ctx, realRunner, mkv)
		if err != nil {
			t.Fatal(err)
		}
		var kinds []string
		for _, tr := range id.Tracks {
			kinds = append(kinds, tr.Type)
			if tr.UID == "" {
				t.Fatalf("track %d has no UID", tr.ID)
			}
		}
		if want := []string{"video", "audio", "audio", "subtitles", "subtitles"}; !reflect.DeepEqual(kinds, want) {
			t.Fatalf("types = %v, want %v", kinds, want)
		}
		if id.Tracks[1].Name != "English 2.0" || id.Tracks[1].Language != "eng" {
			t.Fatalf("audio 1 = %+v", id.Tracks[1])
		}
	})

	t.Run("SetTitle", func(t *testing.T) {
		if err := SetTitle(ctx, realRunner, mkv, "Show S01 D01"); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("SetDefaultFlags", func(t *testing.T) {
		if err := SetDefaultFlags(ctx, realRunner, mkv, 2, intp(4)); err != nil {
			t.Fatal(err)
		}
		streams, err := ffprobe.Streams(ctx, realRunner, mkv)
		if err != nil {
			t.Fatal(err)
		}
		got := []bool{}
		for _, s := range streams {
			got = append(got, s.Default)
		}
		if want := []bool{got[0], false, true, false, true}; !reflect.DeepEqual(got, want) {
			t.Fatalf("defaults = %v, want %v", got, want)
		}
		if err := SetDefaultFlags(ctx, realRunner, mkv, 1, nil); err != nil {
			t.Fatal(err)
		}
		if err := SetDefaultFlags(ctx, realRunner, mkv, 3, nil); err == nil {
			t.Fatal("a subtitle stream is not an audio default")
		}
	})

	t.Run("SetAudioLanguages", func(t *testing.T) {
		if err := SetAudioLanguages(ctx, realRunner, mkv, map[int]string{1: "fre", 2: "ger"}); err != nil {
			t.Fatal(err)
		}
		streams, err := ffprobe.Streams(ctx, realRunner, mkv)
		if err != nil {
			t.Fatal(err)
		}
		for idx, want := range map[int]string{1: "fre", 2: "ger"} {
			if got := streams[idx].Tags["language"]; got != want {
				t.Errorf("stream %d language = %q, want %q", idx, got, want)
			}
		}
		if streams[1].Tags["title"] != "English 2.0" || streams[2].Tags["title"] != "Japanese 2.0" {
			t.Errorf("a title changed: %v %v", streams[1].Tags, streams[2].Tags)
		}
		if streams[3].Tags["language"] != "eng" {
			t.Errorf("a subtitle language changed: %v", streams[3].Tags)
		}

		root, err := extractTags(ctx, realRunner, mkv)
		if err != nil {
			t.Fatal(err)
		}
		id, err := Identify(ctx, realRunner, mkv)
		if err != nil {
			t.Fatal(err)
		}
		got := pairs(tagsOf(root, id.Tracks[1].UID))
		want := []tagPair{{"ENCODER", "libfoo"}, {"LANGUAGE", "fre"}}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("track tag pairs = %v, want %v", got, want)
		}
		var encoder *node
		for _, tag := range tagsOf(root, id.Tracks[1].UID) {
			for i := range tag.Children {
				if n, _ := tag.Children[i].childText("Name"); n == "ENCODER" {
					encoder = &tag.Children[i]
				}
			}
		}
		if encoder == nil || encoder.child("Simple") == nil || !strings.Contains(encoder.child("Simple").Children[1].Text, "1.2") {
			t.Fatalf("the nested Simple did not survive: %+v", encoder)
		}
	})
}
