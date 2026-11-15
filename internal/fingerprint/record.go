// Package fingerprint proves episode adjacency from next-episode previews.
// Every episode of some shows ends with a short preview cut from the next
// one, so the track whose body holds a preview's frames is its successor. A
// record is one track's perceptual hashes; match and dupcheck compare them.
package fingerprint

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/reclyptor/mediaops/internal/dhash"
	"github.com/reclyptor/mediaops/kit/fsutil"
)

// ToolName and SchemaVersion identify a record.
const (
	ToolName      = "mkv-fingerprint"
	SchemaVersion = 1
)

// Sampling rates: the body is dense enough to catch a one or two second
// preview clip, the preview denser still.
const (
	BodyFPS    = 2
	PreviewFPS = 4
)

// maxRecordBytes bounds how much of a record is ever read. A ten hour track
// is about 1.5 MB.
const maxRecordBytes = 64 << 20

// ErrInvalid marks a file that is not a usable schema-1 record.
var ErrInvalid = errors.New("not a schema-1 fingerprint record")

var errNotRegular = errors.New("not a regular file")

// Record is one track's fingerprint, as it is stored. PreviewStart is nil for
// a track with no preview, which is then also the only case with an empty
// Preview.
type Record struct {
	Tool         string   `json:"tool"`
	Schema       int      `json:"schema"`
	Path         string   `json:"path"`
	PreviewStart *float64 `json:"preview_start"`
	BodyFPS      int      `json:"body_fps"`
	PreviewFPS   int      `json:"preview_fps"`
	Body         []string `json:"body"`
	Preview      []string `json:"preview"`
}

// NewRecord builds the record of path from its hashes. path must be absolute
// for the record to validate.
func NewRecord(path string, previewStart *float64, body, preview []uint64) Record {
	return Record{
		Tool:         ToolName,
		Schema:       SchemaVersion,
		Path:         path,
		PreviewStart: previewStart,
		BodyFPS:      BodyFPS,
		PreviewFPS:   PreviewFPS,
		Body:         formatAll(body),
		Preview:      formatAll(preview),
	}
}

func formatAll(hs []uint64) []string {
	out := make([]string, len(hs))
	for i, h := range hs {
		out[i] = dhash.Format(h)
	}
	return out
}

// Hashes parses the record's body and preview hashes.
func (r Record) Hashes() (body, preview []uint64, err error) {
	if body, err = parseAll(r.Body); err != nil {
		return nil, nil, fmt.Errorf("body: %w", err)
	}
	if preview, err = parseAll(r.Preview); err != nil {
		return nil, nil, fmt.Errorf("preview: %w", err)
	}
	return body, preview, nil
}

func parseAll(hex []string) ([]uint64, error) {
	out := make([]uint64, len(hex))
	for i, s := range hex {
		h, err := dhash.Parse(s)
		if err != nil {
			return nil, fmt.Errorf("frame %d: %w", i, err)
		}
		out[i] = h
	}
	return out, nil
}

// Validate reports why r is not a usable schema-1 record: another tool or
// schema, a relative path, a bad hash, no body frames, or a preview that
// disagrees with preview_start.
func (r Record) Validate() error {
	switch {
	case r.Tool != ToolName:
		return fmt.Errorf("tool is %q, want %q", r.Tool, ToolName)
	case r.Schema != SchemaVersion:
		return fmt.Errorf("schema is %d, want %d", r.Schema, SchemaVersion)
	case !filepath.IsAbs(r.Path):
		return fmt.Errorf("path %q is not absolute", r.Path)
	case r.BodyFPS != BodyFPS || r.PreviewFPS != PreviewFPS:
		return fmt.Errorf("sampled at %d and %d fps, want %d and %d", r.BodyFPS, r.PreviewFPS, BodyFPS, PreviewFPS)
	}
	if _, _, err := r.Hashes(); err != nil {
		return err
	}
	if len(r.Body) == 0 {
		return errors.New("no body frames")
	}
	switch {
	case r.PreviewStart == nil && len(r.Preview) > 0:
		return errors.New("preview frames without a preview_start")
	case r.PreviewStart != nil && len(r.Preview) == 0:
		return errors.New("preview_start set but no preview frames")
	case r.PreviewStart != nil && !validStart(*r.PreviewStart):
		return fmt.Errorf("preview_start %v is not a positive number of seconds", *r.PreviewStart)
	}
	return nil
}

