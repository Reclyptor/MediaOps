package manifest

import (
	"os"
	"strings"
	"testing"
)

func TestFixturesCheckClean(t *testing.T) {
	for _, name := range []string{"penguindrum", "films", "outlanders"} {
		t.Run(name, func(t *testing.T) {
			cfg, path := buildFixture(t, name, nil)
			m, findings, err := Load(path)
			if err != nil {
				t.Fatal(err)
			}
			rep, err := cfg.Check(m, findings, nil, CheckOptions{Films: true})
			if err != nil {
				t.Fatal(err)
			}
			if !rep.Clean {
				t.Fatalf("findings: %+v", rep.Findings)
			}
			if rep.Ops != len(m.Ops) || rep.Pending != rep.Ops || rep.Done != 0 {
				t.Fatalf("counts: %+v", rep)
			}
		})
	}
}

func TestFixtureShapes(t *testing.T) {
	want := map[string]map[string]int{
		"penguindrum": {"rename": 41, "flags": 24, "rmdir_if_empty": 6},
		"outlanders":  {"title": 1, "flags": 1, "move_dir": 1},
		"films": {"rename": 19, "title": 16, "remux": 8, "rmdir_if_empty": 5, "archive_dir": 3,
			"archive_file": 3, "rename_dir": 2, "move_dir": 1},
	}
	for name, kinds := range want {
		m, findings, err := Load("testdata/" + name + ".json")
		if err != nil || len(findings) != 0 {
			t.Fatalf("%s: %v %v", name, err, findings)
		}
		got := map[string]int{}
		for _, op := range m.Ops {
			got[op.Kind]++
		}
		for k, n := range kinds {
			if got[k] != n {
				t.Errorf("%s: %s = %d, want %d", name, k, got[k], n)
			}
		}
	}
}

func TestFixturesNameNoRealShare(t *testing.T) {
	for _, name := range []string{"penguindrum", "films", "outlanders"} {
		data, err := os.ReadFile("testdata/" + name + ".json")
		if err != nil {
			t.Fatal(err)
		}
		if got := string(data); strings.Contains(got, "dx"+"p") {
			t.Errorf("%s still names a real share", name)
		}
	}
}
