package proc

import (
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
)

// ErrRefused wraps every argv Check rejects.
var ErrRefused = errors.New("refused")

// allowed is every program the toolset may start.
var allowed = map[string]bool{
	"makemkvcon": true, "mkvpropedit": true, "mkvmerge": true,
	"ffprobe": true, "ffmpeg": true,
	"sg_turs": true, "sg_prevent": true, "sg_start": true,
	"dmesg": true,
}

var codecOption = regexp.MustCompile(`^-(c|codec|vcodec|acodec|scodec|dcodec)(:.+)?$`)

var imageExt = map[string]bool{".png": true, ".jpg": true, ".jpeg": true, ".bmp": true, ".pgm": true, ".ppm": true}

// Check refuses an argv that names a program outside the allowlist or that
// could re-encode media. It is the whole of the never-re-encode guarantee: no
// other package builds a generic ffmpeg or mkvmerge command line.
func Check(argv []string) error {
	if len(argv) == 0 {
		return fmt.Errorf("proc: %w: empty argv", ErrRefused)
	}
	name := argv[0]
	if strings.ContainsRune(name, filepath.Separator) || !allowed[name] {
		return fmt.Errorf("proc: %w: %q is not an allowed program", ErrRefused, name)
	}
	switch name {
	case "ffmpeg":
		return checkFFmpeg(argv[1:])
	case "mkvmerge":
		return checkMkvmerge(argv[1:])
	}
	return nil
}

// checkFFmpeg allows stream copy and decode-only uses: a codec option must say
// copy, and the output must be raw video on stdout, framemd5, a null sink or an
// image file.
func checkFFmpeg(args []string) error {
	for i, a := range args {
		if codecOption.MatchString(a) && (i+1 >= len(args) || args[i+1] != "copy") {
			return fmt.Errorf("proc: %w: ffmpeg %s must be followed by copy", ErrRefused, a)
		}
	}
	if len(args) == 0 {
		return fmt.Errorf("proc: %w: ffmpeg has no output", ErrRefused)
	}
	output := args[len(args)-1]

	// Output options follow the last input, so only -f after it names the
	// output format.
	lastInput := -1
	for i, a := range args {
		if a == "-i" {
			lastInput = i
		}
	}
	format := ""
	for i := lastInput + 1; i+1 < len(args)-1; i++ {
		if args[i] == "-f" {
			format = args[i+1]
		}
	}

	switch {
	case format == "rawvideo" && output == "-":
	case format == "framemd5", format == "null":
	case (format == "" || format == "image2") && imageExt[strings.ToLower(filepath.Ext(output))]:
	default:
		return fmt.Errorf("proc: %w: ffmpeg output %q (format %q) is not raw video, framemd5, null or an image", ErrRefused, output, format)
	}
	return nil
}

// checkMkvmerge allows identification, remuxing and splitting. mkvmerge never
// transcodes, so the guard only keeps callers from inventing other uses.
func checkMkvmerge(args []string) error {
	for _, a := range args {
		switch a {
		case "-i", "--identify", "-J", "--identification-format", "-o", "--output":
			return nil
		}
	}
	return fmt.Errorf("proc: %w: mkvmerge is only allowed to identify, remux or split", ErrRefused)
}
