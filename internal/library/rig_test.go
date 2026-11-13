package library

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"

	"github.com/reclyptor/mediaops/kit/paths"
	"github.com/reclyptor/mediaops/kit/proc"
)

// rig is a media tree and an archive tree in a temp directory, with a device
// number per share so a move across them takes the cross-device path: a rename
// between devices fails with EXDEV, as it does between two NAS exports.
type rig struct {
	t       *testing.T
	media   string
	archive string
	tools   *fakeTools
	d       Deps

	// free is what every filesystem reports as available.
	free uint64

	mu   sync.Mutex
	devs map[string]uint64
}

func newRig(t *testing.T) *rig {
	t.Helper()
	root := t.TempDir()
	r := &rig{
		t:       t,
		media:   filepath.Join(root, "media"),
		archive: filepath.Join(root, "archive"),
		tools:   newFakeTools(),
		free:    1 << 60,
		devs:    map[string]uint64{},
	}
	for _, dir := range []string{r.media, r.archive} {
		if err := os.MkdirAll(dir, 0o770); err != nil {
			t.Fatal(err)
		}
	}
	r.d = Deps{
		Paths:     paths.Config{MediaRoot: r.media, Archive: r.archive, StatDev: r.dev},
		Runner:    r.tools,
		Mover:     Mover{Rename: r.rename},
		FreeBytes: func(string) (uint64, error) { return r.free, nil },
		Workers:   4,
	}
	return r
}

// dev gives "/" and the media root device 1, the archive device 30, and each
// share under the media root its own, like the mounts in the pod.
func (r *rig) dev(path string) (uint64, error) {
	path = filepath.Clean(path)
	switch {
	case within(r.archive, path):
		return 30, nil
	case strictlyWithin(r.media, path):
		rel, _ := filepath.Rel(r.media, path)
		share, _, _ := strings.Cut(rel, string(filepath.Separator))
		r.mu.Lock()
		defer r.mu.Unlock()
		if r.devs[share] == 0 {
			r.devs[share] = uint64(10 + len(r.devs))
		}
		return r.devs[share], nil
	}
	return 1, nil
}

// rename is os.Rename between two places on one device and EXDEV between two.
func (r *rig) rename(oldpath, newpath string) error {
	a, _ := r.dev(oldpath)
	b, _ := r.dev(filepath.Dir(newpath))
	if a != b {
		return &os.LinkError{Op: "rename", Old: oldpath, New: newpath, Err: syscall.EXDEV}
	}
	return os.Rename(oldpath, newpath)
}

// put writes a file under the media root and returns its path.
func (r *rig) put(rel, content string) string {
	r.t.Helper()
	return putFile(r.t, filepath.Join(r.media, rel), content)
}

func putFile(t *testing.T, path, content string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o770); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o660); err != nil {
		t.Fatal(err)
	}
	return path
}

func (r *rig) path(rel string) string { return filepath.Join(r.media, rel) }

func (r *rig) mkdir(rel string) string {
	r.t.Helper()
	p := r.path(rel)
	if err := os.MkdirAll(p, 0o770); err != nil {
		r.t.Fatal(err)
	}
	return p
}

func (r *rig) read(path string) string {
	r.t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		r.t.Fatal(err)
	}
	return string(data)
}

// snapshot is every file and directory under the media and archive roots, with
// each file's content, so a test can prove a command touched nothing or lost
// nothing.
func (r *rig) snapshot() map[string]string {
	r.t.Helper()
	out := map[string]string{}
	for _, root := range []string{r.media, r.archive} {
		err := filepath.WalkDir(root, func(path string, e fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if e.IsDir() {
				out[path+"/"] = ""
				return nil
			}
			data, err := os.ReadFile(path)
			out[path] = string(data)
			return err
		})
		if err != nil {
			r.t.Fatal(err)
		}
	}
	return out
}

// contents is every file's content, wherever it is, sorted: what must be the
// same before and after any move.
func (r *rig) contents() []string {
	var out []string
	for path, content := range r.snapshot() {
		if !strings.HasSuffix(path, "/") {
			out = append(out, content)
		}
	}
	slices.Sort(out)
	return out
}

