package rip

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/reclyptor/mediaops/internal/cli"
	"github.com/reclyptor/mediaops/internal/drives"
	"github.com/reclyptor/mediaops/internal/lease"
	"github.com/reclyptor/mediaops/internal/runstore"
	"github.com/reclyptor/mediaops/kit/paths"
	"github.com/reclyptor/mediaops/kit/proc"
)

// The test binary plays several parts, chosen by roleEnv. Run by `go test` it
// is the tests. Re-executed by a test it is mkv-rip itself, a controller that
// never answers, or one of the programs mkv-rip runs: makemkvcon, ffprobe,
// mkvpropedit and mkvmerge, as symlinks to the binary named for the
// program they stand in for.
const (
	roleEnv    = "RIP_TEST_ROLE"
	roleCLI    = "cli"
	roleTool   = "tool"
	roleSilent = "silent"

	dirEnv     = "RIP_TEST_DIR"     // the tree a process rig lives in
	holdEnv    = "RIP_TEST_HOLD"    // makemkvcon waits for the release file before it prints anything
	fixtureEnv = "RIP_TEST_FIXTURE" // which recorded disc makemkvcon replays
	scaleEnv   = "RIP_TEST_FILESCALE"
	markerEnv  = "RIP_TEST_MARKER" // the file a silent controller writes to show it carried on
)

func TestMain(m *testing.M) {
	switch os.Getenv(roleEnv) {
	case "":
		os.Exit(m.Run())
	case roleCLI:
		cli.Main("mkv-rip", func(ctx context.Context, args []string, streams cli.IO) error {
			return Command(ctx, roleDeps(), args, streams)
		})
	case roleTool:
		os.Exit(runTool())
	case roleSilent:
		time.Sleep(500 * time.Millisecond)
		os.Exit(exitCode(os.WriteFile(os.Getenv(markerEnv), nil, 0o644)))
	}
	os.Exit(2)
}

func exitCode(err error) int {
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}

func fakeStatDev(path string) (uint64, error) {
	if path == "/" {
		return 1, nil
	}
	return 2, nil
}

// roleDeps is the mkv-rip a process rig runs: every path under its tree, a
// drive list read from a file, and every program the fake binary.
func roleDeps() Deps {
	dir := os.Getenv(dirEnv)
	cfg := paths.Config{MediaRoot: filepath.Join(dir, "media"), StatDev: fakeStatDev}
	leasePath := filepath.Join(dir, "lease")
	return Deps{
		Paths:  cfg,
		Drives: fileDrives{path: filepath.Join(dir, "drives.json")},
		Runner: &proc.Exec{
			Grace: 2 * time.Second,
			Env:   []string{roleEnv + "=" + roleTool},
			Resolve: func(name string) (string, error) {
				// dmesg is polled every few milliseconds and a process costs far more
				// than that under -race; the health watch reports it unreadable.
				if name == "dmesg" {
					return "", errors.New("dmesg is not available in the test pod")
				}
				return filepath.Join(dir, "tools", name), nil
			},
		},
		Store:       &runstore.Store{Root: cfg.RipsRoot(), Live: leaseLiveness{path: leasePath}},
		Clock:       systemClock{},
		LeasePath:   leasePath,
		SysBlock:    filepath.Join(dir, "sys", "block"),
		ToolVersion: "test",
		Exe:         "/proc/self/exe",
		Tick:        20 * time.Millisecond, HealthSysfsEvery: 20 * time.Millisecond, HealthDmesgEvery: 200 * time.Millisecond,
		LeaseWait: 400 * time.Millisecond, HandshakeTimeout: 20 * time.Second, PollEvery: 50 * time.Millisecond,
	}
}

// driveFile is a drive as the drives file holds it, with the sysfs directory
// the JSON form of a Drive leaves out.
type driveFile struct {
	drives.Drive
	USBDir string `json:"usb_dir"`
}