func validStart(f float64) bool {
	return f > 0 && !math.IsInf(f, 0) && !math.IsNaN(f)
}

// ParsePreviewStart reads the PREVIEW_START argument: seconds above zero, or
// "none" for a track with no preview (nil).
func ParsePreviewStart(s string) (*float64, error) {
	if s == "none" {
		return nil, nil
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil || !validStart(f) {
		return nil, fmt.Errorf("PREVIEW_START must be a positive number of seconds or none, got %q", s)
	}
	return &f, nil
}

func invalid(path, format string, a ...any) error {
	return fmt.Errorf("%s: %w: %s", path, ErrInvalid, fmt.Sprintf(format, a...))
}

// Read loads and strictly validates the record at path. Only a regular file,
// not reached through a symlink, is read, and at most 64 MiB of it. A file
// that is not a usable record wraps ErrInvalid; one that cannot be read does
// not.
func Read(path string) (Record, error) {
	data, err := readRegular(path, maxRecordBytes, true)
	if errors.Is(err, errNotRegular) || errors.Is(err, errTooLarge) {
		return Record{}, invalid(path, "%v", err)
	}
	if err != nil {
		return Record{}, fmt.Errorf("reading %s: %w", path, err)
	}

	var head struct {
		Tool   string          `json:"tool"`
		Schema json.RawMessage `json:"schema"`
	}
	if err := json.Unmarshal(data, &head); err != nil {
		return Record{}, invalid(path, "%v", err)
	}
	if head.Tool != ToolName {
		return Record{}, invalid(path, "tool is %q, want %q (a record from the Python tool?)", head.Tool, ToolName)
	}
	if string(head.Schema) != strconv.Itoa(SchemaVersion) {
		return Record{}, invalid(path, "schema is %s, want %d", head.Schema, SchemaVersion)
	}

	var rec Record
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&rec); err != nil {
		return Record{}, invalid(path, "%v", err)
	}
	if _, err := dec.Token(); err != io.EOF {
		return Record{}, invalid(path, "data after the record")
	}
	if err := rec.Validate(); err != nil {
		return Record{}, invalid(path, "%v", err)
	}
	return rec, nil
}

// CheckOutput refuses an output path that cannot be a record: one not ending
// in .json, so no output can name a media file, or one that exists and is not
// a regular file.
func CheckOutput(path string) error {
	if !strings.HasSuffix(path, ".json") {
		return fmt.Errorf("%s does not end in .json", path)
	}
	info, err := os.Lstat(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return nil
	case err != nil:
		return err
	case !info.Mode().IsRegular():
		return fmt.Errorf("%s exists and is not a regular file", path)
	}
	return nil
}

// Write stores r at path, replacing a regular file already there. A record
// that does not validate is never written.
func Write(path string, r Record) error {
	if err := CheckOutput(path); err != nil {
		return err
	}
	if err := r.Validate(); err != nil {
		return fmt.Errorf("refusing to write %s: %w", path, err)
	}
	data, err := json.Marshal(r)
	if err != nil {
		return fmt.Errorf("encoding %s: %w", path, err)
	}
	return fsutil.WriteFile(path, append(data, '\n'), fsutil.FileMode)
}

var errTooLarge = errors.New("too large")

// readRegular reads a whole regular file of at most max bytes. The open is
// non-blocking so a FIFO cannot hang it, and the type is checked on the open
// descriptor, so nothing swapped in after a listing is read as a record.
func readRegular(path string, max int64, nofollow bool) ([]byte, error) {
	flags := os.O_RDONLY | syscall.O_NONBLOCK
	if nofollow {
		flags |= syscall.O_NOFOLLOW
	}
	f, err := os.OpenFile(path, flags, 0)
	if errors.Is(err, syscall.ELOOP) {
		return nil, fmt.Errorf("%s is a symbolic link: %w", path, errNotRegular)
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s: %w", path, errNotRegular)
	}
	if info.Size() > max {
		return nil, fmt.Errorf("%s is larger than %d bytes: %w", path, max, errTooLarge)
	}
	data, err := io.ReadAll(io.LimitReader(f, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > max {
		return nil, fmt.Errorf("%s is larger than %d bytes: %w", path, max, errTooLarge)
	}
	return data, nil
}
