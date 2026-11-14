package manifest

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"golang.org/x/sys/unix"

	"github.com/reclyptor/mediaops/internal/cli"
	"github.com/reclyptor/mediaops/kit/fsutil"
)

// Journal event names.
const (
	eventStart  = "start"
	eventBegin  = "begin"
	eventDone   = "done"
	eventFailed = "failed"
	eventFinish = "finish"
	eventRepair = "repair"
)

// journalRecord is every field any journal line can carry, for reading.
type journalRecord struct {
	Event  string      `json:"event"`
	Op     int         `json:"op"`
	SHA256 string      `json:"manifest_sha256"`
	State  *BeginState `json:"state"`
}

type startRecord struct {
	Event       string `json:"event"`
	ManifestSHA string `json:"manifest_sha256"`
	Ops         int    `json:"ops"`
	At          string `json:"at"`
	ToolVersion string `json:"tool_version"`
	PID         int    `json:"pid"`
}

type beginRecord struct {
	Event string     `json:"event"`
	Op    int        `json:"op"`
	Kind  string     `json:"kind"`
	At    string     `json:"at"`
	State BeginState `json:"state"`
}

type doneRecord struct {
	Event  string `json:"event"`
	Op     int    `json:"op"`
	At     string `json:"at"`
	Bytes  int64  `json:"bytes"`
	Detail string `json:"detail,omitempty"`
}

type failedRecord struct {
	Event string `json:"event"`
	Op    int    `json:"op"`
	At    string `json:"at"`
	Error string `json:"error"`
}

type finishRecord struct {
	Event   string `json:"event"`
	At      string `json:"at"`
	Applied int    `json:"applied"`
}

type repairRecord struct {
	Event     string `json:"event"`
	At        string `json:"at"`
	TornBytes int    `json:"torn_bytes"`
}

// ReadJournal loads m's journal. A manifest that has never run has no journal
// file and gets an empty Journal. A journal that cannot be trusted to say
// which ops are done is refused, because resuming from a wrong picture would
// redo or skip ops.
func ReadJournal(m *Manifest) (*Journal, error) {
	old := stem(m.Path) + ".journal"
	if _, err := os.Lstat(old); err == nil {
		return nil, cli.Refused(fmt.Errorf("%s is a Python-era journal; resuming it by index would redo ops", old))
	}

	path := JournalPath(m.Path)
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return &Journal{done: map[int]bool{}, doubt: map[int]BeginState{}}, nil
	}
	if err != nil {
		return nil, cli.Refused(fmt.Errorf("cannot read the journal %s: %w", path, err))
	}
	if !info.Mode().IsRegular() {
		return nil, cli.Refused(fmt.Errorf("%s is not a regular file (%s); refusing to read it as a journal", path, kindOf(info.Mode())))
	}
	data, err := readJournalFile(path)
	if err != nil {
		return nil, err
	}

	type line struct {
		no  int
		rec journalRecord
		ok  bool
	}
	var lines []line
	for i, raw := range bytes.Split(data, []byte("\n")) {
		if len(bytes.TrimSpace(raw)) == 0 {
			continue
		}
		l := line{no: i + 1}
		l.ok = json.Unmarshal(raw, &l.rec) == nil
		lines = append(lines, l)
	}

	j := &Journal{done: map[int]bool{}, doubt: map[int]BeginState{}}
	started := false
	changed := false
	for i, l := range lines {
		if !l.ok {
			if i == len(lines)-1 {
				break
			}
			if next := lines[i+1]; next.ok && next.rec.Event == eventRepair {
				continue
			}
			return nil, cli.Refused(fmt.Errorf("journal %s damaged at line %d", path, l.no))
		}
		rec := l.rec
		if rec.Event == eventRepair {
			continue
		}
		if rec.Event == eventStart {
			if rec.SHA256 != m.SHA256 {
				changed = true
			}
			started = true
			continue
		}
		if !started {
			return nil, cli.Refused(fmt.Errorf("journal %s damaged at line %d: %q record before the first start", path, l.no, rec.Event))
		}
		switch rec.Event {
		case eventBegin, eventDone, eventFailed:
			if rec.Op < 1 || (!changed && rec.Op > len(m.Ops)) {
				return nil, cli.Refused(fmt.Errorf("journal %s damaged at line %d: op %d is outside 1..%d", path, l.no, rec.Op, len(m.Ops)))
			}
		case eventFinish:
			continue
		default:
			return nil, cli.Refused(fmt.Errorf("journal %s damaged at line %d: unknown event %q", path, l.no, rec.Event))
		}
		switch rec.Event {
		case eventDone:
			j.done[rec.Op] = true
			delete(j.doubt, rec.Op)
		case eventBegin:
			if !j.done[rec.Op] {
				var st BeginState
				if rec.State != nil {
					st = *rec.State
				}
				j.doubt[rec.Op] = st
			}
		}
	}
	if changed {
		return nil, cli.Refused(fmt.Errorf("journal %s belongs to a different manifest: the manifest changed after %d ops were applied", path, j.DoneCount()))
	}
	return j, nil
}

// readJournalFile reads the journal without following a symlink and checks the
// open file again, so a path swapped after the Lstat is still refused.
func readJournalFile(path string) ([]byte, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, cli.Refused(fmt.Errorf("cannot read the journal %s: %w", path, err))
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, cli.Refused(fmt.Errorf("cannot read the journal %s: %w", path, err))
	}
	if !info.Mode().IsRegular() {
		return nil, cli.Refused(fmt.Errorf("%s is not a regular file (%s); refusing to read it as a journal", path, kindOf(info.Mode())))
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return nil, cli.Refused(fmt.Errorf("cannot read the journal %s: %w", path, err))
	}
	return data, nil
}

