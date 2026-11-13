package cli

import (
	"flag"
	"io"
)

// NewFlagSet is a flag set that reports a bad flag as an error and prints
// nothing, so the command can word the usage itself.
func NewFlagSet(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	return fs
}

// ParseFlags parses args with flags allowed among the positional arguments, as
// `finalize RUN_ID --dry-run` and `mkv-promote MANIFEST --go` are written, and
// returns the positional ones.
func ParseFlags(fs *flag.FlagSet, args []string) ([]string, error) {
	var positional []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		args = fs.Args()
		if len(args) == 0 {
			return positional, nil
		}
		positional, args = append(positional, args[0]), args[1:]
	}
}
