package manifest

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/reclyptor/mediaops/internal/cli"
)

const jrSHA = "aaaa"

func jrManifest(t *testing.T) *Manifest {
	t.Helper()
	ops := make([]Op, 5)
	for i := range ops {
		ops[i] = Op{N: i + 1, Kind: KindRename}
	}
	return &Manifest{Path: filepath.Join(t.TempDir(), "m.json"), SHA256: jrSHA, Ops: ops}
}

func jrConfig() Config {
	return Config{
		Now:     func() time.Time { return time.Date(2026, 11, 13, 10, 0, 0, 0, time.UTC) },
		Version: "test",
	}
}

func jrWrite(t *testing.T, m *Manifest, body string) {
	t.Helper()
	if err := os.WriteFile(JournalPath(m.Path), []byte(body), 0o660); err != nil {
		t.Fatal(err)
	}
}

func jrRefused(t *testing.T, err error, want ...string) {
	t.Helper()
	if err == nil {
		t.Fatal("want a refusal, got nil")
	}
	if cli.Code(err) != cli.ExitRefused {
		t.Fatalf("exit code = %d, want %d (%v)", cli.Code(err), cli.ExitRefused, err)
	}
	for _, w := range want {
		if !strings.Contains(err.Error(), w) {
			t.Errorf("error %q lacks %q", err, w)
		}
	}
}

func jrReadOK(t *testing.T, m *Manifest) *Journal {
	t.Helper()
	j, err := ReadJournal(m)
	if err != nil {
		t.Fatal(err)
	}
	return j
}

func jrStart(t *testing.T, m *Manifest) *JournalWriter {
	t.Helper()
	w, err := OpenJournal(m, jrConfig())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { w.Close() })
	if err := w.Start(); err != nil {
		t.Fatal(err)
	}
	return w
}

