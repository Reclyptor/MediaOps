package runstore

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/reclyptor/mediaops/internal/health"
)

type fakeLive struct {
	alive bool
	err   error
	asked []string
}

func (f *fakeLive) Alive(runID string) (bool, error) {
	f.asked = append(f.asked, runID)
	return f.alive, f.err
}

var t0 = time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)

func newStore(t *testing.T, alive bool) (*Store, *fakeLive) {
	t.Helper()
	live := &fakeLive{alive: alive}
	return &Store{Root: t.TempDir(), Live: live, Now: func() time.Time { return t0.Add(time.Hour) }}, live
}

func str(s string) *string { return &s }
func num(n int) *int       { return &n }

func running(id string, startedAt time.Time) *State {
	return &State{
		RunID: id, Kind: KindRip, State: RunRunning, StartedAt: startedAt,
		Controller: Controller{PID: 412},
		Discs: []Disc{
			{Index: 0, Serial: "A", Label: "Show S01 D01", State: DiscDone},
			{Index: 1, Serial: "B", Label: "Show S01 D02", State: DiscRipping},
			{Index: 2, Serial: "C", Label: "Show S01 D03", State: DiscPending},
			{Index: 3, Serial: "D", Label: "Show S01 D04", State: DiscBlocked, Reason: str("The drive links at 12 Mbit/s.")},
		},
	}
}

const id1 = "20261113-120000-a41c"

