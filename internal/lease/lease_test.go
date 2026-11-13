package lease

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

const helperEnv = "LEASE_TEST_HELPER_PATH"

// The test binary doubles as a child that takes the lease and sits on it.
func TestMain(m *testing.M) {
	path := os.Getenv(helperEnv)
	if path == "" {
		os.Exit(m.Run())
	}
	if _, err := Acquire(context.Background(), path, Holder{Kind: "rip", RunID: "child", PID: os.Getpid()}, 0); err != nil {
		os.Stderr.WriteString(err.Error() + "\n")
		os.Exit(2)
	}
	os.Stdout.WriteString("ready\n")
	time.Sleep(time.Minute)
}

func leasePath(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "mkv-rip.lease")
}

func TestLeaseExclusive(t *testing.T) {
	path := leasePath(t)
	holder := Holder{Kind: "rip", RunID: "20260101-000000-abcd", PID: 412, StartedAt: time.Unix(1700000000, 0).UTC()}
	first, err := Acquire(t.Context(), path, holder, 0)
	if err != nil {
		t.Fatal(err)
	}

	start := time.Now()
	_, err = Acquire(t.Context(), path, Holder{Kind: "finalize"}, 250*time.Millisecond)
	var held *HeldError
	if !errors.As(err, &held) {
		t.Fatalf("second Acquire = %v, want *HeldError", err)
	}
	if waited := time.Since(start); waited < 250*time.Millisecond {
		t.Errorf("second Acquire gave up after %v, before its wait", waited)
	}
	if held.Holder != holder {
		t.Errorf("HeldError holder = %+v, want %+v", held.Holder, holder)
	}
	for _, want := range []string{"rip", holder.RunID, "412"} {
		if !strings.Contains(held.Error(), want) {
			t.Errorf("HeldError %q does not name %q", held.Error(), want)
		}
	}

	if err := first.Release(); err != nil {
		t.Fatal(err)
	}
	second, err := Acquire(t.Context(), path, Holder{Kind: "finalize"}, 0)
	if err != nil {
		t.Fatalf("Acquire after Release: %v", err)
	}
	second.Release()
}

func TestAcquireWaitsForRelease(t *testing.T) {
	path := leasePath(t)
	first, err := Acquire(t.Context(), path, Holder{Kind: "rip"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		time.Sleep(150 * time.Millisecond)
		first.Release()
	}()
	second, err := Acquire(t.Context(), path, Holder{Kind: "identify"}, 5*time.Second)
	if err != nil {
		t.Fatalf("Acquire within its wait: %v", err)
	}
	second.Release()
}

func TestAcquireCancelled(t *testing.T) {
	path := leasePath(t)
	first, err := Acquire(t.Context(), path, Holder{Kind: "rip"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Release()

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := Acquire(ctx, path, Holder{Kind: "rip"}, time.Minute); !errors.Is(err, context.Canceled) {
		t.Errorf("Acquire with a cancelled context = %v, want context.Canceled", err)
	}
}

func TestHeldErrorUnknownHolder(t *testing.T) {
	if got := (&HeldError{}).Error(); !strings.Contains(got, "an unknown process") {
		t.Errorf("Error() = %q", got)
	}
}

func TestProbeIsShared(t *testing.T) {
	path := leasePath(t)
	holder := Holder{Kind: "rip", RunID: "run-1", PID: 7}

	if st, err := Probe(path); err != nil || st.Held {
		t.Fatalf("Probe of a missing file = %+v, %v; want not held", st, err)
	}

	l, err := Acquire(t.Context(), path, holder, 0)
	if err != nil {
		t.Fatal(err)
	}
	for range 3 {
		st, err := Probe(path)
		if err != nil {
			t.Fatal(err)
		}
		if !st.Held || st.Holder != holder {
			t.Fatalf("Probe of a held lease = %+v, want held by %+v", st, holder)
		}
	}
	// The holder is undisturbed: its file still carries its description.
	if st, _ := Probe(path); st.Holder != holder {
		t.Errorf("a probe disturbed the holder: %+v", st.Holder)
	}
	if err := l.Release(); err != nil {
		t.Fatal(err)
	}

	if st, err := Probe(path); err != nil || st.Held {
		t.Fatalf("Probe after Release = %+v, %v; want not held", st, err)
	}
	// A probe must not leave a shared lock behind that would refuse a controller.
	l, err = Acquire(t.Context(), path, holder, 0)
	if err != nil {
		t.Fatalf("Acquire right after a probe: %v", err)
	}
	l.Release()
}

func TestProbeHeldWithUnreadableContent(t *testing.T) {
	path := leasePath(t)
	l, err := Acquire(t.Context(), path, Holder{Kind: "rip", RunID: "x", PID: 1}, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Release()
	if err := os.WriteFile(path, []byte(`{"kind":"ri`), 0o660); err != nil {
		t.Fatal(err)
	}
	st, err := Probe(path)
	if err != nil {
		t.Fatal(err)
	}
	if !st.Held || st.Holder != (Holder{}) {
		t.Errorf("Probe = %+v, want held with a zero Holder", st)
	}
}

func TestHolderContentRoundTrips(t *testing.T) {
	path := leasePath(t)
	want := Holder{Kind: "finalize", RunID: "20260101-000000-abcd", PID: 99, StartedAt: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)}
	l, err := Acquire(t.Context(), path, want, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Release()

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatalf("lease content is not JSON: %v", err)
	}
	for _, key := range []string{"kind", "run_id", "pid", "started_at"} {
		if _, ok := fields[key]; !ok {
			t.Errorf("lease content lacks %q: %s", key, raw)
		}
	}
	var got Holder
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Errorf("round trip = %+v, want %+v", got, want)
	}
}

func TestReleaseOnProcessDeath(t *testing.T) {
	path := leasePath(t)
	cmd := exec.Command(os.Args[0])
	cmd.Env = append(os.Environ(), helperEnv+"="+path)
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cmd.Process.Kill(); cmd.Wait() })

	if line, err := bufio.NewReader(out).ReadString('\n'); err != nil || line != "ready\n" {
		t.Fatalf("child did not take the lease: %q, %v", line, err)
	}
	st, err := Probe(path)
	if err != nil || !st.Held || st.Holder.PID != cmd.Process.Pid {
		t.Fatalf("Probe with the child alive = %+v, %v; want held by pid %d", st, err, cmd.Process.Pid)
	}

	if err := cmd.Process.Signal(syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	cmd.Wait()

	st, err = Probe(path)
	if err != nil || st.Held {
		t.Fatalf("Probe after SIGKILL = %+v, %v; want not held", st, err)
	}
	l, err := Acquire(t.Context(), path, Holder{Kind: "rip"}, 0)
	if err != nil {
		t.Fatalf("Acquire after the holder died: %v", err)
	}
	l.Release()
}

func TestPidReuseIsNotLiveness(t *testing.T) {
	path := leasePath(t)
	stale := Holder{Kind: "rip", RunID: "dead-run", PID: os.Getpid(), StartedAt: time.Now()}
	raw, _ := json.Marshal(stale)
	if err := os.WriteFile(path, raw, 0o660); err != nil {
		t.Fatal(err)
	}
	st, err := Probe(path)
	if err != nil {
		t.Fatal(err)
	}
	if st.Held {
		t.Fatalf("a lease file naming a live pid but locked by nobody was reported held: %+v", st)
	}
	l, err := Acquire(t.Context(), path, Holder{Kind: "rip"}, 0)
	if err != nil {
		t.Fatalf("Acquire over a stale file: %v", err)
	}
	l.Release()
}