// fileDrives is Drives over a file the test rewrites, shared with the
// processes that read it. Ejecting is a no-op.
type fileDrives struct{ path string }

func (f fileDrives) List(context.Context) ([]drives.Drive, error) {
	data, err := os.ReadFile(f.path)
	if err != nil {
		return nil, err
	}
	var entries []driveFile
	if err := json.Unmarshal(data, &entries); err != nil {
		return nil, err
	}
	out := make([]drives.Drive, len(entries))
	for i, e := range entries {
		out[i] = e.Drive
		out[i].USBDir = e.USBDir
	}
	return out, nil
}

func (fileDrives) Eject(context.Context, drives.Drive) error { return nil }

func runTool() int {
	dir := os.Getenv(dirEnv)
	args := os.Args[1:]
	switch filepath.Base(os.Args[0]) {
	case "makemkvcon":
		return fakeMakemkvcon(dir, args)
	case "ffprobe":
		data, err := os.ReadFile(args[len(args)-1])
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		fmt.Printf(`{"format":{"duration":%q}}`, strings.TrimSpace(string(data)))
		return 0
	case "mkvpropedit":
		line := args[0] + "\t" + strings.TrimPrefix(args[len(args)-1], "title=") + "\n"
		file, err := os.OpenFile(filepath.Join(dir, "titles.tsv"), os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o644)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		_, err = file.WriteString(line)
		return exitCode(errors.Join(err, file.Close()))
	case "mkvmerge":
		data, _ := os.ReadFile(filepath.Join(dir, "titles.tsv"))
		var title string
		for _, line := range strings.Split(string(data), "\n") {
			if file, t, ok := strings.Cut(line, "\t"); ok && file == args[len(args)-1] {
				title = t
			}
		}
		out, _ := json.Marshal(map[string]any{"container": map[string]any{"properties": map[string]any{"title": title}}})
		fmt.Println(string(out))
		return 0
	}
	fmt.Fprintf(os.Stderr, "no fake for %s\n", os.Args[0])
	return 127
}

// fakeMakemkvcon replays a recorded disc: the info log for `info`, the rip log
// for `mkv` with the files appearing in the destination. Each file holds its
// length in seconds, which is what the fake ffprobe reports. With holdEnv set
// it first records its pid and waits for the release file.
func fakeMakemkvcon(dir string, args []string) int {
	_, verb := makemkvInvocation(args)
	disc := fakeDisc{Fixture: os.Getenv(fixtureEnv)}
	if os.Getenv(holdEnv) != "" {
		pid := []byte(strconv.Itoa(os.Getpid()))
		if err := os.WriteFile(filepath.Join(dir, "makemkvcon-"+verb+".pid"), pid, 0o644); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		for !fileExists(filepath.Join(dir, "release")) {
			time.Sleep(10 * time.Millisecond)
		}
	}
	if verb == "info" {
		for _, line := range readFixture(disc.fixture() + "/info.log") {
			fmt.Println(line)
		}
		return 0
	}

	scale := 1.0
	if s := os.Getenv(scaleEnv); s != "" {
		scale, _ = strconv.ParseFloat(s, 64)
	}
	lines := readFixture(disc.fixture() + "/rip.log")
	first := 0
	for i, l := range lines {
		if strings.HasPrefix(l, "PRGC:") && strings.Contains(l, "Saving to MKV file") {
			first = i
			break
		}
	}
	for _, line := range lines[:first] {
		fmt.Println(line)
	}
	for name, secs := range fixtureOutputs(disc) {
		if err := writeMedia(filepath.Join(args[len(args)-1], name), secs*scale); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
	}
	for _, line := range lines[first:] {
		fmt.Println(line)
	}
	return 0
}

