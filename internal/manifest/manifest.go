// Package manifest is mkv-manifest's core: the op manifest schema, the overlay
// checker that replays a manifest against the real tree without touching it,
// the journal that makes a run resumable, the in-doubt assessment, and the
// generators. It performs no move itself and holds no call that removes or
// renames anything; the executor goes through internal/library.
package manifest

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/unix"

	"github.com/reclyptor/mediaops/internal/cli"
	"github.com/reclyptor/mediaops/internal/library"
	"github.com/reclyptor/mediaops/kit/paths"
	"github.com/reclyptor/mediaops/kit/proc"
)

// Kinds an op can have.
const (
	KindRename       = "rename"
	KindRenameDir    = "rename_dir"
	KindMoveDir      = "move_dir"
	KindArchiveFile  = "archive_file"
	KindArchiveDir   = "archive_dir"
	KindRemux        = "remux"
	KindTitle        = "title"
	KindFlags        = "flags"
	KindAudioLang    = "audio_lang"
	KindRmdirIfEmpty = "rmdir_if_empty"
)

// Config is what the manifest tool touches outside itself.
type Config struct {
	Paths paths.Config

	// LockDir holds the per-manifest lock files. Empty means /tmp, which is
	// pod-local on purpose, like the drive lease.
	LockDir string

	// Now stamps journal records and generated manifests. Nil means time.Now.
	Now func() time.Time

	// Version is written into the journal's start record.
	Version string

	// Runner starts mkvtoolnix, ffprobe and ffmpeg for exec. Nil is only good
	// for check, gen and a dry run; DefaultConfig sets the real one.
	Runner proc.Runner

	// Mover moves the files and trees exec relocates. The zero value is real.
	Mover library.Mover
}

// DefaultConfig is the real machine: MEDIA_ROOT and ARCHIVE_ROOT from the
// environment and the real mkvtoolnix and ffmpeg.
func DefaultConfig() Config {
	return Config{Paths: paths.FromEnv(), Runner: &proc.Exec{}, Version: cli.Version}
}

func (c Config) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

// Manifest is a parsed manifest file.
type Manifest struct {
	// Path is the absolute path the manifest was loaded from.
	Path string
	// SHA256 is the hex digest of the file's bytes; the journal is bound to it.
	SHA256 string
	// Name and Note are the optional top-level strings.
	Name, Note string
	Ops        []Op
}

// Op is one step. Only the fields its Kind uses are set; a field the manifest
// omitted is the zero value, except SubDefault, where nil is an explicit
// `null` (no default subtitle) on a flags op that has passed Load.
type Op struct {
	// N is the 1-based position in the manifest.
	N    int
	Kind string

	Src, Dst, ArchiveDst, Path, Title string
	Size                              *int64

	AudioDefault int
	SubDefault   *int
	Langs        map[int]string

	// Why and Note are the free-text annotations every op may carry.
	Why, Note string
}

// IsDirMove reports whether the op moves a directory tree.
func (o Op) IsDirMove() bool {
	return o.Kind == KindRenameDir || o.Kind == KindMoveDir || o.Kind == KindArchiveDir
}

// IsFileMove reports whether the op moves one file: rename, archive_file and
// remux (which also writes a new file at Dst).
func (o Op) IsFileMove() bool {
	return o.Kind == KindRename || o.Kind == KindArchiveFile || o.Kind == KindRemux
}

// IsArchive reports whether the op writes into ARCHIVE_ROOT.
func (o Op) IsArchive() bool {
	return o.Kind == KindArchiveFile || o.Kind == KindArchiveDir || o.Kind == KindRemux
}

// Finding is one problem a check found. Op is the 1-based position in the
// manifest, nil for a whole-manifest or --films finding.
type Finding struct {
	Op      *int   `json:"op"`
	Kind    string `json:"kind,omitempty"`
	Path    string `json:"path,omitempty"`
	Problem string `json:"problem"`
}

// opFinding is a finding about op n.
func opFinding(n int, kind, path, problem string) Finding {
	return Finding{Op: &n, Kind: kind, Path: path, Problem: problem}
}

// readManifestFile reads a regular file, following a symlink to one. The type
// is checked before opening so a FIFO cannot block, and again on the open file
// so a path swapped in between is still refused; /dev/zero never gets read.
func readManifestFile(abs string) ([]byte, error) {
	info, err := os.Stat(abs)
	if err != nil {
		return nil, cli.Usage(fmt.Errorf("cannot read the manifest: %w", err))
	}
	if !info.Mode().IsRegular() {
		return nil, cli.Usage(fmt.Errorf("cannot read the manifest: %s is not a regular file (%s)", abs, kindOf(info.Mode())))
	}
	f, err := os.OpenFile(abs, os.O_RDONLY|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, cli.Usage(fmt.Errorf("cannot read the manifest: %w", err))
	}
	defer f.Close()
	info, err = f.Stat()
	if err != nil {
		return nil, cli.Usage(fmt.Errorf("cannot read the manifest: %w", err))
	}
	if !info.Mode().IsRegular() {
		return nil, cli.Usage(fmt.Errorf("cannot read the manifest: %s is not a regular file (%s)", abs, kindOf(info.Mode())))
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return nil, cli.Usage(fmt.Errorf("cannot read the manifest: %w", err))
	}
	return data, nil
}

// Load reads a manifest. A manifest that cannot be read, is not JSON, is not an
// object, or has no `ops` array is an exit-2 error; every other schema problem
// is a Finding, so one run reports them all. An op that drew findings is still
// in Ops, as far as it could be read.
func Load(path string) (*Manifest, []Finding, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, nil, cli.Usage(fmt.Errorf("cannot resolve %s: %w", path, err))
	}
	data, err := readManifestFile(abs)
	if err != nil {
		return nil, nil, err
	}
	sum := sha256.Sum256(data)
	m := &Manifest{Path: abs, SHA256: hex.EncodeToString(sum[:])}

	var top map[string]json.RawMessage
	if err := json.Unmarshal(data, &top); err != nil {
		var typeErr *json.UnmarshalTypeError
		if errors.As(err, &typeErr) {
			return nil, nil, cli.Usage(fmt.Errorf("%s is not a JSON object", abs))
		}
		return nil, nil, cli.Usage(fmt.Errorf("%s is not valid JSON: %w", abs, err))
	}
	if top == nil {
		return nil, nil, cli.Usage(fmt.Errorf("%s is not a JSON object", abs))
	}
	rawOps, ok := top["ops"]
	if !ok {
		return nil, nil, cli.Usage(fmt.Errorf(`%s has no "ops" array`, abs))
	}
	var ops []json.RawMessage
	if err := json.Unmarshal(rawOps, &ops); err != nil || ops == nil {
		return nil, nil, cli.Usage(fmt.Errorf(`"ops" in %s is not an array`, abs))
	}

	findings := []Finding{}
	findings = append(findings, parseTop(m, top)...)
	m.Ops = make([]Op, len(ops))
	for i, raw := range ops {
		op, fs := parseOp(i+1, raw)
		m.Ops[i] = op
		findings = append(findings, fs...)
	}
	return m, findings, nil
}

// JournalPath is where the journal for the manifest at manifestPath lives:
// beside it, named for the file without its .json.
func JournalPath(manifestPath string) string {
	return stem(manifestPath) + ".journal.jsonl"
}

// stem is the manifest path without a trailing .json.
func stem(manifestPath string) string {
	if filepath.Ext(manifestPath) == ".json" {
		return manifestPath[:len(manifestPath)-len(".json")]
	}
	return manifestPath
}
