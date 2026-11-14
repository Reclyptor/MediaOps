// Package inventory is the library's probe document: mkv-probe writes it, and
// mkv-order and mkv-audit postcheck read it. Classification everywhere
// downstream is by duration and chapters taken from here, never by file name,
// because MakeMKV names tracks _t00, _t01 in disc order and that carries no
// meaning.
package inventory

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/reclyptor/mediaops/internal/cli"
)

// maxDocument bounds how much of an inventory is read: a library's worth of
// records is megabytes, so anything larger is not an inventory.
const maxDocument = 256 << 20

// Stream is one stream of a probed file.
type Stream struct {
	Index    int     `json:"index"`
	Type     string  `json:"type"`
	Codec    string  `json:"codec"`
	Lang     *string `json:"lang"`
	Title    *string `json:"title"`
	Default  bool    `json:"default"`
	Channels int     `json:"channels"`
	Width    int     `json:"width"`
	Height   int     `json:"height"`
}

// Record is one probed file, or the reason it could not be probed. Sizes are
// bytes, times are seconds, and Chapters are chapter start times.
type Record struct {
	Path     string    `json:"path"`
	Rel      *string   `json:"rel,omitempty"`
	Size     int64     `json:"size,omitempty"`
	Duration float64   `json:"duration,omitempty"`
	Title    *string   `json:"title,omitempty"`
	Chapters []float64 `json:"chapters,omitempty"`
	Streams  []Stream  `json:"streams,omitempty"`
	Error    string    `json:"error,omitempty"`
}

// MarshalJSON writes an error record as only its path and error, and a success
// record with rel, title, chapters and streams always present (null or [] when
// empty), so a reader never has to tell "absent" from "empty".
func (r Record) MarshalJSON() ([]byte, error) {
	if r.Error != "" {
		return json.Marshal(struct {
			Path  string `json:"path"`
			Error string `json:"error"`
		}{r.Path, r.Error})
	}
	chapters, streams := r.Chapters, r.Streams
	if chapters == nil {
		chapters = []float64{}
	}
	if streams == nil {
		streams = []Stream{}
	}
	return json.Marshal(struct {
		Path     string    `json:"path"`
		Rel      *string   `json:"rel"`
		Size     int64     `json:"size"`
		Duration float64   `json:"duration"`
		Title    *string   `json:"title"`
		Chapters []float64 `json:"chapters"`
		Streams  []Stream  `json:"streams"`
	}{r.Path, r.Rel, r.Size, r.Duration, r.Title, chapters, streams})
}

// Inventory is mkv-probe's whole output. Probed counts every file a probe was
// attempted on, so len(Files) == Probed and Errors of them carry an error.
type Inventory struct {
	Roots   []string `json:"roots"`
	Probed  int      `json:"probed"`
	Errors  int      `json:"errors"`
	Skipped []string `json:"skipped"`
	Files   []Record `json:"files"`
}

// Read loads an mkv-probe document. A file that cannot be read, is not JSON,
// or has no "files" array, or whose records have no path, is not an inventory
// and is a usage error.
func Read(path string) (Inventory, error) {
	info, err := os.Stat(path)
	if err != nil {
		return Inventory{}, cli.Usage(fmt.Errorf("cannot read the inventory %s: %w", path, err))
	}
	if !info.Mode().IsRegular() {
		return Inventory{}, cli.Usage(fmt.Errorf("the inventory %s is not a regular file", path))
	}
	f, err := os.Open(path)
	if err != nil {
		return Inventory{}, cli.Usage(fmt.Errorf("cannot read the inventory %s: %w", path, err))
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxDocument+1))
	if err != nil {
		return Inventory{}, cli.Usage(fmt.Errorf("cannot read the inventory %s: %w", path, err))
	}
	if len(data) > maxDocument {
		return Inventory{}, cli.Usage(fmt.Errorf("the inventory %s is larger than %d MiB", path, maxDocument>>20))
	}

	var doc struct {
		Inventory
		Files *[]Record `json:"files"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return Inventory{}, cli.Usage(fmt.Errorf("%s is not an mkv-probe inventory: %w", path, err))
	}
	if doc.Files == nil {
		return Inventory{}, cli.Usage(errors.New(path + " is not an mkv-probe inventory: it has no \"files\" array"))
	}
	inv := doc.Inventory
	inv.Files = *doc.Files
	for i, rec := range inv.Files {
		if rec.Path == "" {
			return Inventory{}, cli.Usage(fmt.Errorf("%s is not an mkv-probe inventory: files[%d] has no path", path, i))
		}
	}
	return inv, nil
}

// ByPath indexes the success records by absolute path. Error records are left
// out: a caller that needs a file the probe could not read must treat it as
// missing.
func (inv Inventory) ByPath() map[string]Record {
	out := make(map[string]Record, len(inv.Files))
	for _, rec := range inv.Files {
		if rec.Error == "" {
			out[rec.Path] = rec
		}
	}
	return out
}
