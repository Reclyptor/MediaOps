package inventory

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/reclyptor/mediaops/internal/cli"
	"github.com/reclyptor/mediaops/kit/paths"
	"github.com/reclyptor/mediaops/kit/proc"
)

func write(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// tree builds media/anime/Show/{b.mkv,a.mkv,notes.txt,Disc 01/c.mp4}, a FIFO and
// symlinks, and returns the root.
func tree(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	show := filepath.Join(root, "anime", "Show")
	write(t, filepath.Join(show, "b.mkv"))
	write(t, filepath.Join(show, "a.mkv"))
	write(t, filepath.Join(show, "notes.txt"))
	write(t, filepath.Join(show, "Disc 01", "c.mp4"))
	if err := syscall.Mkfifo(filepath.Join(show, "fifo.mkv"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(show, "a.mkv"), filepath.Join(show, "link.mkv")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(show, "Disc 01"), filepath.Join(show, "linkdir")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(show, "a.mkv"), filepath.Join(show, "cover.jpg")); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(filepath.Join(show, "pipe"), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

func deps(root string, f *fakeProbe) Deps {
	return Deps{Paths: paths.Config{MediaRoot: root}, Runner: f}
}

func TestWalkSkipsWhatIsNotARegularFile(t *testing.T) {
	root := tree(t)
	show := filepath.Join(root, "anime", "Show")

	// a hung read on the FIFO would trip this timeout
	done := make(chan struct{})
	var files, skipped []string
	var err error
	go func() {
		defer close(done)
		files, skipped, err = Walk([]string{root})
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Walk blocked, probably opening the FIFO")
	}
	if err != nil {
		t.Fatal(err)
	}

	wantFiles := []string{filepath.Join(show, "Disc 01", "c.mp4"), filepath.Join(show, "a.mkv"), filepath.Join(show, "b.mkv")}
	if !reflect.DeepEqual(files, wantFiles) {
		t.Errorf("files = %v, want %v", files, wantFiles)
	}
	wantSkipped := []string{filepath.Join(show, "fifo.mkv"), filepath.Join(show, "link.mkv")}
	if !reflect.DeepEqual(skipped, wantSkipped) {
		t.Errorf("skipped = %v, want %v", skipped, wantSkipped)
	}
}

func TestWalkDeduplicatesOverlappingRoots(t *testing.T) {
	root := tree(t)
	show := filepath.Join(root, "anime", "Show")
	files, _, err := Walk([]string{show, filepath.Join(show, "a.mkv"), root})
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 3 {
		t.Fatalf("files = %v, want each file once", files)
	}
}

func TestWalkNamesAFIFOGivenAsAnArgument(t *testing.T) {
	root := tree(t)
	fifo := filepath.Join(root, "anime", "Show", "fifo.mkv")
	files, skipped, err := Walk([]string{fifo})
	if err != nil || len(files) != 0 || !reflect.DeepEqual(skipped, []string{fifo}) {
		t.Fatalf("files %v skipped %v err %v", files, skipped, err)
	}
}

func TestProbeBuildsTheInventory(t *testing.T) {
	root := tree(t)
	show := filepath.Join(root, "anime", "Show")
	f := &fakeProbe{answer: map[string]proc.Result{
		filepath.Join(show, "a.mkv"):            ffprobeJSON(1425.5, "Fate/Apocrypha", 0, 335.669),
		filepath.Join(show, "b.mkv"):            ffprobeJSON(60, ""),
		filepath.Join(show, "Disc 01", "c.mp4"): ffprobeJSON(10, "", 0),
	}}

	inv, err := Probe(context.Background(), f, paths.Config{MediaRoot: root}, []string{root}, 3)
	if err != nil {
		t.Fatal(err)
	}
	if inv.Probed != 3 || inv.Errors != 0 || len(inv.Files) != 3 || len(inv.Skipped) != 2 {
		t.Fatalf("inventory = %+v", inv)
	}
	for _, p := range f.asked {
		if strings.Contains(p, "fifo") || strings.HasSuffix(p, "link.mkv") || strings.HasSuffix(p, "linkdir") {
			t.Errorf("probed a path that is not a regular file: %s", p)
		}
	}
	if got := []string{inv.Files[0].Path, inv.Files[1].Path, inv.Files[2].Path}; !sort3(got) {
		t.Errorf("records are not sorted by path: %v", got)
	}

	a := inv.Files[1]
	if a.Path != filepath.Join(show, "a.mkv") {
		t.Fatalf("record 1 = %s", a.Path)
	}
	if a.Rel == nil || *a.Rel != "anime/Show/a.mkv" || a.Size != 1000 || a.Duration != 1425.5 {
		t.Errorf("record = %+v", a)
	}
	if a.Title == nil || *a.Title != "Fate/Apocrypha" || !reflect.DeepEqual(a.Chapters, []float64{0, 335.669}) {
		t.Errorf("title/chapters = %v %v", a.Title, a.Chapters)
	}
	v, au := a.Streams[0], a.Streams[1]
	if v.Type != "video" || v.Codec != "h264" || v.Width != 1920 || !v.Default || v.Lang != nil || v.Title != nil {
		t.Errorf("video = %+v", v)
	}
	if au.Lang == nil || *au.Lang != "jpn" || au.Title == nil || *au.Title != "Japanese" || au.Channels != 2 {
		t.Errorf("audio = %+v", au)
	}
}

func sort3(s []string) bool { return s[0] < s[1] && s[1] < s[2] }

func TestProbeTurnsAFailureIntoAnErrorRecord(t *testing.T) {
	root := tree(t)
	show := filepath.Join(root, "anime", "Show")
	f := &fakeProbe{answer: map[string]proc.Result{
		filepath.Join(show, "a.mkv"): ffprobeJSON(60, ""),
		// b.mkv and c.mp4 are unknown to the fake, so ffprobe exits 1
	}}
	inv, err := Probe(context.Background(), f, paths.Config{MediaRoot: root}, []string{show}, 2)
	if err != nil {
		t.Fatal(err)
	}
	if inv.Probed != 3 || inv.Errors != 2 {
		t.Fatalf("probed %d errors %d", inv.Probed, inv.Errors)
	}
	bad := inv.Files[2]
	if bad.Error == "" || !strings.Contains(bad.Error, "ffprobe exited 1") {
		t.Errorf("error record = %+v", bad)
	}
}

func TestProbeRejectsAZeroDuration(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "z.mkv"))
	f := &fakeProbe{answer: map[string]proc.Result{filepath.Join(root, "z.mkv"): ffprobeJSON(0, "")}}
	inv, err := Probe(context.Background(), f, paths.Config{MediaRoot: root}, []string{root}, 1)
	if err != nil || inv.Errors != 1 || !strings.Contains(inv.Files[0].Error, "duration") {
		t.Fatalf("inv %+v err %v", inv, err)
	}
}

func TestRelIsNullOutsideTheMediaRoot(t *testing.T) {
	media, other := t.TempDir(), t.TempDir()
	write(t, filepath.Join(other, "x.mkv"))
	f := &fakeProbe{answer: map[string]proc.Result{filepath.Join(other, "x.mkv"): ffprobeJSON(60, "")}}
	inv, err := Probe(context.Background(), f, paths.Config{MediaRoot: media}, []string{other}, 1)
	if err != nil || inv.Files[0].Rel != nil {
		t.Fatalf("rel = %v err %v", inv.Files[0].Rel, err)
	}
}

func TestProbeRejectsFewerThanOneWorker(t *testing.T) {
	if _, err := Probe(context.Background(), &fakeProbe{}, paths.Config{}, nil, 0); err == nil {
		t.Fatal("zero workers accepted")
	}
}

func TestRecordJSONShapes(t *testing.T) {
	ok, err := json.Marshal(Record{Path: "/a.mkv", Size: 5, Duration: 2})
	if err != nil {
		t.Fatal(err)
	}
	const wantOK = `{"path":"/a.mkv","rel":null,"size":5,"duration":2,"title":null,"chapters":[],"streams":[]}`
	if string(ok) != wantOK {
		t.Errorf("success record = %s", ok)
	}
	bad, err := json.Marshal(Record{Path: "/b.mkv", Error: "boom", Size: 9})
	if err != nil {
		t.Fatal(err)
	}
	if string(bad) != `{"path":"/b.mkv","error":"boom"}` {
		t.Errorf("error record = %s", bad)
	}
}

func TestProbeCommand(t *testing.T) {
	root := tree(t)
	show := filepath.Join(root, "anime", "Show")
	answers := map[string]proc.Result{
		filepath.Join(show, "a.mkv"):            ffprobeJSON(60, "", 0, 30),
		filepath.Join(show, "b.mkv"):            ffprobeJSON(61, ""),
		filepath.Join(show, "Disc 01", "c.mp4"): ffprobeJSON(62, ""),
	}
	run := func(args ...string) (int, string, string) {
		var out, errOut bytes.Buffer
		f := &fakeProbe{answer: answers}
		code := cli.Run(context.Background(), "mkv-probe", args, cli.IO{Stdout: &out, Stderr: &errOut},
			func(ctx context.Context, args []string, s cli.IO) error {
				return ProbeCommand(ctx, deps(root, f), args, s)
			})
		return code, out.String(), errOut.String()
	}

	t.Run("flags among the paths", func(t *testing.T) {
		code, out, errOut := run(show, "--workers", "2")
		if code != 0 || errOut != "" {
			t.Fatalf("code %d stderr %q", code, errOut)
		}
		var inv Inventory
		if err := json.Unmarshal([]byte(out), &inv); err != nil {
			t.Fatal(err)
		}
		if inv.Probed != 3 || inv.Errors != 0 || !reflect.DeepEqual(inv.Roots, []string{show}) || len(inv.Skipped) != 2 {
			t.Fatalf("inventory = %+v", inv)
		}
		if !strings.Contains(out, `"skipped": [`) {
			t.Errorf("output = %s", out)
		}
	})

	t.Run("an empty tree is an empty list, not null", func(t *testing.T) {
		code, out, _ := run(t.TempDir())
		if code != 0 || !strings.Contains(out, `"files": []`) || !strings.Contains(out, `"skipped": []`) {
			t.Fatalf("code %d out %s", code, out)
		}
	})

	t.Run("a failed probe exits 1 with the record in the output", func(t *testing.T) {
		delete(answers, filepath.Join(show, "b.mkv"))
		defer func() { answers[filepath.Join(show, "b.mkv")] = ffprobeJSON(61, "") }()
		code, out, errOut := run(show)
		if code != 1 || errOut != "" || !strings.Contains(out, `"error": "ffprobe exited 1`) {
			t.Fatalf("code %d stderr %q out %s", code, errOut, out)
		}
	})

	t.Run("usage", func(t *testing.T) {
		for name, args := range map[string][]string{
			"no path":         {},
			"workers zero":    {show, "--workers", "0"},
			"workers garbage": {show, "--workers", "many"},
			"unknown flag":    {show, "--nope"},
			"missing path":    {filepath.Join(root, "absent")},
		} {
			code, out, errOut := run(args...)
			if code != 2 || out != "" || !strings.HasPrefix(errOut, "mkv-probe: FATAL: ") {
				t.Errorf("%s: code %d stdout %q stderr %q", name, code, out, errOut)
			}
		}
	})
}
