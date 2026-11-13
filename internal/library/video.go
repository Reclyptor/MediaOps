// Package library moves files into and out of the media library with full
// accounting: a safe cross-device move, the archive destination, and the
// promotion checks. Nothing here deletes media; the only unlinks are the ones
// in remove.go, and each is guarded.
package library

import (
	"path/filepath"
	"strings"
)

// videoExts is every container the library holds, kept in step with the
// enumeration in anime-normalize's probe-tracks.sh. An extension missing here is
// invisible to the promotion's completeness check, which is the one failure
// that check exists to prevent: .hack//SIGN is 27 .ogm files, and the library
// also carries .vob and .m4v, none of which an mkv-only list would account for.
var videoExts = []string{
	".mkv", ".ogm", ".avi", ".mp4", ".m2ts", ".ts", ".m4v", ".wmv",
	".rmvb", ".mov", ".flv", ".webm", ".divx", ".mpg", ".mpeg", ".vob",
}

// IsVideo reports whether name has a video extension, ignoring case.
func IsVideo(name string) bool {
	ext := strings.ToLower(filepath.Ext(name))
	for _, v := range videoExts {
		if ext == v {
			return true
		}
	}
	return false
}