func rawState(t *testing.T, s *Store, id string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(s.Dir(id), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestStateRoundTrip(t *testing.T) {
	s, _ := newStore(t, true)
	started := t0.Add(5 * time.Second)
	finished := t0.Add(time.Minute)
	paused := t0.Add(2 * time.Minute)
	st := &State{
		RunID: id1, Kind: KindIdentify, State: RunRunning, Reason: nil,
		StartedAt: t0, ToolVersion: "20261118021500-1a2b3c4",
		Controller: Controller{PID: 412, Alive: true},
		Health: Health{
			Dmesg:       health.Dmesg{Available: false, Detail: "dmesg: klogctl: Operation not permitted"},
			PausedUntil: &paused, PauseReason: str("5-1.2.1 fell from 480 to 12 Mbit/s"),
			Events: []health.Event{{Port: "5-1.2.1", Kind: health.KindLinkDropped, Detail: "x", Time: t0}},
		},
		Discs: []Disc{{
			Index: 0, Serial: "BP52D24A16083824", Label: "Parallel World Pharmacy S01 D02",
			Dest: "/stage/anime/PWP/Season 01/Disc 02", State: DiscSuspect, Reason: str("a sentence"),
			Drive:         &DriveRef{Dev: "/dev/sr0", SG: "/dev/sg26", USBPort: "5-1.2.1", LinkMbps: 480},
			Progress:      &Progress{Phase: "Saving to MKV file", Current: 31004, Total: 65536},
			Titles:        TitleCount{Done: 3, Total: 8},
			MakemkvExit:   num(0),
			Errors:        []string{"read error"},
			SuspectEvents: []health.Event{{Port: "5-1.2", Kind: health.KindDmesg, Detail: "reset", Time: t0}},
			Eject:         &Eject{OK: false, Detail: "tray still closed"},
			Identified: &Identified{Name: "PWP", VolumeLabel: "PWP_D2", Type: "Blu-ray disc", Titles: []IdentifiedTitle{{
				ID: 1, Duration: "0:24:30", DurationSeconds: num(1470), Chapters: num(7),
				SourcePlaylist: "00800.mpls", SizeBytes: new(int64(5000000000)),
			}}},
			StartedAt: &started, CopyStartedAt: &started, FinishedAt: &finished,
		}},
		Problems: []Problem{{Index: 0, Serial: "BP52D24A16083824", Label: "l", State: DiscSuspect, Reason: "a sentence"}},
	}
	want := *st
	want.Schema = Schema

	if err := s.Create(st); err != nil {
		t.Fatal(err)
	}
	got, err := s.Load(id1)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(*got, want) {
		gotJSON, _ := json.MarshalIndent(got, "", " ")
		wantJSON, _ := json.MarshalIndent(want, "", " ")
		t.Errorf("round trip changed the state\n got: %s\nwant: %s", gotJSON, wantJSON)
	}
	if got.Discs[0].MakemkvExit == nil || *got.Discs[0].MakemkvExit != 0 {
		t.Error("makemkv_exit 0 was lost: a zero exit must stay distinguishable from none")
	}
}

func TestSaveNormalisesNilSlices(t *testing.T) {
	s, _ := newStore(t, true)
	st := &State{RunID: id1, Kind: KindRip, State: RunRunning, StartedAt: t0, Discs: []Disc{
		{Index: 0, Serial: "A", State: DiscPending, Identified: &Identified{}},
	}}
	if err := s.Create(st); err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(rawState(t, s, id1), &doc); err != nil {
		t.Fatal(err)
	}
	isEmptyList := func(v any) bool { l, ok := v.([]any); return ok && len(l) == 0 }
	disc := doc["discs"].([]any)[0].(map[string]any)
	for name, v := range map[string]any{
		"problems":          doc["problems"],
		"health.events":     doc["health"].(map[string]any)["events"],
		"discs[0].errors":   disc["errors"],
		"discs[0].suspect":  disc["suspect_events"],
		"identified.titles": disc["identified"].(map[string]any)["titles"],
	} {
		if !isEmptyList(v) {
			t.Errorf("%s = %#v, want []", name, v)
		}
	}
	if doc["schema"] != float64(2) {
		t.Errorf("schema = %v", doc["schema"])
	}

	empty := &State{RunID: "20261113-120001-0000", Kind: KindRip, State: RunRunning, StartedAt: t0}
	if err := s.Create(empty); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(rawState(t, s, empty.RunID), []byte(`"discs": []`)) {
		t.Error("a run with no discs must still write discs as []")
	}
}

func TestSaveKeepsNewestEvents(t *testing.T) {
	s, _ := newStore(t, true)
	st := running(id1, t0)
	for i := range 25 {
		st.Health.Events = append(st.Health.Events, health.Event{Port: "5-1", Kind: health.KindDmesg, Detail: string(rune('a' + i))})
	}
	if err := s.Create(st); err != nil {
		t.Fatal(err)
	}
	got, err := s.Load(id1)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Health.Events) != MaxEvents || got.Health.Events[0].Detail != "f" || got.Health.Events[MaxEvents-1].Detail != "y" {
		t.Errorf("events = %d, first %q, last %q", len(got.Health.Events), got.Health.Events[0].Detail, got.Health.Events[len(got.Health.Events)-1].Detail)
	}
}

func TestDeadControllerVerdictPersisted(t *testing.T) {
	s, live := newStore(t, true)
	if err := s.Create(running(id1, t0)); err != nil {
		t.Fatal(err)
	}
	alive, err := s.Load(id1)
	if err != nil || alive.State != RunRunning || !alive.Controller.Alive {
		t.Fatalf("a live run must be left alone: %+v, %v", alive, err)
	}

	live.alive = false
	dead, err := s.Load(id1)
	if err != nil {
		t.Fatal(err)
	}
	if dead.State != RunIncomplete || dead.Reason == nil || *dead.Reason != "controller died" || dead.Controller.Alive {
		t.Fatalf("verdict = %s / %v / alive %v", dead.State, dead.Reason, dead.Controller.Alive)
	}
	if dead.FinishedAt == nil || !dead.FinishedAt.Equal(t0.Add(time.Hour)) {
		t.Errorf("finished_at = %v", dead.FinishedAt)
	}
	wantStates := []DiscState{DiscDone, DiscFailed, DiscFailed, DiscBlocked}
	for i, d := range dead.Discs {
		if d.State != wantStates[i] {
			t.Errorf("disc %d = %s, want %s", i, d.State, wantStates[i])
		}
	}
	if r := dead.Discs[1].Reason; r == nil || *r != "The controller died before this disc finished." {
		t.Errorf("disc 1 reason = %v", r)
	}
	if r := dead.Discs[3].Reason; r == nil || *r != "The drive links at 12 Mbit/s." {
		t.Errorf("a blocked disc must keep its own reason, got %v", r)
	}
	if len(dead.Problems) != 3 || dead.Problems[0].Serial != "B" || dead.Problems[2].State != DiscBlocked {
		t.Errorf("problems = %+v", dead.Problems)
	}

	// The verdict is on disk: it survives a liveness source that now claims life.
	live.alive = true
	again, err := s.Load(id1)
	if err != nil {
		t.Fatal(err)
	}
	if again.State != RunIncomplete || *again.Reason != "controller died" || len(again.Problems) != 3 {
		t.Errorf("verdict was not persisted: %s / %v", again.State, again.Reason)
	}
	if !bytes.Contains(rawState(t, s, id1), []byte(`"state": "incomplete"`)) {
		t.Error("state.json still says the run is running")
	}
}

func TestFinalizingRunWithDeadControllerIsConcluded(t *testing.T) {
	s, live := newStore(t, true)
	st := running(id1, t0)
	st.State = RunFinalizing
	if err := s.Create(st); err != nil {
		t.Fatal(err)
	}
	live.alive = false
	got, err := s.Load(id1)
	if err != nil || got.State != RunIncomplete {
		t.Fatalf("state = %v, err %v", got, err)
	}
}

func TestTerminalRunIsNotRewritten(t *testing.T) {
	s, live := newStore(t, false)
	st := running(id1, t0)
	st.State = RunComplete
	st.Discs = st.Discs[:1]
	if err := s.Create(st); err != nil {
		t.Fatal(err)
	}
	before := rawState(t, s, id1)
	got, err := s.Load(id1)
	if err != nil || got.State != RunComplete || got.Reason != nil {
		t.Fatalf("got %+v, %v", got, err)
	}
	if !bytes.Equal(before, rawState(t, s, id1)) {
		t.Error("loading a finished run rewrote its state")
	}
	if len(live.asked) != 1 || live.asked[0] != id1 {
		t.Errorf("liveness asked about %v", live.asked)
	}
}

func TestLoadLivenessError(t *testing.T) {
	s, live := newStore(t, true)
	if err := s.Create(running(id1, t0)); err != nil {
		t.Fatal(err)
	}
	live.err = errors.New("lease unreadable")
	if _, err := s.Load(id1); err == nil || !strings.Contains(err.Error(), "lease unreadable") {
		t.Errorf("err = %v", err)
	}
}

func TestSaveLeavesNoTempFileOnFailure(t *testing.T) {
	s, _ := newStore(t, true)
	if err := s.Create(running(id1, t0)); err != nil {
		t.Fatal(err)
	}
	// A directory where state.json belongs makes the final rename fail.
	path := filepath.Join(s.Dir(id1), "state.json")
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(path, "keep"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := s.Save(running(id1, t0)); err == nil {
		t.Fatal("Save succeeded over a directory")
	}
	entries, err := os.ReadDir(s.Dir(id1))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() != "state.json" {
			t.Errorf("left behind: %s", e.Name())
		}
	}
}

func TestCreateRefusesExistingDir(t *testing.T) {
	s, _ := newStore(t, true)
	if err := os.Mkdir(s.Dir(id1), 0o770); err != nil {
		t.Fatal(err)
	}
	err := s.Create(running(id1, t0))
	if !errors.Is(err, os.ErrExist) {
		t.Fatalf("err = %v, want it to wrap ErrExist", err)
	}
	if _, statErr := os.Stat(filepath.Join(s.Dir(id1), "state.json")); statErr == nil {
		t.Error("Create wrote into a directory that already existed")
	}
}

func TestModes(t *testing.T) {
	old := syscall.Umask(0o077)
	defer syscall.Umask(old)

	s, _ := newStore(t, true)
	if err := s.Create(running(id1, t0)); err != nil {
		t.Fatal(err)
	}
	if err := s.Save(running(id1, t0)); err != nil {
		t.Fatal(err)
	}
	dir, err := os.Stat(s.Dir(id1))
	if err != nil {
		t.Fatal(err)
	}
	file, err := os.Stat(filepath.Join(s.Dir(id1), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	if dir.Mode().Perm() != 0o770 || file.Mode().Perm() != 0o660 {
		t.Errorf("dir %v, file %v; want 0770 and 0660 whatever the umask", dir.Mode().Perm(), file.Mode().Perm())
	}
}

func TestNewID(t *testing.T) {
	now := time.Date(2026, 11, 9, 21, 50, 32, 0, time.UTC)
	got, err := NewID(now, bytes.NewReader([]byte{0x4e, 0x78}))
	if err != nil || got != "20261109-215032-4e78" {
		t.Fatalf("NewID = %q, %v", got, err)
	}
	if !idPattern.MatchString(got) {
		t.Errorf("%q does not match the id pattern", got)
	}
	if _, err := NewID(now, bytes.NewReader([]byte{1})); err == nil {
		t.Error("a short random source must be an error")
	}
}

func TestErrNoRun(t *testing.T) {
	// A valid schema-2 state one level above the store: a traversal id would find it.
	parent := t.TempDir()
	if err := os.Mkdir(filepath.Join(parent, "x"), 0o770); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(parent, "x", "state.json"), []byte(`{"schema":2,"run_id":"../x"}`), 0o660); err != nil {
		t.Fatal(err)
	}
	live := &fakeLive{alive: true}
	s := &Store{Root: filepath.Join(parent, "runs"), Live: live}
	if err := os.Mkdir(s.Root, 0o770); err != nil {
		t.Fatal(err)
	}

	for _, id := range []string{
		"", "../x", "..", "../../etc", id1 + "/../x", "x/y", "20261113-120000-A41C", "20261113-120000-a41", "20261113-120000-a41cc",
		"aborted-20261109-204732-42bf", "discinfo", "20261113-120000-a41c\n", "20261113-120000-zzzz",
		id1, // well-formed but absent
	} {
		if _, err := s.Load(id); !errors.Is(err, ErrNoRun) {
			t.Errorf("Load(%q) = %v, want ErrNoRun", id, err)
		}
	}
	for _, id := range []string{"../x", "..", ""} {
		if err := s.Save(&State{RunID: id}); !errors.Is(err, ErrNoRun) {
			t.Errorf("Save(%q) = %v, want ErrNoRun", id, err)
		}
		if err := s.Create(&State{RunID: id}); !errors.Is(err, ErrNoRun) {
			t.Errorf("Create(%q) = %v, want ErrNoRun", id, err)
		}
	}
	if len(live.asked) != 1 || live.asked[0] != id1 {
		t.Errorf("only the well-formed id may reach the liveness probe, got %v", live.asked)
	}
	if data, _ := os.ReadFile(filepath.Join(parent, "x", "state.json")); string(data) != `{"schema":2,"run_id":"../x"}` {
		t.Errorf("a traversal id wrote outside the store: %s", data)
	}
}

func TestLoadRejectsWrongSchemaAndMismatchedID(t *testing.T) {
	s, _ := newStore(t, true)
	write := func(id, body string) {
		t.Helper()
		if err := os.Mkdir(s.Dir(id), 0o770); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(s.Dir(id), "state.json"), []byte(body), 0o660); err != nil {
			t.Fatal(err)
		}
	}
	write("20261109-215032-4e78", `{"run_id":"20261109-215032-4e78","state":"running","started":"2026-11-09","discs":[{"progress":"12%"}]}`)
	write("20261109-215033-4e78", `{"schema":1,"run_id":"20261109-215033-4e78"}`)
	write("20261109-215034-4e78", `{"schema":"2"}`)
	write("20261109-215035-4e78", `not json`)
	write("20261109-215036-4e78", `{"schema":2,"run_id":"someone-else"}`)
	for _, id := range []string{"20261109-215032-4e78", "20261109-215033-4e78", "20261109-215034-4e78", "20261109-215035-4e78"} {
		if _, err := s.Load(id); !errors.Is(err, ErrNoRun) {
			t.Errorf("Load(%s) = %v, want ErrNoRun", id, err)
		}
	}
	if _, err := s.Load("20261109-215036-4e78"); err == nil || errors.Is(err, ErrNoRun) {
		t.Errorf("a state naming another run must be an error, got %v", err)
	}
}

func TestListIgnoresNonSchema2AndSortsNewestFirst(t *testing.T) {
	s, live := newStore(t, true)
	older := "20261112-090000-0001"
	newer := "20261113-090000-0002"
	same := "20261113-090000-0003"
	for _, st := range []*State{running(older, t0.Add(-24*time.Hour)), running(newer, t0), running(same, t0)} {
		if err := s.Create(st); err != nil {
			t.Fatal(err)
		}
	}
	finished := t0.Add(time.Hour)
	done := running("20261113-080000-0004", t0.Add(-time.Hour))
	done.State, done.FinishedAt, done.Kind = RunComplete, &finished, KindIdentify
	done.Discs = nil
	if err := s.Create(done); err != nil {
		t.Fatal(err)
	}

	// None of these is a run to this tool.
	mk := func(rel, body string) {
		t.Helper()
		path := filepath.Join(s.Root, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o770); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o660); err != nil {
			t.Fatal(err)
		}
	}
	mk("20261109-215032-4e78/state.json", `{"run_id":"20261109-215032-4e78","state":"complete","started":"2026-11-09T21:50:32-06:00","discs":[{"progress":"12%","titles_done":3}]}`)
	mk("20261109-215033-4e78/state.json", `{"schema":1,"state":"complete"}`)
	mk("20261109-215034-4e78/state.json", `{broken`)
	mk("aborted-20261109-204732-42bf/state.json", `{"schema":2,"run_id":"aborted-20261109-204732-42bf","state":"running"}`)
	mk("discinfo/BP52.txt", "label")
	mk("finalize.lock", "")
	if err := os.Mkdir(filepath.Join(s.Root, "20261109-215035-4e78"), 0o770); err != nil {
		t.Fatal(err)
	}

	got, err := s.List()
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, r := range got {
		ids = append(ids, r.RunID)
	}
	want := []string{same, newer, "20261113-080000-0004", older}
	if !reflect.DeepEqual(ids, want) {
		t.Fatalf("list = %v, want %v", ids, want)
	}
	if got[2].Kind != KindIdentify || got[2].State != RunComplete || got[2].FinishedAt == nil || !got[2].FinishedAt.Equal(finished) {
		t.Errorf("summary = %+v", got[2])
	}
	// List goes through Load, so a dead run is concluded on the way.
	live.alive = false
	got, err = s.List()
	if err != nil || got[0].State != RunIncomplete {
		t.Errorf("a dead run listed as %v (%v)", got[0].State, err)
	}
}

