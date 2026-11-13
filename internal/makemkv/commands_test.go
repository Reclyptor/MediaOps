package makemkv

import (
	"reflect"
	"testing"
)

func TestCommands(t *testing.T) {
	wantInfo := []string{"makemkvcon", "-r", "--noscan", "--minlength=60", "info", "dev:/dev/sr3"}
	if got := InfoArgv("/dev/sr3", 60); !reflect.DeepEqual(got, wantInfo) {
		t.Errorf("InfoArgv = %q, want %q", got, wantInfo)
	}
	wantRip := []string{
		"makemkvcon", "-r", "--noscan", "--progress=-same", "--minlength=90",
		"mkv", "dev:/dev/sr3", "all", "/stage/anime/X/Disc 01.partial",
	}
	if got := RipArgv("/dev/sr3", 90, "/stage/anime/X/Disc 01.partial"); !reflect.DeepEqual(got, wantRip) {
		t.Errorf("RipArgv = %q, want %q", got, wantRip)
	}
}
