package library

import (
	"strings"
	"testing"
)

func TestParseArchiveManifest(t *testing.T) {
	got, err := ParseArchiveManifest([]byte(`[{"path":"a/b.mkv"},{"path":"c/d.mkv"}]`))
	if err != nil || len(got) != 2 || got[1].Path != "c/d.mkv" {
		t.Fatalf("got %v, %v", got, err)
	}

	bad := map[string]string{
		`{"path":"a"}`:                 "JSON array",
		`not json`:                     "JSON array",
		`[{"path":"a"},{}]`:            "entry 1: \"path\" is required",
		`[{"path":"a"},{"pth":"b"}]`:   "entry 1",
		`[{"path":"a","title":"x"}]`:   "entry 0",
		`["a/b.mkv"]`:                  "entry 0",
		`[{"path":"a"}] trailing`:      "JSON array",
		`[{"path":"a"},{"path":null}]`: "entry 1",
	}
	for in, want := range bad {
		if _, err := ParseArchiveManifest([]byte(in)); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("ParseArchiveManifest(%s) err = %v, want %q", in, err, want)
		}
	}
}

func TestParsePromoteManifest(t *testing.T) {
	in := `[{"src":"p/a.mkv","dst":"anime/X/a.mkv","title":"X - 01"},
	        {"src":"p/b.mkv","dst":"anime/X/b.mkv"},
	        {"keep":"anime/X/Extras/t.mkv"}]`
	got, err := ParsePromoteManifest([]byte(in))
	if err != nil || len(got) != 3 {
		t.Fatalf("got %v, %v", got, err)
	}
	if got[0].Title != "X - 01" || got[1].Title != "" || got[2].Keep != "anime/X/Extras/t.mkv" {
		t.Fatalf("fields = %+v", got)
	}

	bad := map[string]string{
		`[{"src":"a"}]`:                         `entry 0: a move needs both "src" and "dst"`,
		`[{"dst":"a"}]`:                         `entry 0: a move needs both`,
		`[{}]`:                                  `entry 0`,
		`[{"keep":"a","src":"b"}]`:              `entry 0: "keep" cannot be combined`,
		`[{"keep":"a","title":"t"}]`:            `entry 0: "keep" cannot be combined`,
		`[{"src":"a","dst":"b"},{"sorce":"c"}]`: `entry 1`,
		`[{"src":"a","dst":"b","extra":1}]`:     `entry 0`,
	}
	for in, want := range bad {
		if _, err := ParsePromoteManifest([]byte(in)); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("ParsePromoteManifest(%s) err = %v, want %q", in, err, want)
		}
	}
}
