package manifest

import (
	"context"
	"errors"
	"fmt"
	"math"
	"slices"

	"github.com/reclyptor/mediaops/internal/library"
	"github.com/reclyptor/mediaops/kit/ffprobe"
	"github.com/reclyptor/mediaops/kit/mkvtool"
)

// maxDurationSkew is how far a remux may move a file's length, in seconds.
const maxDurationSkew = 0.1

const partialSuffix = ".partial"

// remux copies src into dst stream for stream without re-encoding, proves the
// copy, titles it, and archives the original. The copy is written to
// dst+".partial" and only moved to dst after it has been verified and titled,
// so dst never holds an unproven file.
//
// A Resume is the tail of an interrupted remux: dst is in place and the
// source is not archived yet, so dst is verified against the source again,
// titled, and the source archived.
func (x *executor) remux(ctx context.Context, op Op, a Assessment, st BeginState) (step, error) {
	if st.Size == nil {
		return step{}, errors.New("the journal recorded no source size for this op")
	}
	size := *st.Size
	if a.Outcome == Settled {
		if _, err := requireFile(op.Dst, "destination"); err != nil {
			return step{}, err
		}
		if err := movedWhole(op.Src, op.ArchiveDst, size); err != nil {
			return step{}, err
		}
		return step{status: StatusDone, bytes: size}, nil
	}

	var detail string
	var err error
	if a.Outcome == Redo {
		detail, err = x.writeRemux(ctx, op)
	} else {
		detail, err = x.verifyRemux(ctx, op.Src, op.Dst)
		if err == nil {
			err = mkvtool.SetTitle(ctx, x.c.Runner, op.Dst, op.Title)
		}
	}
	if err != nil {
		return step{}, err
	}

	if err := x.move(ctx, op.Src, op.ArchiveDst); err != nil {
		return step{}, fmt.Errorf("archiving the source: %w", err)
	}
	if err := movedWhole(op.Src, op.ArchiveDst, size); err != nil {
		return step{}, err
	}
	if _, err := requireFile(op.Dst, "destination"); err != nil {
		return step{}, err
	}
	return step{status: StatusDone, bytes: size, detail: detail}, nil
}

// writeRemux produces dst from op.Src through a verified, titled partial. A
// partial that did not make it into place is discarded, since the source is
// untouched and the next attempt starts over.
func (x *executor) writeRemux(ctx context.Context, op Op) (detail string, err error) {
	partial := op.Dst + partialSuffix
	if err := library.DiscardPartial(partial); err != nil {
		return "", err
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, library.DiscardPartial(partial))
		}
	}()

	if err := mkdirFor(op.Dst); err != nil {
		return "", err
	}
	if err := mkvtool.Remux(ctx, x.c.Runner, op.Src, partial); err != nil {
		return "", err
	}
	detail, err = x.verifyRemux(ctx, op.Src, partial)
	if err != nil {
		return "", err
	}
	if err := mkvtool.SetTitle(ctx, x.c.Runner, partial, op.Title); err != nil {
		return "", err
	}
	if err := library.PlacePartial(op.Dst); err != nil {
		return "", fmt.Errorf("moving the remuxed file into place: %w", err)
	}
	return detail, nil
}

// signature is what makes a playable stream the same stream after a remux.
type signature struct {
	Type, Codec             string
	Channels, Width, Height int
}

var playable = []string{"video", "audio", "subtitle"}

// signatures lists the playable streams of a file, grouped by type and in
// stream order within a type, and counts the rest. Container order across
// types is the muxer's choice, not a property of the media.
func signatures(streams []ffprobe.Stream) (sigs []signature, other int) {
	for _, typ := range playable {
		for _, s := range streams {
			if s.CodecType == typ {
				sigs = append(sigs, signature{typ, s.CodecName, s.Channels, s.Width, s.Height})
			}
		}
	}
	return sigs, len(streams) - len(sigs)
}

// verifyRemux proves dst holds what src holds: the same decoded video frames
// (and at least one), the same length to within 0.1 s, the same playable
// streams, and no decode errors that src did not have. src is only decoded
// when dst already shows errors. The detail it returns notes what was left out
// of the comparison.
func (x *executor) verifyRemux(ctx context.Context, src, dst string) (string, error) {
	r := x.c.Runner
	srcFrames, _, err := ffprobe.CountFrames(ctx, r, src)
	if err != nil {
		return "", fmt.Errorf("counting the frames of the source: %w", err)
	}
	dstFrames, _, err := ffprobe.CountFrames(ctx, r, dst)
	if err != nil {
		return "", fmt.Errorf("counting the frames of the remux: %w", err)
	}
	if srcFrames == 0 || srcFrames != dstFrames {
		return "", fmt.Errorf("the remux of %s has %d video frames, the source has %d", src, dstFrames, srcFrames)
	}

	srcLen, err := ffprobe.Duration(ctx, r, src)
	if err != nil {
		return "", fmt.Errorf("reading the length of the source: %w", err)
	}
	dstLen, err := ffprobe.Duration(ctx, r, dst)
	if err != nil {
		return "", fmt.Errorf("reading the length of the remux: %w", err)
	}
	if math.Abs(srcLen-dstLen) > maxDurationSkew+1e-9 {
		return "", fmt.Errorf("the remux of %s is %.3f s long, the source is %.3f s", src, dstLen, srcLen)
	}

	srcStreams, err := ffprobe.Streams(ctx, r, src)
	if err != nil {
		return "", err
	}
	dstStreams, err := ffprobe.Streams(ctx, r, dst)
	if err != nil {
		return "", err
	}
	want, other := signatures(srcStreams)
	got, _ := signatures(dstStreams)
	if !slices.Equal(want, got) {
		return "", fmt.Errorf("the remux of %s has the streams %v, the source has %v", src, got, want)
	}

	var notes []string
	if other > 0 {
		notes = append(notes, fmt.Sprintf("%d non-media source stream(s) not compared", other))
	}
	dstErrs, err := ffprobe.DecodeErrors(ctx, r, dst)
	if err != nil {
		return "", err
	}
	if len(dstErrs) > 0 {
		srcErrs, err := ffprobe.DecodeErrors(ctx, r, src)
		if err != nil {
			return "", err
		}
		if len(dstErrs) > len(srcErrs) {
			return "", fmt.Errorf("decoding the remux of %s printed %d error line(s), the source %d; first: %s", src, len(dstErrs), len(srcErrs), dstErrs[0])
		}
		notes = append(notes, fmt.Sprintf("%d decode error line(s) were already in the source", len(dstErrs)))
	}
	return joinDetail(notes...), nil
}