func TestListEmpty(t *testing.T) {
	s, _ := newStore(t, true)
	got, err := s.List()
	if err != nil || got == nil || len(got) != 0 {
		t.Errorf("List = %#v, %v; want an empty non-nil slice", got, err)
	}
	s.Root = filepath.Join(s.Root, "missing")
	if got, err := s.List(); err != nil || len(got) != 0 {
		t.Errorf("a missing root is no runs, got %v, %v", got, err)
	}
	if out, _ := json.Marshal(got); string(out) != "[]" {
		t.Errorf("JSON = %s, want []", out)
	}
}

func TestProblems(t *testing.T) {
	discs := []Disc{
		{Index: 0, Serial: "A", Label: "a", State: DiscDone},
		{Index: 1, Serial: "B", Label: "b", State: DiscFailed, Reason: str("boom")},
		{Index: 2, Serial: "C", Label: "c", State: DiscPending},
	}
	got := Problems(discs)
	want := []Problem{
		{Index: 1, Serial: "B", Label: "b", State: DiscFailed, Reason: "boom"},
		{Index: 2, Serial: "C", Label: "c", State: DiscPending},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Problems = %+v", got)
	}
	if p := Problems(nil); p == nil || len(p) != 0 {
		t.Errorf("Problems(nil) = %#v, want empty non-nil", p)
	}
}
