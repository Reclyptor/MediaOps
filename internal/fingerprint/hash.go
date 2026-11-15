package fingerprint

import (
	"context"
	"fmt"

	"github.com/reclyptor/mediaops/internal/dhash"
	"github.com/reclyptor/mediaops/kit/proc"
)

// Compute fingerprints the file at path, which must be absolute: the body is
// the whole file at BodyFPS, the preview runs from previewStart to the end at
// PreviewFPS. A track with no body frames, or a preview start that leaves no
// preview frames, is an error rather than a record that could never match.
func Compute(ctx context.Context, p proc.Piper, path string, previewStart *float64) (Record, error) {
	body, err := dhash.Sample(ctx, p, path, BodyFPS, nil)
	if err != nil {
		return Record{}, err
	}
	if len(body) == 0 {
		return Record{}, fmt.Errorf("no frames decoded from %s", path)
	}
	var preview []uint64
	if previewStart != nil {
		if preview, err = dhash.Sample(ctx, p, path, PreviewFPS, previewStart); err != nil {
			return Record{}, err
		}
		if len(preview) == 0 {
			return Record{}, fmt.Errorf("no frames after preview start %vs in %s", *previewStart, path)
		}
	}
	return NewRecord(path, previewStart, body, preview), nil
}