// JournalWriter appends records to a manifest's journal. The journal is never
// rewritten: every record is one write of a whole line, then a sync.
type JournalWriter struct {
	mu   sync.Mutex
	f    *os.File
	m    *Manifest
	c    Config
	path string
}

// OpenJournal opens m's journal for appending, creating it when missing. A
// journal whose last record was cut short is first closed with a newline and a
// repair record, since nothing in it may be truncated; ReadJournal accepts the
// torn line only because that record follows it. OpenJournal does not check
// the journal against the manifest; ReadJournal does.
func OpenJournal(m *Manifest, c Config) (*JournalWriter, error) {
	path := JournalPath(m.Path)
	info, statErr := os.Lstat(path)
	created := errors.Is(statErr, os.ErrNotExist)
	if statErr == nil && !info.Mode().IsRegular() {
		return nil, cli.Refused(fmt.Errorf("the journal cannot be created: %s is not a regular file (%s)", path, kindOf(info.Mode())))
	}

	f, err := os.OpenFile(path, os.O_RDWR|os.O_APPEND|os.O_CREATE|unix.O_NOFOLLOW|unix.O_NONBLOCK, fsutil.FileMode)
	if err != nil {
		return nil, cli.Refused(fmt.Errorf("the journal cannot be created: %w", err))
	}
	if fi, err := f.Stat(); err != nil || !fi.Mode().IsRegular() {
		f.Close()
		if err != nil {
			return nil, cli.Refused(fmt.Errorf("the journal cannot be created: %w", err))
		}
		return nil, cli.Refused(fmt.Errorf("the journal cannot be created: %s is not a regular file (%s)", path, kindOf(fi.Mode())))
	}
	w := &JournalWriter{f: f, m: m, c: c, path: path}
	fail := func(err error) (*JournalWriter, error) {
		f.Close()
		return nil, cli.Refused(fmt.Errorf("the journal cannot be created: %w", err))
	}
	if created {
		if err := f.Chmod(fsutil.FileMode); err != nil {
			return fail(fmt.Errorf("setting the mode of %s: %w", path, err))
		}
		if err := fsutil.SyncDir(filepath.Dir(path)); err != nil {
			return fail(fmt.Errorf("syncing the directory of %s: %w", path, err))
		}
	}

	torn, err := tornBytes(f)
	if err != nil {
		f.Close()
		return nil, fmt.Errorf("reading the tail of %s: %w", path, err)
	}
	if torn > 0 {
		rec, err := json.Marshal(repairRecord{Event: eventRepair, At: w.stamp(), TornBytes: torn})
		if err != nil {
			f.Close()
			return nil, err
		}
		if err := w.write(append(append([]byte("\n"), rec...), '\n')); err != nil {
			f.Close()
			return nil, err
		}
	}
	return w, nil
}

// tornBytes counts the bytes after the file's last newline.
func tornBytes(f *os.File) (int, error) {
	size, err := f.Seek(0, io.SeekEnd)
	if err != nil {
		return 0, err
	}
	const chunk = 4096
	buf := make([]byte, chunk)
	end := size
	for end > 0 {
		start := max(end-chunk, 0)
		b := buf[:end-start]
		if _, err := f.ReadAt(b, start); err != nil {
			return 0, err
		}
		if i := bytes.LastIndexByte(b, '\n'); i >= 0 {
			return int(size - (start + int64(i) + 1)), nil
		}
		end = start
	}
	return int(size), nil
}

func (w *JournalWriter) stamp() string {
	return w.c.now().UTC().Format(time.RFC3339)
}

// write appends data in one Write and syncs it.
func (w *JournalWriter) write(data []byte) error {
	if _, err := w.f.Write(data); err != nil {
		return fmt.Errorf("writing %s: %w", w.path, err)
	}
	if err := w.f.Sync(); err != nil {
		return fmt.Errorf("syncing %s: %w", w.path, err)
	}
	return nil
}

func (w *JournalWriter) append(rec any) error {
	data, err := json.Marshal(rec)
	if err != nil {
		return fmt.Errorf("encoding a journal record: %w", err)
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.write(append(data, '\n'))
}

// Start records a run beginning against the manifest's current digest.
func (w *JournalWriter) Start() error {
	return w.append(startRecord{
		Event:       eventStart,
		ManifestSHA: w.m.SHA256,
		Ops:         len(w.m.Ops),
		At:          w.stamp(),
		ToolVersion: w.c.Version,
		PID:         os.Getpid(),
	})
}

// Begin records op about to run, with the state needed to settle it if the run
// dies mid-op.
func (w *JournalWriter) Begin(op Op, st BeginState) error {
	return w.append(beginRecord{Event: eventBegin, Op: op.N, Kind: op.Kind, At: w.stamp(), State: st})
}

// Done records op n complete.
func (w *JournalWriter) Done(n int, bytes int64, detail string) error {
	return w.append(doneRecord{Event: eventDone, Op: n, At: w.stamp(), Bytes: bytes, Detail: detail})
}

// Failed records op n failing with cause. The op stays in doubt.
func (w *JournalWriter) Failed(n int, cause error) error {
	msg := ""
	if cause != nil {
		msg = cause.Error()
	}
	return w.append(failedRecord{Event: eventFailed, Op: n, At: w.stamp(), Error: msg})
}

// Finish records the run ending with applied ops done in it.
func (w *JournalWriter) Finish(applied int) error {
	return w.append(finishRecord{Event: eventFinish, At: w.stamp(), Applied: applied})
}

// Close closes the journal file.
func (w *JournalWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.f.Close()
}
