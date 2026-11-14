package mkvtool

import (
	"context"
	"github.com/reclyptor/mediaops/kit/proc"
	"reflect"
	"strings"
	"testing"
)

func intp(i int) *int { return &i }

func TestSetDefaultFlags(t *testing.T) {
	tests := []struct {
		name string
		sub  *int
		want []bool // default flag of streams 1..4 afterwards
		argv []string
	}{
		{"subtitle chosen", intp(3), []bool{false, true, true, false}, []string{
			"mkvpropedit", "/x/a.mkv",
			"--edit", "track:a1", "--set", "flag-default=0", "--edit", "track:a2", "--set", "flag-default=1",
			"--edit", "track:s1", "--set", "flag-default=1", "--edit", "track:s2", "--set", "flag-default=0"}},
		{"no default subtitle", nil, []bool{false, true, false, false}, []string{
			"mkvpropedit", "/x/a.mkv",
			"--edit", "track:a1", "--set", "flag-default=0", "--edit", "track:a2", "--set", "flag-default=1",
			"--edit", "track:s1", "--set", "flag-default=0", "--edit", "track:s2", "--set", "flag-default=0"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := sample()
			if err := SetDefaultFlags(context.Background(), f, "/x/a.mkv", 2, tc.sub); err != nil {
				t.Fatal(err)
			}
			var got []bool
			for _, tr := range f.tracks[1:] {
				got = append(got, tr.def)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("defaults = %v, want %v", got, tc.want)
			}
			var edit []string
			for _, c := range f.calls {
				if c[0] == "mkvpropedit" {
					edit = c
				}
			}
			if !reflect.DeepEqual(edit, tc.argv) {
				t.Fatalf("mkvpropedit argv = %q, want %q", edit, tc.argv)
			}
			if f.tracks[1].title != "English 5.1" || f.tracks[4].title != "Signs" {
				t.Fatal("a track name changed")
			}
		})
	}
}

func TestSetDefaultFlagsRefusals(t *testing.T) {
	tests := []struct {
		name    string
		mod     func(*fakeMkv)
		audio   int
		sub     *int
		wantErr string
	}{
		{"audio is the video stream", nil, 0, nil, "not an audio stream"},
		{"audio is a subtitle", nil, 3, nil, "not an audio stream"},
		{"audio does not exist", nil, 9, nil, "not an audio stream"},
		{"sub is an audio stream", nil, 1, intp(2), "not a subtitle stream"},
		{"sub does not exist", nil, 1, intp(9), "not a subtitle stream"},
		{"mkvmerge sees more audio", func(f *fakeMkv) { f.mkvExtraAudio = 1 }, 1, nil, "mkvmerge sees"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := sample()
			if tc.mod != nil {
				tc.mod(f)
			}
			err := SetDefaultFlags(context.Background(), f, "/x/a.mkv", tc.audio, tc.sub)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %v, want it to contain %q", err, tc.wantErr)
			}
			if f.ran("mkvpropedit") {
				t.Fatalf("mkvpropedit ran despite the refusal: %q", f.calls)
			}
		})
	}
}

func TestSetDefaultFlagsReadBack(t *testing.T) {
	tests := []struct {
		name    string
		mod     func(*fakeMkv)
		wantErr string
	}{
		{"edit ignored", func(f *fakeMkv) { f.ignoreFlags = true }, "read back"},
		{"title changed", func(f *fakeMkv) { f.renameOnEdit = true }, "title of stream"},
		{"mkvpropedit fails", func(f *fakeMkv) { f.failEdit = 2 }, "cannot edit"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := sample()
			tc.mod(f)
			err := SetDefaultFlags(context.Background(), f, "/x/a.mkv", 2, intp(3))
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %v, want it to contain %q", err, tc.wantErr)
			}
		})
	}
}

func TestSetDefaultFlagsWarningsAccepted(t *testing.T) {
	f := sample()
	f.failEdit = 0
	// Exit 1 from mkvpropedit is a warning; the read-back still decides.
	if err := SetDefaultFlags(context.Background(), &warnOnce{fakeMkv: f}, "/x/a.mkv", 2, nil); err != nil {
		t.Fatal(err)
	}
}

// warnOnce wraps the fake so mkvpropedit reports exit 1 after applying its edit.
type warnOnce struct{ *fakeMkv }

func (w *warnOnce) Run(ctx context.Context, argv ...string) (res proc.Result, err error) {
	res, err = w.fakeMkv.Run(ctx, argv...)
	if argv[0] == "mkvpropedit" {
		res.ExitCode = 1
	}
	return res, err
}
