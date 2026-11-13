package library

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
)

// ArchiveEntry is one file to archive: its path relative to MEDIA_ROOT.
type ArchiveEntry struct {
	Path string `json:"path"`
}

// PromoteEntry is either a move or a keep. A move maps Src to Dst, both
// relative to MEDIA_ROOT, with an optional segment Title for an .mkv. A keep
// names a video that stays where it is, on purpose.
type PromoteEntry struct {
	Src   string `json:"src"`
	Dst   string `json:"dst"`
	Title string `json:"title"`
	Keep  string `json:"keep"`
}

// ParseArchiveManifest reads an archive manifest: a JSON array of
// {"path": ...}. An unknown key is an error rather than a silently ignored
// typo, and every error names the entry.
func ParseArchiveManifest(data []byte) ([]ArchiveEntry, error) {
	return decodeEntries(data, func(e ArchiveEntry) error {
		if e.Path == "" {
			return errors.New(`"path" is required`)
		}
		return nil
	})
}

// ParsePromoteManifest reads a promote manifest: a JSON array of moves
// ({"src", "dst", optional "title"}) and keeps ({"keep"}).
func ParsePromoteManifest(data []byte) ([]PromoteEntry, error) {
	return decodeEntries(data, func(e PromoteEntry) error {
		if e.Keep != "" {
			if e.Src != "" || e.Dst != "" || e.Title != "" {
				return errors.New(`"keep" cannot be combined with "src", "dst" or "title"`)
			}
			return nil
		}
		if e.Src == "" || e.Dst == "" {
			return errors.New(`a move needs both "src" and "dst", and a keep needs "keep"`)
		}
		return nil
	})
}

func decodeEntries[T any](data []byte, check func(T) error) ([]T, error) {
	var raw []json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("the manifest must be a JSON array of entries: %w", err)
	}
	entries := make([]T, len(raw))
	for i, r := range raw {
		dec := json.NewDecoder(bytes.NewReader(r))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&entries[i]); err != nil {
			return nil, fmt.Errorf("entry %d: %w", i, err)
		}
		if err := check(entries[i]); err != nil {
			return nil, fmt.Errorf("entry %d: %w", i, err)
		}
	}
	return entries, nil
}
