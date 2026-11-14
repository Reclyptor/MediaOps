package manifest

import (
	"fmt"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/sys/unix"

	"github.com/reclyptor/mediaops/internal/cli"
)

// CheckOptions are the switches of a check.
type CheckOptions struct {
	// Films adds the post-state audit of every Movies/ folder the manifest touches.
	Films bool
}

// InDoubt is an op the journal began and never finished, with what the tree
// says it needs.
type InDoubt struct {
	Op      int     `json:"op"`
	Kind    string  `json:"kind"`
	Outcome Outcome `json:"outcome"`
	Reason  string  `json:"reason,omitempty"`
}

// Source is a directory ops read from and how many files they take from it.
type Source struct {
	Dir   string `json:"dir"`
	Files int    `json:"files"`
}

// Note is something worth knowing that is not a finding.
type Note struct {
	Op   int    `json:"op"`
	Kind string `json:"kind"`
	Path string `json:"path"`
	Note string `json:"note"`
}

// CheckReport is what a check prints. Clean is no findings.
type CheckReport struct {
	Manifest string         `json:"manifest"`
	SHA256   string         `json:"sha256"`
	Ops      int            `json:"ops"`
	Kinds    map[string]int `json:"kinds"`
	Done     int            `json:"done"`
	Pending  int            `json:"pending"`
	InDoubt  []InDoubt      `json:"in_doubt"`
	Sources  []Source       `json:"sources"`
	Bytes    int64          `json:"bytes"`
	Notes    []Note         `json:"notes"`
	Findings []Finding      `json:"findings"`
	Films    bool           `json:"films"`
	Clean    bool           `json:"clean"`
}

// Check replays the pending ops of m, in order, on an overlay of the real
// tree and collects every problem. schema is what Load found. j says which ops
// are done (skipped) and which are in doubt (assessed from the tree); nil is a
// manifest that never ran. Nothing is written. A manifest with archive ops
// needs ARCHIVE_ROOT to pass its guard, else the error is a refusal.
func (c Config) Check(m *Manifest, schema []Finding, j *Journal, opts CheckOptions) (CheckReport, error) {
	var archiveRoot string
	for _, op := range m.Ops {
		if op.IsArchive() {
			root, err := c.Paths.ArchiveRoot()
			if err != nil {
				return CheckReport{}, cli.Refused(err)
			}
			archiveRoot = root
			break
		}
	}

	r := &replay{
		c: c, m: m, j: j, archiveRoot: archiveRoot,
		ov:      newOverlay(),
		claimed: map[string]int{},
		sources: map[string]int{},
		rep: CheckReport{
			Manifest: m.Path, SHA256: m.SHA256, Ops: len(m.Ops), Kinds: map[string]int{},
			InDoubt: []InDoubt{}, Sources: []Source{}, Notes: []Note{}, Findings: []Finding{},
			Films: opts.Films,
		},
	}
	r.run(schema)
	if opts.Films {
		r.rep.Findings = append(r.rep.Findings, c.auditFilms(m, r.ov)...)
	}
	r.rep.Clean = len(r.rep.Findings) == 0
	return r.rep, nil
}

type replay struct {
	c           Config
	m           *Manifest
	j           *Journal
	archiveRoot string
	ov          *overlay
	claimed     map[string]int
	sources     map[string]int
	rep         CheckReport
	cur         Op
}

func (r *replay) run(schema []Finding) {
	byOp := map[int][]Finding{}
	for _, f := range schema {
		if f.Op == nil {
			r.rep.Findings = append(r.rep.Findings, f)
			continue
		}
		byOp[*f.Op] = append(byOp[*f.Op], f)
	}

	for _, op := range r.m.Ops {
		r.rep.Kinds[op.Kind]++
		if r.j.Done(op.N) {
			r.rep.Done++
			continue
		}
		r.rep.Pending++
		r.cur = op

		hadSchema := len(byOp[op.N]) > 0
		r.rep.Findings = append(r.rep.Findings, byOp[op.N]...)
		if hadSchema || !r.contained(op) {
			continue
		}
		r.checkNames(op)

		a := Assessment{Outcome: Redo}
		if st, doubt := r.j.InDoubt(op.N); doubt {
			a = Assess(op, st)
			r.rep.InDoubt = append(r.rep.InDoubt, InDoubt{Op: op.N, Kind: op.Kind, Outcome: a.Outcome, Reason: a.Reason})
		}
		switch op.Kind {
		case KindRename, KindArchiveFile, KindRemux:
			r.fileMove(op, a)
		case KindRenameDir, KindMoveDir, KindArchiveDir:
			r.dirMove(op, a)
		case KindTitle, KindFlags, KindAudioLang:
			if !r.ov.isFile(op.Path) {
				r.bad(op.Path, "%s target is not a file at this point", op.Kind)
			}
		case KindRmdirIfEmpty:
			r.rmdir(op)
		}
	}

	r.rep.Sources = make([]Source, 0, len(r.sources))
	for dir, n := range r.sources {
		r.rep.Sources = append(r.rep.Sources, Source{Dir: dir, Files: n})
	}
	sort.Slice(r.rep.Sources, func(i, k int) bool { return r.rep.Sources[i].Dir < r.rep.Sources[k].Dir })
}

