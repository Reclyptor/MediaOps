package proc

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

// The test binary doubles as the child: PROC_HELPER selects what it does.
func TestMain(m *testing.M) {
	switch os.Getenv("PROC_HELPER") {
	case "":
		os.Exit(m.Run())
	case "lines":
		fmt.Fprint(os.Stdout, "one\ntwo\xff\r\n")
		fmt.Fprint(os.Stderr, "three")
	case "exit3":
		os.Exit(3)
	case "pdeathsig":
		var sig int32
		if err := unix.Prctl(unix.PR_GET_PDEATHSIG, uintptr(unsafe.Pointer(&sig)), 0, 0, 0); err != nil {
			os.Exit(2)
		}
		fmt.Println(sig)
	case "sleep":
		time.Sleep(time.Minute)
	case "ignoreterm":
		signal.Ignore(syscall.SIGTERM)
		fmt.Println("ready")
		time.Sleep(time.Minute)
	}
	os.Exit(0)
}

func helper(t *testing.T, mode string) *Exec {
	t.Helper()
	return &Exec{
		Grace:   200 * time.Millisecond,
		Env:     []string{"PROC_HELPER=" + mode},
		Resolve: func(string) (string, error) { return os.Args[0], nil },
	}
}

func TestRunReportsExitCode(t *testing.T) {
	res, err := helper(t, "exit3").Run(context.Background(), "makemkvcon")
	if err != nil {
		t.Fatalf("a non-zero exit is not an error: %v", err)
	}
	if res.ExitCode != 3 {
		t.Fatalf("exit code = %d, want 3", res.ExitCode)
	}
}

func TestStreamSplitsLinesAndRepairsUTF8(t *testing.T) {
	var lines []string
	code, err := helper(t, "lines").Stream(context.Background(), []string{"makemkvcon"}, func(l string) { lines = append(lines, l) })
	if err != nil || code != 0 {
		t.Fatalf("Stream = %d, %v", code, err)
	}
	want := []string{"one", "two�", "three"}
	if strings.Join(lines, "|") != strings.Join(want, "|") {
		t.Fatalf("lines = %q, want %q", lines, want)
	}
}

func TestEveryChildHasPdeathsig(t *testing.T) {
	res, err := helper(t, "pdeathsig").Run(context.Background(), "ffprobe")
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(res.Stdout)); got != fmt.Sprint(int(syscall.SIGKILL)) {
		t.Fatalf("child's PR_GET_PDEATHSIG = %q, want %d", got, syscall.SIGKILL)
	}
}

func TestCancelTerminatesChild(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	code, err := helper(t, "sleep").Stream(ctx, []string{"makemkvcon"}, func(string) {})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want deadline exceeded", err)
	}
	if code != -1 {
		t.Fatalf("exit code = %d, want -1 for a signalled child", code)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatal("child outlived its context")
	}
}

func TestCancelKillsAChildThatIgnoresSIGTERM(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ready := make(chan struct{})
	var once sync.Once
	finished := make(chan int, 1)
	go func() {
		code, _ := helper(t, "ignoreterm").Stream(ctx, []string{"makemkvcon"}, func(string) { once.Do(func() { close(ready) }) })
		finished <- code
	}()
	<-ready
	cancel()
	select {
	case code := <-finished:
		if code != -1 {
			t.Fatalf("exit code = %d, want -1 after SIGKILL", code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("SIGKILL never followed the grace period")
	}
}

func TestAllowlist(t *testing.T) {
	for _, argv := range [][]string{
		{"python3", "-c", "1"},
		{"/usr/bin/ffprobe", "x"},
		{"sh", "-c", "true"},
		{},
	} {
		if err := Check(argv); !errors.Is(err, ErrRefused) {
			t.Errorf("Check(%q) = %v, want ErrRefused", argv, err)
		}
	}
	for _, argv := range [][]string{
		{"makemkvcon", "-r", "info", "disc:9999"},
		{"mkvpropedit", "f.mkv", "--edit", "info", "--set", "title=x"},
		{"ffprobe", "-v", "error", "f.mkv"},
		{"sg_turs", "/dev/sg0"}, {"sg_prevent", "--allow", "/dev/sg0"}, {"sg_start", "--eject", "/dev/sg0"},
		{"dmesg"},
	} {
		if err := Check(argv); err != nil {
			t.Errorf("Check(%q) = %v, want nil", argv, err)
		}
	}
}

func TestNoEncoderArgv(t *testing.T) {
	refused := [][]string{
		{"ffmpeg", "-i", "in.mkv", "out.mkv"},
		{"ffmpeg", "-i", "in.mkv", "-c:v", "libx264", "out.mp4"},
		{"ffmpeg", "-i", "in.mkv", "-c", "copy", "-c:a", "aac", "out.mkv"},
		{"ffmpeg", "-i", "in.mkv", "-vcodec"},
		{"ffmpeg", "-i", "in.mkv", "-acodec", "libopus", "-f", "matroska", "-"},
		{"ffmpeg", "-i", "in.mkv", "-f", "matroska", "-"},
		{"ffmpeg", "-i", "in.mkv", "-f", "rawvideo", "out.yuv"},
		{"ffmpeg"},
		{"mkvmerge", "--version"},
		{"mkvmerge"},
	}
	for _, argv := range refused {
		if err := Check(argv); !errors.Is(err, ErrRefused) {
			t.Errorf("Check(%q) = %v, want ErrRefused", argv, err)
		}
	}
	allowedArgv := [][]string{
		{"ffmpeg", "-v", "error", "-i", "in.mkv", "-f", "rawvideo", "-pix_fmt", "gray", "-"},
		{"ffmpeg", "-i", "in.mkv", "-map", "0:v:0", "-c", "copy", "-f", "framemd5", "-"},
		{"ffmpeg", "-i", "in.mkv", "-f", "null", "-"},
		{"ffmpeg", "-ss", "10", "-i", "in.mkv", "-frames:v", "1", "frame.png"},
		{"ffmpeg", "-f", "lavfi", "-i", "color", "-frames:v", "1", "frame.jpg"},
		{"mkvmerge", "-J", "in.mkv"},
		{"mkvmerge", "--identify", "in.mkv"},
		{"mkvmerge", "-o", "out.mkv", "--split", "chapters:2", "in.mkv"},
	}
	for _, argv := range allowedArgv {
		if err := Check(argv); err != nil {
			t.Errorf("Check(%q) = %v, want nil", argv, err)
		}
	}
}

func TestRunRefusesBeforeStarting(t *testing.T) {
	_, err := (&Exec{Resolve: func(string) (string, error) {
		t.Fatal("a refused argv must not be resolved")
		return "", nil
	}}).Run(context.Background(), "ffmpeg", "-i", "a", "b.mkv")
	if !errors.Is(err, ErrRefused) {
		t.Fatalf("err = %v, want ErrRefused", err)
	}
}
