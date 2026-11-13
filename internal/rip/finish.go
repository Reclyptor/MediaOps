package rip

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/reclyptor/mediaops/internal/health"
	"github.com/reclyptor/mediaops/internal/makemkv"
	"github.com/reclyptor/mediaops/internal/runstore"
	"github.com/reclyptor/mediaops/internal/verify"
	"github.com/reclyptor/mediaops/kit/ffprobe"
	"github.com/reclyptor/mediaops/kit/fsutil"
	"github.com/reclyptor/mediaops/kit/mkvtool"
)

// finishInput is what a finished makemkvcon leaves to be judged. forced is a
// failure known before looking at the files, such as the watchdog's kill.
type finishInput struct {
	info     *makemkv.Parser
	infoExit int
	rip      *makemkv.Parser
	ripExit  int
	forced   string
	suspects []health.Event
}

// measuredFile is one ripped .mkv as found on disk.
type measuredFile struct {
	name    string
	seconds float64
	bytes   int64
	err     error // ffprobe could not read it
}

// finish verifies a copy and, if it holds up, promotes it out of .partial:
// titles, manifest, modes, rename, eject. A copy that verifies but was made
// under USB errors is kept, untitled, for the owner to judge.
func (w *worker) finish(in finishInput) result {
	partial := w.partial()
	total := in.rip.Progress().Max
	w.snap.progress = &runstore.Progress{Phase: "verifying", Current: total, Total: total}
	w.snap.errors = in.rip.Errors()
	w.publish()

	ctx := w.ctx
	if in.forced != "" {
		// The kill that forced this is spent; ffprobe on what was written must
		// still run to say what the rest of the failure was.
		ctx = context.WithoutCancel(ctx)
	}
	files, err := w.measure(ctx, partial)
	if err != nil {
		return w.failed(fmt.Sprintf("%v (kept at %s)", err, partial))
	}
	w.snap.titles.Done = len(files)

	reasons := verify.Check(verify.Input{
		Info: in.info, InfoExit: in.infoExit, Rip: in.rip, RipExit: in.ripExit,
		Files: fileInfos(files), MinTitleSeconds: w.minTitleSeconds,
	})
	if in.forced != "" {
		reasons = slices.Insert(reasons, 0, in.forced)
	}
	if len(reasons) > 0 {
		return w.failed(fmt.Sprintf("%s (kept at %s)", strings.Join(reasons, "; "), partial))
	}

	m, err := w.buildManifest(in, files)
	if err != nil {
		return w.failed(fmt.Sprintf("%v (kept at %s)", err, partial))
	}
	if len(in.suspects) > 0 {
		if err := fsutil.WriteJSON(filepath.Join(partial, manifestFile), m); err != nil {
			return w.failed(fmt.Sprintf("cannot write the manifest: %v (kept at %s)", err, partial))
		}
		return w.result(runstore.DiscSuspect, fmt.Sprintf(
			"rip verified, but the USB tree reported %d error(s) under this drive; kept at %s for review",
			len(in.suspects), partial))
	}

	for _, t := range m.Titles {
		if err := mkvtool.SetTitle(w.ctx, w.runner, filepath.Join(partial, t.File), t.SegmentTitle); err != nil {
			return w.failed(fmt.Sprintf("cannot set the segment title: %v (kept at %s)", err, partial))
		}
	}
	if err := fsutil.WriteJSON(filepath.Join(partial, manifestFile), m); err != nil {
		return w.failed(fmt.Sprintf("cannot write the manifest: %v (kept at %s)", err, partial))
	}
	if err := fsutil.ApplyLibraryModes(partial); err != nil {
		return w.failed(fmt.Sprintf("cannot apply the library modes: %v (kept at %s)", err, partial))
	}
	if err := w.promote(partial); err != nil {
		return w.failed(err.Error())
	}

	res := w.result(runstore.DiscDone, "")
	res.eject = w.eject()
	return res
}

// measure reads the duration of every .mkv in dir. A file ffprobe cannot read
// is recorded with its error, not skipped: it fails the verification.
func (w *worker) measure(ctx context.Context, dir string) ([]measuredFile, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("cannot list %s: %w", dir, err)
	}
	var files []measuredFile
	for _, e := range entries {
		if !e.Type().IsRegular() || !strings.HasSuffix(strings.ToLower(e.Name()), ".mkv") {
			continue
		}
		f := measuredFile{name: e.Name()}
		if info, err := e.Info(); err == nil {
			f.bytes = info.Size()
		}
		f.seconds, f.err = ffprobe.Duration(ctx, w.runner, filepath.Join(dir, e.Name()))
		files = append(files, f)
	}
	return files, nil
}

func fileInfos(files []measuredFile) map[string]verify.FileInfo {
	m := make(map[string]verify.FileInfo, len(files))
	for _, f := range files {
		m[f.name] = verify.FileInfo{Seconds: f.seconds, Err: f.err}
	}
	return m
}

// promote renames the partial folder to its destination, which must still not
// exist: something that appeared during the rip is never overwritten.
func (w *worker) promote(partial string) error {
	if _, err := os.Lstat(w.dest); err == nil {
		return fmt.Errorf("destination appeared during the rip: %s", w.dest)
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("cannot check the destination %s: %w", w.dest, err)
	}
	if err := os.Rename(partial, w.dest); err != nil {
		return fmt.Errorf("cannot move %s to %s: %w", partial, w.dest, err)
	}
	return nil
}

// eject opens the tray. Its failure is recorded and never fails a rip that
// verified and was promoted.
func (w *worker) eject() *runstore.Eject {
	// The disc is already promoted; stopping the run must not leave it locked in.
	if err := w.drives.Eject(context.WithoutCancel(w.ctx), w.drive); err != nil {
		return &runstore.Eject{Detail: err.Error()}
	}
	return &runstore.Eject{OK: true, Detail: "tray opened"}
}
