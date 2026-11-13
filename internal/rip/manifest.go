package rip

import (
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/reclyptor/mediaops/internal/health"
	"github.com/reclyptor/mediaops/internal/plan"
	"github.com/reclyptor/mediaops/internal/runstore"
	"github.com/reclyptor/mediaops/internal/verify"
)

const (
	manifestFile   = "manifest.json"
	manifestSchema = 2
)

// manifest is the manifest.json written beside a disc's files: what was ripped,
// from what, and how it was judged.
type manifest struct {
	Schema        int               `json:"schema"`
	RunID         string            `json:"run_id"`
	DiscIndex     int               `json:"disc_index"`
	Plan          plan.Entry        `json:"plan"`
	Drive         runstore.DriveRef `json:"drive"`
	Makemkv       manifestMakemkv   `json:"makemkv"`
	Disc          manifestDisc      `json:"disc"`
	Titles        []manifestTitle   `json:"titles"`
	Errors        []string          `json:"errors"`
	SuspectEvents []health.Event    `json:"suspect_events"`
	Started       time.Time         `json:"started"`
	CopyStarted   *time.Time        `json:"copy_started"`
	Finished      time.Time         `json:"finished"`
}

type manifestMakemkv struct {
	Version     string `json:"version"`
	Exit        int    `json:"exit"`
	TitlesSaved int    `json:"titles_saved"`
}

type manifestDisc struct {
	Name        string `json:"name"`
	VolumeLabel string `json:"volume_label"`
	Type        string `json:"type"`
}

// manifestTitle is one ripped file. A figure makemkvcon did not report is null.
type manifestTitle struct {
	ID              int     `json:"id"`
	Name            string  `json:"name"`
	File            string  `json:"file"`
	Chapters        *int    `json:"chapters"`
	DurationSeconds *int    `json:"duration_seconds"`
	Duration        string  `json:"duration"`
	SizeBytes       *int64  `json:"size_bytes"`
	SourcePlaylist  string  `json:"source_playlist"`
	FileBytes       int64   `json:"file_bytes"`
	FileSeconds     float64 `json:"file_seconds"`
	SegmentTitle    string  `json:"segment_title"`
}

// buildManifest describes a verified rip. files must be sorted by name.
func (w *worker) buildManifest(in finishInput, files []measuredFile) (manifest, error) {
	tally, _ := in.rip.Tally()
	disc := in.info.Disc()
	m := manifest{
		Schema:    manifestSchema,
		RunID:     w.runID,
		DiscIndex: w.index,
		Plan:      w.entry,
		Drive:     *driveRef(w.drive),
		Makemkv: manifestMakemkv{
			Version:     strings.TrimPrefix(firstNonEmpty(in.rip.Version(), in.info.Version()), "MakeMKV "),
			Exit:        in.ripExit,
			TitlesSaved: tally.Saved,
		},
		Disc:          manifestDisc{Name: disc.Name, VolumeLabel: disc.VolumeLabel, Type: disc.Type},
		Errors:        append([]string{}, in.rip.Errors()...),
		SuspectEvents: append([]health.Event{}, in.suspects...),
		Started:       w.started,
		Finished:      stamp(w.clock.Now()),
	}
	switch {
	case !w.snap.copyStarted.IsZero():
		m.CopyStarted = &w.snap.copyStarted
	case w.earlier != nil:
		m.CopyStarted = w.earlier.CopyStartedAt // a finalize copied nothing itself
	}
	for _, f := range files {
		title, ok := verify.TitleFor(in.info, f.name)
		if !ok {
			return manifest{}, fmt.Errorf("%s matches no title in the disc's title table", f.name)
		}
		mt := manifestTitle{
			ID:             title.ID,
			Name:           title.Name,
			File:           f.name,
			Chapters:       positive(title.Chapters),
			Duration:       title.Duration,
			SizeBytes:      positive(title.SizeBytes),
			SourcePlaylist: title.SourcePlaylist,
			FileBytes:      f.bytes,
			FileSeconds:    math.Round(f.seconds*1000) / 1000,
			SegmentTitle:   w.entry.SegmentTitle(),
		}
		if secs, ok := title.Seconds(); ok {
			mt.DurationSeconds = &secs
		}
		m.Titles = append(m.Titles, mt)
	}
	return m, nil
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// positive is &n, or nil when n is zero or less, which makemkv's parser uses
// for "not reported".
func positive[T int | int64](n T) *T {
	if n <= 0 {
		return nil
	}
	return &n
}