func (r *replay) bad(path, format string, args ...any) {
	r.rep.Findings = append(r.rep.Findings, opFinding(r.cur.N, r.cur.Kind, path, fmt.Sprintf(format, args...)))
}

func (r *replay) note(path, note string) {
	r.rep.Notes = append(r.rep.Notes, Note{Op: r.cur.N, Kind: r.cur.Kind, Path: path, Note: note})
}

// contained holds every path to the tree it may touch: sources, targets and
// ordinary destinations to MEDIA_ROOT, archive destinations to ARCHIVE_ROOT,
// and rmdir away from the roots a removal must never reach.
func (r *replay) contained(op Op) bool {
	media := filepath.Clean(r.c.Paths.MediaRoot)
	ok := true
	in := func(field, p string) {
		if p != "" && !strictlyWithin(media, p) {
			r.bad(p, "%s is outside MEDIA_ROOT (%s)", field, media)
			ok = false
		}
	}
	inArchive := func(field, p string) {
		if p != "" && !strictlyWithin(r.archiveRoot, p) {
			r.bad(p, "%s is outside ARCHIVE_ROOT (%s)", field, r.archiveRoot)
			ok = false
		}
	}
	in("src", op.Src)
	in("path", op.Path)
	if op.Kind == KindArchiveFile || op.Kind == KindArchiveDir {
		inArchive("dst", op.Dst)
	} else {
		in("dst", op.Dst)
	}
	if op.Kind == KindRemux {
		inArchive("archive_dst", op.ArchiveDst)
	}
	if op.Kind == KindRmdirIfEmpty && ok {
		switch {
		case filepath.Dir(op.Path) == media:
			r.bad(op.Path, "rmdir_if_empty never removes a share")
			ok = false
		case op.Path == filepath.Clean(r.c.Paths.Staging()):
			r.bad(op.Path, "rmdir_if_empty never removes the staging tree")
			ok = false
		}
	}
	return ok
}

// checkNames is the static stand-in for the Python's touch test: a name the
// filesystem or the safe copy would reject.
func (r *replay) checkNames(op Op) {
	for _, p := range []string{op.Dst, op.ArchiveDst} {
		if p == "" {
			continue
		}
		if problem := badName(filepath.Base(p)); problem != "" {
			r.bad(p, "destination name %s", problem)
		}
	}
}

func badName(name string) string {
	switch {
	case len(name) > 255:
		return fmt.Sprintf("is %d bytes; the limit is 255", len(name))
	case !utf8.ValidString(name):
		return "is not valid UTF-8"
	case strings.HasSuffix(name, ".partial"):
		return "ends in .partial, which is reserved for copies in flight"
	}
	for _, r := range name {
		if unicode.IsControl(r) {
			return fmt.Sprintf("contains the control character %U", r)
		}
	}
	return ""
}

// claim records dst as taken by this op, and reports a second claimant.
func (r *replay) claim(field, dst string) {
	if first, ok := r.claimed[dst]; ok {
		if first == r.cur.N {
			r.bad(dst, "%s and another destination of this op are the same path", field)
		} else {
			r.bad(dst, "%s is also claimed by op %d", field, first)
		}
		return
	}
	r.claimed[dst] = r.cur.N
}

// checkParent holds the nearest existing ancestor of dst to be a directory the
// run can write into.
func (r *replay) checkParent(field, dst string) {
	a := filepath.Dir(dst)
	for !r.ov.exists(a) && a != filepath.Dir(a) {
		a = filepath.Dir(a)
	}
	switch {
	case !r.ov.isDir(a):
		r.bad(dst, "%s ancestor is not a directory: %s", field, a)
	case realDir(a) && unix.Access(a, unix.W_OK|unix.X_OK) != nil:
		r.bad(dst, "%s ancestor is not writable: %s", field, a)
	}
}

