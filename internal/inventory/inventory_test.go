package inventory

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/reclyptor/mediaops/internal/cli"
)

func TestReadRoundTripsAProbeDocument(t *testing.T) {
	rel := "anime/Show/a.mkv"
	path := filepath.Join(t.TempDir(), "inv.json")
	writeInventory(t, path, Inventory{
		Roots: []string{"/m"}, Probed: 2, Errors: 1, Skipped: []string{"/m/fifo"},
		Files: []Record{
			{Path: "/m/a.mkv", Rel: &rel, Size: 7, Duration: 1425.5, Chapters: []float64{0, 335.669}, Streams: []Stream{{Index: 1, Type: "audio", Channels: 2}}},
			{Path: "/m/b.mkv", Error: "ffprobe exited 1"},
		},
	})
	inv, err := Read(path)
	if err != nil {
		t.Fatal(err)
	}
	if inv.Probed != 2 || inv.Errors != 1 || len(inv.Files) != 2 || inv.Skipped[0] != "/m/fifo" {
		t.Fatalf("inventory = %+v", inv)
	}
	a := inv.Files[0]
	if *a.Rel != rel || a.Duration != 1425.5 || a.Chapters[1] != 335.669 || a.Streams[0].Channels != 2 {
		t.Errorf("record = %+v", a)
	}

	by := inv.ByPath()
	if _, ok := by["/m/a.mkv"]; !ok || len(by) != 1 {
		t.Errorf("ByPath = %v, want only the success record", by)
	}
}

func TestReadRefusesWhatIsNotAnInventory(t *testing.T) {
	dir := t.TempDir()
	fifo := filepath.Join(dir, "fifo")
	if err := syscall.Mkfifo(fifo, 0o644); err != nil {
		t.Fatal(err)
	}
	cases := map[string]string{
		"not json":        "{",
		"no files":        `{"roots": []}`,
		"files null":      `{"files": null}`,
		"files not array": `{"files": 3}`,
		"record no path":  `{"files": [{"duration": 3}]}`,
		"a list":          `[]`,
	}
	for name, body := range cases {
		p := filepath.Join(dir, strings.ReplaceAll(name, " ", "-")+".json")
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := Read(p); cli.Code(err) != cli.ExitUsage {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	for name, p := range map[string]string{"missing": filepath.Join(dir, "absent.json"), "a directory": dir, "a FIFO": fifo} {
		if _, err := Read(p); cli.Code(err) != cli.ExitUsage {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}