// writeMedia makes a file the fake ffprobe will measure at secs.
func writeMedia(path string, secs float64) error {
	return os.WriteFile(path, []byte(strconv.FormatFloat(secs+0.321, 'f', 3, 64)), 0o666)
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// procRig is a tree for processes to share: the NAS under media, a lease, a
// drives file, a sysfs, and the symlinks that make the test binary every
// program mkv-rip runs.
type procRig struct {
	t      *testing.T
	dir    string
	sys    *fakeSys
	drives []drives.Drive
	cfg    paths.Config
	store  *runstore.Store
}

func newProcRig(t *testing.T, serials ...string) *procRig {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	p := &procRig{t: t, dir: dir, sys: &fakeSys{root: filepath.Join(dir, "sys")}}
	p.cfg = paths.Config{MediaRoot: filepath.Join(dir, "media"), StatDev: fakeStatDev}
	p.store = &runstore.Store{Root: p.cfg.RipsRoot(), Live: leaseLiveness{path: p.leasePath()}}
	for _, d := range []string{p.cfg.Staging(), p.sys.blockDir(), filepath.Join(dir, "tools"), filepath.Join(dir, "home")} {
		mustMkdir(t, d)
	}
	for _, tool := range []string{"makemkvcon", "ffprobe", "mkvpropedit", "mkvmerge"} {
		if err := os.Symlink(exe, filepath.Join(dir, "tools", tool)); err != nil {
			t.Fatal(err)
		}
	}
	for i, serial := range serials {
		p.drives = append(p.drives, p.sys.add(t, serial, i, fmt.Sprintf("5-1.%d", i+1)))
	}
	p.writeDrives()
	t.Cleanup(p.cleanup)
	return p
}

func (p *procRig) leasePath() string { return filepath.Join(p.dir, "lease") }

// writeDrives publishes p.drives to the processes.
func (p *procRig) writeDrives() {
	p.t.Helper()
	entries := make([]driveFile, len(p.drives))
	for i, d := range p.drives {
		entries[i] = driveFile{Drive: d, USBDir: d.USBDir}
	}
	data, err := json.Marshal(entries)
	if err != nil {
		p.t.Fatal(err)
	}
	path := filepath.Join(p.dir, "drives.json")
	if err := os.WriteFile(path+".tmp", data, 0o644); err != nil {
		p.t.Fatal(err)
	}
	if err := os.Rename(path+".tmp", path); err != nil {
		p.t.Fatal(err)
	}
}

// release lets every held fake makemkvcon go on.
func (p *procRig) release() {
	p.t.Helper()
	if err := os.WriteFile(filepath.Join(p.dir, "release"), nil, 0o644); err != nil {
		p.t.Fatal(err)
	}
}

// cleanup lets a failed test's controllers finish, and kills the one that does
// not, so no process outlives the test.
func (p *procRig) cleanup() {
	_ = os.WriteFile(filepath.Join(p.dir, "release"), nil, 0o644)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if st, err := lease.Probe(p.leasePath()); err == nil && !st.Held {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	if st, err := lease.Probe(p.leasePath()); err == nil && st.Holder.PID > 0 {
		_ = syscall.Kill(st.Holder.PID, syscall.SIGKILL)
	}
}

// command is mkv-rip with args, as a process of its own.
func (p *procRig) command(env []string, stdin string, args ...string) *exec.Cmd {
	exe, err := os.Executable()
	if err != nil {
		p.t.Fatal(err)
	}
	cmd := exec.Command(exe, args...)
	cmd.Env = append(os.Environ(),
		roleEnv+"="+roleCLI, cli.RoleEnv+"="+cli.RoleRip, noExitSleep, dirEnv+"="+p.dir, "HOME="+filepath.Join(p.dir, "home"), "MAKEMKV_KEY=testkey-secret")
	cmd.Env = append(cmd.Env, env...)
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	return cmd
}

// outcome is how a finished mkv-rip ended.
type outcome struct {
	stdout, stderr string
	code           int
}

// run runs mkv-rip to its end.
func (p *procRig) run(env []string, stdin string, args ...string) outcome {
	p.t.Helper()
	cmd := p.command(env, stdin, args...)
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	done := make(chan error, 1)
	if err := cmd.Start(); err != nil {
		p.t.Fatal(err)
	}
	go func() { done <- cmd.Wait() }()
	select {
	case <-done:
	case <-time.After(60 * time.Second):
		_ = cmd.Process.Kill()
		p.t.Fatalf("mkv-rip %v did not finish in 60s", args)
	}
	return outcome{out.String(), errOut.String(), cmd.ProcessState.ExitCode()}
}

func (p *procRig) ok(env []string, stdin string, args ...string) outcome {
	p.t.Helper()
	r := p.run(env, stdin, args...)
	if r.code != 0 {
		p.t.Fatalf("mkv-rip %v exited %d: %s", args, r.code, r.stderr)
	}
	return r
}

func (p *procRig) runID(r outcome) string {
	p.t.Helper()
	var ref runRef
	if err := json.Unmarshal([]byte(r.stdout), &ref); err != nil || ref.RunID == "" {
		p.t.Fatalf("stdout %q is not a run id (%v)", r.stdout, err)
	}
	return ref.RunID
}

// planFile writes a plan of one Penguindrum disc per serial, with no spin-up
// gap so a run needs no waiting.
func (p *procRig) planFile(serials ...string) string {
	p.t.Helper()
	return writePlan(p.t, p.dir, serials...)
}

func writePlan(t *testing.T, dir string, serials ...string) string {
	t.Helper()
	var entries []string
	for i, serial := range serials {
		entries = append(entries, fmt.Sprintf(`{"serial":%q,"category":"anime","series":"Penguindrum","season":1,"disc":%d}`, serial, i+1))
	}
	path := filepath.Join(dir, "plan.json")
	body := `{"spinup_gap_seconds":0,"discs":[` + strings.Join(entries, ",") + `]}`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// state is a run's state as `status` would read it.
func (p *procRig) state(id string) *runstore.State {
	p.t.Helper()
	st, err := p.store.Load(id)
	if err != nil {
		p.t.Fatal(err)
	}
	return st
}

// until polls cond for up to 30 s.
func (p *procRig) until(what string, cond func() bool) {
	p.t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	p.t.Fatalf("timed out waiting for %s", what)
}

// runDirs is the names under .rips.
func (p *procRig) runDirs() []string {
	entries, _ := os.ReadDir(p.cfg.RipsRoot())
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

func (p *procRig) pidFile(name string) int {
	data, err := os.ReadFile(filepath.Join(p.dir, name))
	if err != nil {
		return 0
	}
	pid, _ := strconv.Atoi(string(data))
	return pid
}

var holdMakemkv = []string{holdEnv + "=1"}

var halfLength = []string{scaleEnv + "=0.5"}

// failedRun runs one disc through a real start whose files measure half their
// length, so it fails verification and keeps its .partial. fixPartial then
// makes them right.
func (p *procRig) failedRun(serial string) (id string, disc runstore.Disc) {
	p.t.Helper()
	id = p.runID(p.ok(halfLength, "", "start", p.planFile(serial)))
	if r := p.run(nil, "", "wait", id); r.code != 1 {
		p.t.Fatalf("setup: wait exited %d: %s", r.code, r.stderr)
	}
	st := p.state(id)
	disc = st.Discs[0]
	if disc.State != runstore.DiscFailed || !fileExists(disc.Dest+".partial") {
		p.t.Fatalf("setup: disc = %s %q", disc.State, deref(disc.Reason))
	}
	return id, disc
}

func (p *procRig) fixPartial(disc runstore.Disc) {
	p.t.Helper()
	for name, secs := range fixtureOutputs(fakeDisc{}) {
		if err := writeMedia(filepath.Join(disc.Dest+".partial", name), secs); err != nil {
			p.t.Fatal(err)
		}
	}
}

// noExitSleep stops a race-enabled process sleeping a second at exit, which
// would otherwise be added to every program a run starts.
const noExitSleep = "GORACE=atexit_sleep_ms=0"