func (r *rig) exists(path string) bool { return exists(path) }

// fakeTools stands in for ffprobe, mkvpropedit and mkvmerge. Every argv goes
// through proc.Check first, so the arguments the library builds are held to
// the same allowlist as the real runner's.
//
// A file's content decides what ffprobe says of it: its first word is the
// duration, except BROKEN (a failed probe), ZERO, NAN and NODUR.
type fakeTools struct {
	mu     sync.Mutex
	argv   [][]string
	titles map[string]string

	// propeditExit is mkvpropedit's exit status. readBack, when set, replaces
	// the title mkvmerge -J reports.
	propeditExit int
	readBack     func(file string) string
	probeErr     error
	onProbe      func()
}

func newFakeTools() *fakeTools { return &fakeTools{titles: map[string]string{}} }

var _ proc.Runner = (*fakeTools)(nil)

func (f *fakeTools) Stream(context.Context, []string, func(string)) (int, error) {
	return -1, errors.New("fakeTools has no Stream")
}

func (f *fakeTools) Run(_ context.Context, argv ...string) (proc.Result, error) {
	if err := proc.Check(argv); err != nil {
		return proc.Result{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.argv = append(f.argv, slices.Clone(argv))

	file := argv[len(argv)-1]
	switch argv[0] {
	case "ffprobe":
		return f.probe(argv, file)
	case "mkvpropedit":
		title := ""
		for i, a := range argv {
			if a == "--set" && i+1 < len(argv) {
				title = strings.TrimPrefix(argv[i+1], "title=")
			}
		}
		if f.propeditExit < 2 {
			f.titles[argv[1]] = title
		}
		return proc.Result{ExitCode: f.propeditExit, Stdout: []byte("Done.")}, nil
	case "mkvmerge":
		title := f.titles[file]
		if f.readBack != nil {
			title = f.readBack(file)
		}
		out, _ := json.Marshal(map[string]any{"container": map[string]any{"properties": map[string]any{"title": title}}})
		return proc.Result{Stdout: out}, nil
	}
	return proc.Result{}, fmt.Errorf("fakeTools has no %s", argv[0])
}

func (f *fakeTools) probe(_ []string, file string) (proc.Result, error) {
	if f.onProbe != nil {
		f.onProbe()
	}
	if f.probeErr != nil {
		return proc.Result{}, f.probeErr
	}
	data, err := os.ReadFile(file)
	if err != nil {
		return proc.Result{ExitCode: 1, Stderr: []byte(err.Error())}, nil
	}
	word, _, _ := strings.Cut(strings.TrimSpace(string(data)), " ")
	switch word {
	case "BROKEN":
		return proc.Result{ExitCode: 1, Stderr: []byte("Invalid data found when processing input")}, nil
	case "ZERO":
		word = "0.000000"
	case "NAN":
		word = "N/A"
	case "NODUR":
		return proc.Result{Stdout: []byte(`{"format":{}}`)}, nil
	}
	if _, err := fmt.Sscanf(word, "%f", new(float64)); err != nil && word != "N/A" {
		word = fmt.Sprint(math.NaN())
	}
	return proc.Result{Stdout: fmt.Appendf(nil, `{"format":{"duration":%q}}`, word)}, nil
}

// probed is the files ffprobe was run on.
func (f *fakeTools) probed() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, a := range f.argv {
		if a[0] == "ffprobe" {
			out = append(out, a[len(a)-1])
		}
	}
	slices.Sort(out)
	return out
}

// edited is the files mkvpropedit was run on.
func (f *fakeTools) edited() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, a := range f.argv {
		if a[0] == "mkvpropedit" {
			out = append(out, a[1])
		}
	}
	return out
}

func (f *fakeTools) calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.argv)
}

// episode is the content of a good media file: a duration the fake ffprobe
// reads, and a tag that makes the file unique so a lost or swapped file shows.
func episode(tag string) string { return "1421.5 " + tag }

func skipIfRoot(t *testing.T) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("root bypasses permission bits")
	}
}