func jrCheck(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func TestJournalRoundTrip(t *testing.T) {
	m := jrManifest(t)
	w := jrStart(t, m)
	size := int64(1234)
	tree := map[string]int64{"a.mkv": 10, "d/b.mkv": 20}
	jrCheck(t, w.Begin(m.Ops[0], BeginState{Size: &size}))
	jrCheck(t, w.Done(1, 1234, "renamed"))
	jrCheck(t, w.Begin(m.Ops[1], BeginState{Tree: tree}))
	jrCheck(t, w.Failed(2, errors.New("boom")))
	jrCheck(t, w.Begin(m.Ops[2], BeginState{Size: &size}))
	jrCheck(t, w.Finish(1))
	jrCheck(t, w.Close())

	j := jrReadOK(t, m)
	if !j.Done(1) || j.Done(2) || j.Done(3) || j.Done(99) {
		t.Errorf("Done wrong: 1=%v 2=%v 3=%v", j.Done(1), j.Done(2), j.Done(3))
	}
	if j.DoneCount() != 1 {
		t.Errorf("DoneCount = %d, want 1", j.DoneCount())
	}
	if _, ok := j.InDoubt(1); ok {
		t.Error("op 1 is done, not in doubt")
	}
	st, ok := j.InDoubt(2)
	if !ok || !reflect.DeepEqual(st, BeginState{Tree: tree}) {
		t.Errorf("InDoubt(2) = %+v, %v", st, ok)
	}
	st, ok = j.InDoubt(3)
	if !ok || st.Size == nil || *st.Size != size {
		t.Errorf("InDoubt(3) = %+v, %v", st, ok)
	}
	if _, ok := j.InDoubt(4); ok {
		t.Error("op 4 never began")
	}
}

func TestJournalRecordShape(t *testing.T) {
	m := jrManifest(t)
	w := jrStart(t, m)
	jrCheck(t, w.Done(1, 0, ""))
	jrCheck(t, w.Finish(0))
	jrCheck(t, w.Close())
	data, err := os.ReadFile(JournalPath(m.Path))
	if err != nil {
		t.Fatal(err)
	}
	s := string(data)
	for _, want := range []string{`"event":"start"`, `"manifest_sha256":"aaaa"`, `"ops":5`, `"tool_version":"test"`, `"at":"2026-11-13T10:00:00Z"`, `"bytes":0`, `"applied":0`} {
		if !strings.Contains(s, want) {
			t.Errorf("journal lacks %s:\n%s", want, s)
		}
	}
	if strings.Count(s, "\n") != 3 || !strings.HasSuffix(s, "\n") {
		t.Errorf("want 3 newline-terminated records:\n%s", s)
	}
}

func TestJournalNeverRun(t *testing.T) {
	m := jrManifest(t)
	j := jrReadOK(t, m)
	if j == nil || j.DoneCount() != 0 || j.Done(1) {
		t.Errorf("never-run journal = %+v", j)
	}
}

func TestJournalNilSafe(t *testing.T) {
	var j *Journal
	if j.Done(1) || j.DoneCount() != 0 {
		t.Error("nil journal is not empty")
	}
	if _, ok := j.InDoubt(1); ok {
		t.Error("nil journal has an in-doubt op")
	}
}

func TestJournalReadCreatesNothing(t *testing.T) {
	m := jrManifest(t)
	dir := filepath.Dir(m.Path)
	before, _ := os.ReadDir(dir)
	jrReadOK(t, m)
	after, _ := os.ReadDir(dir)
	if len(before) != 0 || len(after) != 0 {
		t.Errorf("reading created files: before %d, after %d", len(before), len(after))
	}
}

func TestJournalFileMode(t *testing.T) {
	for _, umask := range []int{0, 0o077} {
		old := syscall.Umask(umask)
		m := jrManifest(t)
		w, err := OpenJournal(m, jrConfig())
		syscall.Umask(old)
		if err != nil {
			t.Fatal(err)
		}
		w.Close()
		info, err := os.Stat(JournalPath(m.Path))
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o660 {
			t.Errorf("umask %o: mode = %o, want 660", umask, info.Mode().Perm())
		}
	}
}

func TestJournalTornLastLineIgnored(t *testing.T) {
	m := jrManifest(t)
	jrWrite(t, m, `{"event":"start","manifest_sha256":"aaaa"}`+"\n"+
		`{"event":"done","op":1}`+"\n"+`{"event":"don`)
	j := jrReadOK(t, m)
	if !j.Done(1) || j.DoneCount() != 1 {
		t.Errorf("journal = %+v", j)
	}
}

func TestJournalTornTailRepairCycle(t *testing.T) {
	m := jrManifest(t)
	jrWrite(t, m, `{"event":"start","manifest_sha256":"aaaa"}`+"\n"+
		`{"event":"done","op":1}`+"\n"+`{"event":"begin","op":2,"sta`)
	if j := jrReadOK(t, m); !j.Done(1) {
		t.Fatal("torn tail lost op 1")
	}

	w, err := OpenJournal(m, jrConfig())
	jrCheck(t, err)
	jrCheck(t, w.Start())
	jrCheck(t, w.Begin(m.Ops[2], BeginState{}))
	jrCheck(t, w.Done(3, 5, ""))
	jrCheck(t, w.Close())

	data, _ := os.ReadFile(JournalPath(m.Path))
	if !strings.Contains(string(data), `{"event":"repair","at":"2026-11-13T10:00:00Z","torn_bytes":28}`) {
		t.Errorf("no repair record:\n%s", data)
	}
	j := jrReadOK(t, m)
	if !j.Done(1) || !j.Done(3) || j.Done(2) || j.DoneCount() != 2 {
		t.Errorf("after repair: done 1=%v 2=%v 3=%v count=%d", j.Done(1), j.Done(2), j.Done(3), j.DoneCount())
	}
	if _, ok := j.InDoubt(2); ok {
		t.Error("the torn begin must not count")
	}

	w, err = OpenJournal(m, jrConfig())
	jrCheck(t, err)
	jrCheck(t, w.Close())
	after, _ := os.ReadFile(JournalPath(m.Path))
	if string(after) != string(data) {
		t.Error("a clean journal must not be repaired again")
	}
}

func TestJournalTornTailLongerThanChunk(t *testing.T) {
	m := jrManifest(t)
	jrWrite(t, m, `{"event":"start","manifest_sha256":"aaaa"}`+"\n"+`{"event":"done","op":1,"detail":"`+strings.Repeat("x", 10000))
	w, err := OpenJournal(m, jrConfig())
	jrCheck(t, err)
	jrCheck(t, w.Done(2, 0, ""))
	jrCheck(t, w.Close())
	j := jrReadOK(t, m)
	if j.Done(1) || !j.Done(2) {
		t.Errorf("done 1=%v 2=%v", j.Done(1), j.Done(2))
	}
}

func TestJournalDamagedMiddleRefused(t *testing.T) {
	m := jrManifest(t)
	jrWrite(t, m, `{"event":"start","manifest_sha256":"aaaa"}`+"\n"+
		"garbage\n"+`{"event":"done","op":1}`+"\n")
	_, err := ReadJournal(m)
	jrRefused(t, err, "damaged at line 2", filepath.Base(JournalPath(m.Path)))
}

func TestJournalSHAMismatchRefused(t *testing.T) {
	m := jrManifest(t)
	jrWrite(t, m, `{"event":"start","manifest_sha256":"aaaa"}`+"\n"+
		`{"event":"done","op":1}`+"\n"+`{"event":"done","op":2}`+"\n"+
		`{"event":"start","manifest_sha256":"bbbb"}`+"\n")
	_, err := ReadJournal(m)
	jrRefused(t, err, "the manifest changed after 2 ops were applied")
}

func TestJournalSHAMismatchCountsAllDoneOps(t *testing.T) {
	m := jrManifest(t)
	m.SHA256 = "bbbb"
	jrWrite(t, m, `{"event":"start","manifest_sha256":"aaaa"}`+"\n"+
		`{"event":"begin","op":1}`+"\n"+`{"event":"done","op":1}`+"\n"+
		`{"event":"begin","op":2}`+"\n"+`{"event":"done","op":2}`+"\n"+
		`{"event":"begin","op":9}`+"\n"+`{"event":"done","op":9}`+"\n")
	_, err := ReadJournal(m)
	jrRefused(t, err, "the manifest changed after 3 ops were applied")
}

func TestJournalPythonEraRefused(t *testing.T) {
	m := jrManifest(t)
	if err := os.WriteFile(stem(m.Path)+".journal", []byte("3\n"), 0o660); err != nil {
		t.Fatal(err)
	}
	_, err := ReadJournal(m)
	jrRefused(t, err, "Python-era", "redo ops")
}

func TestJournalMalformedRefused(t *testing.T) {
	start := `{"event":"start","manifest_sha256":"aaaa"}` + "\n"
	tests := map[string]string{
		"unknown event":       start + `{"event":"explode","op":1}` + "\n" + `{"event":"done","op":2}` + "\n",
		"missing event":       start + `{"op":1}` + "\n" + `{"event":"done","op":2}` + "\n",
		"op too high":         start + `{"event":"done","op":6}` + "\n",
		"op zero":             start + `{"event":"begin","op":0}` + "\n",
		"failed op range":     start + `{"event":"failed","op":-1}` + "\n",
		"event before start":  `{"event":"done","op":1}` + "\n" + start,
		"finish before start": `{"event":"finish","applied":0}` + "\n" + start,
		"not an object":       start + "[1]\n" + `{"event":"done","op":1}` + "\n",
	}
	for name, body := range tests {
		t.Run(name, func(t *testing.T) {
			m := jrManifest(t)
			jrWrite(t, m, body)
			_, err := ReadJournal(m)
			jrRefused(t, err, "damaged")
		})
	}
}

func TestJournalBeginFailedBeginDone(t *testing.T) {
	m := jrManifest(t)
	w := jrStart(t, m)
	jrCheck(t, w.Begin(m.Ops[0], BeginState{}))
	jrCheck(t, w.Failed(1, errors.New("x")))
	jrCheck(t, w.Begin(m.Ops[0], BeginState{}))
	jrCheck(t, w.Done(1, 1, ""))
	jrCheck(t, w.Begin(m.Ops[0], BeginState{}))
	jrCheck(t, w.Close())
	j := jrReadOK(t, m)
	if !j.Done(1) {
		t.Error("op 1 should be done")
	}
	if _, ok := j.InDoubt(1); ok {
		t.Error("a done op is never in doubt")
	}
}

func TestJournalLaterBeginReplacesState(t *testing.T) {
	m := jrManifest(t)
	w := jrStart(t, m)
	a, b := int64(1), int64(2)
	jrCheck(t, w.Begin(m.Ops[0], BeginState{Size: &a}))
	jrCheck(t, w.Failed(1, nil))
	jrCheck(t, w.Begin(m.Ops[0], BeginState{Size: &b}))
	jrCheck(t, w.Close())
	st, ok := jrReadOK(t, m).InDoubt(1)
	if !ok || *st.Size != b {
		t.Errorf("state = %+v, %v", st, ok)
	}
}

func TestJournalConcurrentWritesDoNotInterleave(t *testing.T) {
	m := jrManifest(t)
	w := jrStart(t, m)
	done := make(chan error, 5)
	for n := 1; n <= 5; n++ {
		go func() { done <- w.Done(n, 1, strings.Repeat("d", 2000)) }()
	}
	for range 5 {
		jrCheck(t, <-done)
	}
	jrCheck(t, w.Close())
	if got := jrReadOK(t, m).DoneCount(); got != 5 {
		t.Errorf("DoneCount = %d, want 5", got)
	}
}

func TestOpenJournalUncreatableRefused(t *testing.T) {
	m := &Manifest{Path: filepath.Join(t.TempDir(), "missing", "m.json"), SHA256: jrSHA}
	_, err := OpenJournal(m, jrConfig())
	jrRefused(t, err, "cannot be created")
}

func TestJournalSymlinkRefused(t *testing.T) {
	m := jrManifest(t)
	if err := os.Symlink("/dev/zero", JournalPath(m.Path)); err != nil {
		t.Fatal(err)
	}
	_, err := ReadJournal(m)
	jrRefused(t, err, "not a regular file (symlink)")
}

func TestOpenJournalSymlinkRefused(t *testing.T) {
	m := jrManifest(t)
	target := filepath.Join(t.TempDir(), "target")
	if err := os.WriteFile(target, []byte("keep\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, JournalPath(m.Path)); err != nil {
		t.Fatal(err)
	}
	_, err := OpenJournal(m, jrConfig())
	jrRefused(t, err, "not a regular file (symlink)")
	if got, _ := os.ReadFile(target); string(got) != "keep\n" {
		t.Errorf("symlink target was written: %q", got)
	}
}