func (r *replay) fileMove(op Op, a Assessment) {
	r.claim("dst", op.Dst)
	if op.Kind == KindRemux {
		r.claim("archive_dst", op.ArchiveDst)
	}
	if a.Outcome == Conflict {
		r.conflict(op, a)
		return
	}
	if a.Outcome == Settled {
		r.ov.hide(op.Src)
		r.ov.added[op.Dst] = entry{size: r.ov.size(op.Dst)}
		if op.Kind == KindRemux {
			r.ov.added[op.ArchiveDst] = entry{size: r.ov.size(op.ArchiveDst)}
		}
		return
	}

	if !r.ov.isFile(op.Src) {
		r.bad(op.Src, "%s src is not a file at this point", op.Kind)
		return
	}
	size := r.ov.size(op.Src)
	if op.Size != nil && *op.Size != size {
		r.bad(op.Src, "%s src size %d is not the recorded %d", op.Kind, size, *op.Size)
	}
	if a.Outcome == Resume {
		// A remux whose output is already in place: only the archiving is left.
		r.checkParent("archive_dst", op.ArchiveDst)
		r.account(filepath.Dir(op.Src), 1, size)
		r.ov.moveFile(op.Src, op.ArchiveDst)
		return
	}
	if r.ov.exists(op.Dst) {
		r.bad(op.Dst, "%s dst already exists", op.Kind)
	}
	r.checkParent("dst", op.Dst)
	if op.Kind == KindRemux {
		if r.ov.exists(op.ArchiveDst) {
			r.bad(op.ArchiveDst, "remux archive_dst already exists")
		}
		r.checkParent("archive_dst", op.ArchiveDst)
	}
	r.account(filepath.Dir(op.Src), 1, size)
	r.ov.moveFile(op.Src, op.Dst)
	if op.Kind == KindRemux {
		r.ov.added[op.ArchiveDst] = entry{size: size}
	}
}

func (r *replay) dirMove(op Op, a Assessment) {
	r.claim("dst", op.Dst)
	switch a.Outcome {
	case Conflict:
		r.conflict(op, a)
		return
	case Settled:
		r.ov.hide(op.Src)
		return
	case Resume:
		files := r.ov.filesUnder(op.Src)
		r.account(op.Src, len(files), sum(files))
		r.ov.moveDir(op.Src, op.Dst, true)
		return
	}

	if !r.ov.isDir(op.Src) {
		r.bad(op.Src, "%s src is not a directory at this point", op.Kind)
		return
	}
	if strictlyWithin(op.Src, op.Dst) {
		r.bad(op.Dst, "%s dst is inside its own src %s", op.Kind, op.Src)
	}
	if r.ov.exists(op.Dst) {
		r.bad(op.Dst, "%s dst already exists", op.Kind)
	}
	r.checkParent("dst", op.Dst)
	r.checkTreeMovable(op.Src)
	files := r.ov.filesUnder(op.Src)
	r.account(op.Src, len(files), sum(files))
	r.ov.moveDir(op.Src, op.Dst, false)
}

// checkTreeMovable reports every symlink and special file under dir, because
// the move refuses a tree that holds one. Only the real tree is walked:
// overlay-added entries are always regular files.
func (r *replay) checkTreeMovable(dir string) {
	if !realDir(dir) {
		return
	}
	_ = filepath.WalkDir(dir, func(p string, e fs.DirEntry, err error) error {
		switch {
		case err != nil:
			r.bad(p, "%s cannot read %s: %v", r.cur.Kind, p, err)
			return nil
		case r.ov.hidden(p):
			if e.IsDir() {
				return fs.SkipDir
			}
		case e.IsDir() || e.Type().IsRegular():
		case e.Type()&fs.ModeSymlink != 0:
			r.bad(p, "%s src holds a symlink, %s; a tree holding one is never moved", r.cur.Kind, p)
		default:
			r.bad(p, "%s src holds a special file, %s (%s); a tree holding one is never moved", r.cur.Kind, p, e.Type())
		}
		return nil
	})
}

func (r *replay) rmdir(op Op) {
	switch kids := r.ov.children(op.Path); {
	case !r.ov.isDir(op.Path):
		r.note(op.Path, "already absent; the executor skips it")
	case len(kids) > 0:
		names := make([]string, 0, len(kids))
		for k := range kids {
			names = append(names, k)
		}
		sort.Strings(names)
		if len(names) > 3 {
			names = names[:3]
		}
		r.note(op.Path, fmt.Sprintf("would be skipped, still holds %s", strings.Join(names, ", ")))
	default:
		r.ov.removeDir(op.Path)
	}
}

// conflict reports an op the journal says was interrupted and the tree says
// cannot be finished without a decision.
func (r *replay) conflict(op Op, a Assessment) {
	path := op.Dst
	if path == "" {
		path = op.Path
	}
	r.bad(path, "interrupted earlier and cannot be resumed: %s", a.Reason)
}

func (r *replay) account(dir string, files int, bytes int64) {
	r.sources[dir] += files
	r.rep.Bytes += bytes
}

func sum(files map[string]int64) int64 {
	var total int64
	for _, s := range files {
		total += s
	}
	return total
}
