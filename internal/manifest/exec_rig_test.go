package manifest

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/reclyptor/mediaops/internal/cli"
	"github.com/reclyptor/mediaops/internal/library"
)

// xEnv is a rig whose Config runs the fake tools.
type xEnv struct {
	*rig
	fake *fakeRunner
}

func newXEnv(t *testing.T) *xEnv {
	t.Helper()
	r := newRig(t)
	f := &fakeRunner{}
	r.cfg.Runner = f
	return &xEnv{rig: r, fake: f}
}

// crossDevice makes every rename fail with EXDEV, so each move takes the
// library's copy, prove and unlink route.
func (e *xEnv) crossDevice() {
	e.cfg.Mover = exdevMover()
}

// exdevMover is a Mover whose every rename fails with EXDEV.
func exdevMover() library.Mover {
	return library.Mover{Rename: func(oldpath, newpath string) error {
		return &os.LinkError{Op: "rename", Old: oldpath, New: newpath, Err: syscall.EXDEV}
	}}
}

// write stores ops as the manifest and returns its path.
func (e *xEnv) write(ops ...Op) string {
	e.t.Helper()
	m, findings, path := e.manifestOps(ops...)
	if len(findings) > 0 || len(m.Ops) != len(ops) {
		e.t.Fatalf("test manifest has findings: %+v", findings)
	}
	return path
}

// do loads the manifest at path and execs it.
func (e *xEnv) do(ctx context.Context, path string, opts ExecOptions) (ExecReport, error) {
	e.t.Helper()
	m, schema, err := Load(path)
	if err != nil {
		e.t.Fatal(err)
	}
	return e.cfg.Exec(ctx, m, schema, opts)
}

// goRun writes ops and runs exec --go on them.
func (e *xEnv) goRun(ops ...Op) (ExecReport, error) {
	e.t.Helper()
	return e.do(context.Background(), e.write(ops...), ExecOptions{Go: true})
}

// wantCode fails unless err carries the exit code.
func wantCode(t *testing.T, err error, code int) {
	t.Helper()
	if cli.Code(err) != code {
		t.Fatalf("exit code = %d (%v), want %d", cli.Code(err), err, code)
	}
}

func statuses(rep ExecReport) []string {
	out := make([]string, len(rep.Results))
	for i, r := range rep.Results {
		out[i] = r.Status
	}
	return out
}

func eqStrings(a, b []string) bool { return strings.Join(a, ",") == strings.Join(b, ",") }

// journalEvents is the journal file's records in order, as event names with
// their op numbers: "start", "begin 2", "done 2", "finish".
func journalEvents(t *testing.T, manifestPath string) []string {
	t.Helper()
	data, err := os.ReadFile(JournalPath(manifestPath))
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, line := range bytes.Split(bytes.TrimSpace(data), []byte("\n")) {
		var rec struct {
			Event string `json:"event"`
			Op    int    `json:"op"`
		}
		if err := json.Unmarshal(line, &rec); err != nil {
			t.Fatalf("journal line %q: %v", line, err)
		}
		if rec.Op > 0 {
			out = append(out, rec.Event+" "+itoa(rec.Op))
		} else {
			out = append(out, rec.Event)
		}
	}
	return out
}

func itoa(n int) string {
	b, _ := json.Marshal(n)
	return string(b)
}

// lastRecord decodes the journal's last line.
func lastRecord(t *testing.T, manifestPath string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(JournalPath(manifestPath))
	if err != nil {
		t.Fatal(err)
	}
	lines := bytes.Split(bytes.TrimSpace(data), []byte("\n"))
	var rec map[string]any
	if err := json.Unmarshal(lines[len(lines)-1], &rec); err != nil {
		t.Fatal(err)
	}
	return rec
}

// beginOps writes a journal in which ops are begun and never finished, the
// state a killed run leaves, optionally with earlier ops done.
func (e *xEnv) beginOps(path string, done []int, begun map[int]BeginState) {
	e.t.Helper()
	m, _, err := Load(path)
	if err != nil {
		e.t.Fatal(err)
	}
	w, err := OpenJournal(m, e.cfg)
	if err != nil {
		e.t.Fatal(err)
	}
	defer w.Close()
	if err := w.Start(); err != nil {
		e.t.Fatal(err)
	}
	for _, n := range done {
		if err := w.Begin(m.Ops[n-1], BeginState{}); err != nil {
			e.t.Fatal(err)
		}
		if err := w.Done(n, 0, ""); err != nil {
			e.t.Fatal(err)
		}
	}
	for n := 1; n <= len(m.Ops); n++ {
		if st, ok := begun[n]; ok {
			if err := w.Begin(m.Ops[n-1], st); err != nil {
				e.t.Fatal(err)
			}
		}
	}
}

func sizeOf(t *testing.T, path string) *int64 {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return i64(info.Size())
}

func present(path string) bool {
	_, err := os.Lstat(path)
	return !errors.Is(err, os.ErrNotExist)
}

// treeFiles maps every regular file under root to its bytes.
func treeFiles(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := walkAll(root, func(p string, isDir bool) error {
		if isDir {
			return nil
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		out[p] = string(data)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func walkAll(root string, fn func(path string, isDir bool) error) error {
	return filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		return fn(p, d.IsDir())
	})
}

// cancelOn returns a context that is cancelled when the fake starts program on
// file, so the run stops between ops once that op finishes.
func cancelOn(t *testing.T, f *fakeRunner, program, file string) (context.Context, context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	f.hook = func(argv []string) {
		if argv[0] == program && len(argv) > 1 && argv[1] == file {
			cancel()
		}
	}
	return ctx, cancel
}
