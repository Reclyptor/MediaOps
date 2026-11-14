package manifest

import (
	"io/fs"
)

// kindOf names what a non-regular file is, for messages.
func kindOf(mode fs.FileMode) string {
	switch {
	case mode&fs.ModeSymlink != 0:
		return "symlink"
	case mode.IsDir():
		return "directory"
	case mode&fs.ModeNamedPipe != 0:
		return "named pipe"
	case mode&fs.ModeSocket != 0:
		return "socket"
	case mode&fs.ModeCharDevice != 0:
		return "character device"
	case mode&fs.ModeDevice != 0:
		return "block device"
	default:
		return "irregular file"
	}
}
