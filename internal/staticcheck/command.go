// Package staticcheck tells disc furniture from content. MakeMKV rips menu
// backgrounds and logo cards as ordinary titles; a held still hashes to the
// same dHash at every sampled second, where a creditless OP or a promo keeps
// changing.
package staticcheck

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"sync"

	"github.com/reclyptor/mediaops/internal/cli"
	"github.com/reclyptor/mediaops/internal/dhash"
	"github.com/reclyptor/mediaops/kit/proc"
)

const usage = "usage: mkv-staticcheck FILE... [--workers N] | --version"

const (
	// DefaultWorkers bounds the decodes in flight.
	DefaultWorkers = 4
	// sampleFPS is the sampling rate: one frame a second.
	sampleFPS = 1.0
	// staticRatio is the distinct-to-total frame ratio at or below which a
	// file is a still.
	staticRatio = 0.15
)

// Verdicts a file can get.
const (
	Static  = "static"
	Content = "content"
)

// FileReport is one file's result. Frames, Distinct, Ratio and Verdict are set
// for a readable file, Error for an unreadable one.
type FileReport struct {
	Path     string  `json:"path"`
	Frames   int     `json:"frames,omitempty"`
	Distinct int     `json:"distinct,omitempty"`
	Ratio    float64 `json:"ratio,omitempty"`
	Verdict  string  `json:"verdict,omitempty"`
	Error    string  `json:"error,omitempty"`
}

// Report is mkv-staticcheck's one JSON value; Files are in argument order.
type Report struct {
	Files      []FileReport `json:"files"`
	Static     int          `json:"static"`
	Content    int          `json:"content"`
	Unreadable int          `json:"unreadable"`
}

// classify judges a file from its frame hashes: static when at most 15% of
// the frames are distinct. ok is false for no frames at all. The verdict is
// decided on the exact ratio, the reported ratio is rounded to four places.
func classify(hashes []uint64) (rep FileReport, ok bool) {
	if len(hashes) == 0 {
		return FileReport{}, false
	}
	seen := make(map[uint64]struct{}, len(hashes))
	for _, h := range hashes {
		seen[h] = struct{}{}
	}
	ratio := float64(len(seen)) / float64(len(hashes))
	rep = FileReport{
		Frames:   len(hashes),
		Distinct: len(seen),
		Ratio:    math.Round(ratio*1e4) / 1e4,
		Verdict:  Content,
	}
	if ratio <= staticRatio {
		rep.Verdict = Static
	}
	return rep, true
}

// Command is the mkv-staticcheck body: args are what follows the program name.
// It prints one JSON report and exits 1 when any file could not be read.
func Command(ctx context.Context, p proc.Piper, args []string, streams cli.IO) error {
	workers := DefaultWorkers
	fs := cli.NewFlagSet("mkv-staticcheck")
	fs.IntVar(&workers, "workers", DefaultWorkers, "")
	files, err := cli.ParseFlags(fs, args)
	if err != nil || len(files) == 0 {
		return cli.Usage(errors.New(usage))
	}
	if workers < 1 {
		return cli.Usage(fmt.Errorf("--workers must be at least 1, got %d", workers))
	}
	for _, f := range files {
		info, err := os.Stat(f)
		switch {
		case err != nil:
			return cli.Usage(fmt.Errorf("cannot read %s: %w", f, err))
		case !info.Mode().IsRegular():
			return cli.Usage(fmt.Errorf("%s is not a regular file", f))
		}
	}

	reports := make([]FileReport, len(files))
	jobs := make(chan int)
	var wg sync.WaitGroup
	for range min(workers, len(files)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				if ctx.Err() != nil {
					continue
				}
				reports[i] = check(ctx, p, files[i])
			}
		}()
	}
feed:
	for i := range files {
		select {
		case jobs <- i:
		case <-ctx.Done():
			break feed
		}
	}
	close(jobs)
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("interrupted: %w", err)
	}

	rep := Report{Files: reports}
	for _, r := range reports {
		switch r.Verdict {
		case Static:
			rep.Static++
		case Content:
			rep.Content++
		default:
			rep.Unreadable++
		}
	}
	if err := cli.PrintJSON(streams.Stdout, rep); err != nil {
		return err
	}
	if rep.Unreadable > 0 {
		return cli.Exit(cli.ExitIncomplete)
	}
	return nil
}

func check(ctx context.Context, p proc.Piper, path string) FileReport {
	hashes, err := dhash.Sample(ctx, p, path, sampleFPS, nil)
	if err != nil {
		return FileReport{Path: path, Error: err.Error()}
	}
	rep, ok := classify(hashes)
	if !ok {
		return FileReport{Path: path, Error: "no frames decoded"}
	}
	rep.Path = path
	return rep
}
