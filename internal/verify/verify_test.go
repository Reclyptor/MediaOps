package verify

import (
	"bufio"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/reclyptor/mediaops/internal/makemkv"
)

func parse(lines ...string) *makemkv.Parser {
	p := makemkv.NewParser()
	for _, l := range lines {
		p.Feed(l)
	}
	return p
}

func parseLog(t *testing.T, path ...string) *makemkv.Parser {
	t.Helper()
	f, err := os.Open(filepath.Join(append([]string{"..", "makemkv", "testdata"}, path...)...))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	p := makemkv.NewParser()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		p.Feed(sc.Text())
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	return p
}

// Hand-written two-title disc: two 24-minute episodes and a 20 s extra.
var (
	fateInfo = []string{
		`MSG:1005,0,1,"MakeMKV v2.0.0 linux(x64-release) started","%1 started","MakeMKV v2.0.0 linux(x64-release)"`,
		`CINFO:2,0,"FATE_ZERO_D1"`, `CINFO:32,0,"FATE_ZERO_D1"`,
		`TINFO:0,2,0,"Fate/Zero"`, `TINFO:0,9,0,"0:24:30"`, `TINFO:0,11,0,"5000000000"`,
		`TINFO:0,16,0,"00800.mpls"`, `TINFO:0,27,0,"FATE_ZERO_D1_t00.mkv"`,
		`TINFO:1,9,0,"0:24:31"`, `TINFO:1,16,0,"00801.mpls"`, `TINFO:1,27,0,"FATE_ZERO_D1_t01.mkv"`,
		`TINFO:2,9,0,"0:00:20"`, `TINFO:2,16,0,"00802.mpls"`, `TINFO:2,27,0,"FATE_ZERO_D1_t02.mkv"`,
		`MSG:5011,0,0,"Operation successfully completed","Operation successfully completed"`,
	}
	fateRip = []string{
		`MSG:1005,0,1,"MakeMKV v2.0.0 linux(x64-release) started","%1 started","MakeMKV v2.0.0 linux(x64-release)"`,
		`MSG:3307,0,2,"File 00800.mpls was added as title #0","File %1 was added as title #%2","00800.mpls","0"`,
		`MSG:3307,0,2,"File 00801.mpls was added as title #1","File %1 was added as title #%2","00801.mpls","1"`,
		`PRGT:5024,0,"Saving all titles to MKV files"`, `PRGC:5017,0,"Saving to MKV file"`,
		`MSG:5005,128,1,"2 titles saved","%1 titles saved","2"`,
		`MSG:5036,260,1,"Copy complete. 2 titles saved.","Copy complete. %1 titles saved.","2"`,
	}
)

func fateFiles() map[string]FileInfo {
	return map[string]FileInfo{
		"FATE_ZERO_D1_t00.mkv": {Seconds: 1470.4},
		"FATE_ZERO_D1_t01.mkv": {Seconds: 1470.9},
	}
}

func fateInput() Input {
	return Input{Info: parse(fateInfo...), Rip: parse(fateRip...), Files: fateFiles(), MinTitleSeconds: 60}
}

func assertProblem(t *testing.T, got []string, substr string) {
	t.Helper()
	for _, g := range got {
		if strings.Contains(g, substr) {
			return
		}
	}
	t.Errorf("no problem contains %q; got %q", substr, got)
}

func TestCheckRip(t *testing.T) {
	t.Run("clean", func(t *testing.T) {
		if got := Check(fateInput()); len(got) != 0 {
			t.Errorf("clean rip has problems: %q", got)
		}
	})
	t.Run("exit 2", func(t *testing.T) {
		in := fateInput()
		in.RipExit = 2
		assertProblem(t, Check(in), "makemkvcon exited 2")
	})
	t.Run("missing file for a title", func(t *testing.T) {
		in := fateInput()
		in.Files = map[string]FileInfo{"FATE_ZERO_D1_t00.mkv": {Seconds: 1470}}
		assertProblem(t, Check(in), "title 1 (1471s) was reported but no file was written for it")
	})
	t.Run("a title under the minimum needs no file", func(t *testing.T) {
		in := fateInput()
		in.MinTitleSeconds = 20
		assertProblem(t, Check(in), "title 2 (20s) was reported but no file")
	})
	t.Run("wrong duration", func(t *testing.T) {
		in := fateInput()
		in.Files["FATE_ZERO_D1_t00.mkv"] = FileInfo{Seconds: 1200}
		assertProblem(t, Check(in), "FATE_ZERO_D1_t00.mkv: 1200.0s on disk vs 1470s reported")
	})
	t.Run("unreadable file", func(t *testing.T) {
		in := fateInput()
		in.Files["FATE_ZERO_D1_t00.mkv"] = FileInfo{Err: errors.New("moov atom not found")}
		assertProblem(t, Check(in), "ffprobe could not read it")
	})
	t.Run("no files", func(t *testing.T) {
		in := fateInput()
		in.Files = nil
		got := Check(in)
		assertProblem(t, got, "no .mkv files were produced")
		for _, g := range got {
			if strings.Contains(g, "no file was written") {
				t.Errorf("with no files the per-title complaints are redundant: %q", g)
			}
		}
	})
	t.Run("stray file", func(t *testing.T) {
		in := fateInput()
		in.Files["stray.mkv"] = FileInfo{Seconds: 5}
		assertProblem(t, Check(in), "stray.mkv: not a title makemkvcon reported, cannot verify its length")
	})
	t.Run("title without a length", func(t *testing.T) {
		in := fateInput()
		in.Info = parse(slices.DeleteFunc(slices.Clone(fateInfo), func(l string) bool { return strings.HasPrefix(l, "TINFO:0,9,") })...)
		assertProblem(t, Check(in), "gave no length for title 0")
	})
	t.Run("tolerance is the larger of 2% and 2 s", func(t *testing.T) {
		in := fateInput()
		in.Files["FATE_ZERO_D1_t00.mkv"] = FileInfo{Seconds: 1470 + 29} // under 2% of 1470 (29.4)
		if got := Check(in); len(got) != 0 {
			t.Errorf("within 2%% but flagged: %q", got)
		}
		in.Files["FATE_ZERO_D1_t00.mkv"] = FileInfo{Seconds: 1470 + 30}
		assertProblem(t, Check(in), "vs 1470s")

		short := slices.Clone(fateInfo)
		short = append(short, `TINFO:3,9,0,"0:01:05"`, `TINFO:3,16,0,"00803.mpls"`, `TINFO:3,27,0,"FATE_ZERO_D1_t03.mkv"`)
		rip := slices.Clone(fateRip)
		rip = slices.Insert(rip, 3, `MSG:3307,0,2,"File 00803.mpls was added as title #3","File %1 was added as title #%2","00803.mpls","3"`)
		rip = append(rip[:len(rip)-2], `MSG:5005,128,1,"3 titles saved","%1 titles saved","3"`)
		in = Input{Info: parse(short...), Rip: parse(rip...), Files: fateFiles(), MinTitleSeconds: 60}
		in.Files["FATE_ZERO_D1_t03.mkv"] = FileInfo{Seconds: 65 + 2}
		if got := Check(in); len(got) != 0 {
			t.Errorf("2 s off a 65 s title should pass: %q", got)
		}
		in.Files["FATE_ZERO_D1_t03.mkv"] = FileInfo{Seconds: 65 + 2.5}
		assertProblem(t, Check(in), "FATE_ZERO_D1_t03.mkv")
	})
	t.Run("fatal and read errors", func(t *testing.T) {
		in := fateInput()
		in.Rip = parse(append(slices.Clone(fateRip),
			`MSG:2003,16,3,"Error 'Scsi error' occurred while reading 'x' at offset '1'","Error '%1' occurred while reading '%2' at offset '%3'","Scsi error","x","1"`,
			`MSG:5021,131332,1,"This application version is too old.","fmt"`)...)
		got := Check(in)
		assertProblem(t, got, "fatal: This application version is too old.")
		assertProblem(t, got, "1 read/write error message(s) from makemkvcon")
	})
}

func TestCheckRealDisc(t *testing.T) {
	for _, dir := range []string{"pwp-d1", "pwp-d2", "revue-d3", "penguindrum-d2"} {
		t.Run(dir, func(t *testing.T) {
			info, rip := parseLog(t, dir, "info.log"), parseLog(t, dir, "rip.log")
			files := map[string]FileInfo{}
			for _, title := range info.Titles() {
				secs, _ := title.Seconds()
				files[title.OutputName] = FileInfo{Seconds: float64(secs) + 0.4}
			}
			in := Input{Info: info, Rip: rip, Files: files, MinTitleSeconds: 60}
			if got := Check(in); len(got) != 0 {
				t.Fatalf("real clean disc has problems: %q", got)
			}
			for name := range files {
				delete(files, name)
				break
			}
			if got := Check(in); len(got) == 0 {
				t.Error("a lost file went unnoticed")
			}
		})
	}
}

func replaceLine(lines []string, prefix, with string) []string {
	out := slices.Clone(lines)
	for i, l := range out {
		if strings.HasPrefix(l, prefix) {
			out[i] = with
			return out
		}
	}
	panic("no line starts with " + prefix)
}

func dropLines(lines []string, prefix string) []string {
	return slices.DeleteFunc(slices.Clone(lines), func(l string) bool { return strings.HasPrefix(l, prefix) })
}

func TestPlaylistAgreement(t *testing.T) {
	t.Run("agreeing passes agree", func(t *testing.T) {
		if got := Check(fateInput()); len(got) != 0 {
			t.Errorf("unexpected problems: %q", got)
		}
	})
	t.Run("mismatch", func(t *testing.T) {
		in := fateInput()
		in.Info = parse(replaceLine(fateInfo, "TINFO:1,16,", `TINFO:1,16,0,"00999.mpls"`)...)
		got := Check(in)
		assertProblem(t, got, "title 1 is 00801.mpls in the rip but 00999.mpls in the title info pass")
		if len(got) != 1 {
			t.Errorf("only the mismatch should be reported, got %q", got)
		}
	})
	t.Run("missing title", func(t *testing.T) {
		in := fateInput()
		in.Info = parse(dropLines(fateInfo, "TINFO:1,")...)
		assertProblem(t, Check(in), "title 1 is 00801.mpls in the rip but missing in the title info pass")
	})
	t.Run("title without a playlist counts as missing", func(t *testing.T) {
		in := fateInput()
		in.Info = parse(dropLines(fateInfo, "TINFO:1,16,")...)
		assertProblem(t, Check(in), "title 1 is 00801.mpls in the rip but missing")
	})
	t.Run("nothing announced", func(t *testing.T) {
		in := fateInput()
		in.Rip = parse(dropLines(fateRip, "MSG:3307,")...)
		assertProblem(t, Check(in), "the rip log announces no titles, so it cannot be matched to the title info")
	})
	t.Run("real pwp-d1 info against a different disc's rip", func(t *testing.T) {
		in := Input{Info: parseLog(t, "pwp-d2", "info.log"), Rip: parseLog(t, "pwp-d1", "rip.log"), MinTitleSeconds: 60}
		assertProblem(t, Check(in), "the disc may have changed between them")
	})
}

func TestTallyRequired(t *testing.T) {
	t.Run("exit 0 and no tally line fails", func(t *testing.T) {
		in := fateInput()
		in.Rip = parse(dropLines(fateRip, "MSG:50")...)
		assertProblem(t, Check(in), "no titles-saved tally")
	})
	t.Run("count differs from files", func(t *testing.T) {
		in := fateInput()
		in.Rip = parse(replaceLine(replaceLine(fateRip, "MSG:5005,", `MSG:5005,128,1,"3 titles saved","%1 titles saved","3"`),
			"MSG:5036,", `MSG:5036,260,1,"Copy complete. 3 titles saved.","Copy complete. %1 titles saved.","3"`)...)
		assertProblem(t, Check(in), "reports 3 title(s) saved but 2 .mkv file(s) are on disk")
	})
	t.Run("more files than the tally", func(t *testing.T) {
		in := fateInput()
		in.Files["FATE_ZERO_D1_t02.mkv"] = FileInfo{Seconds: 20}
		assertProblem(t, Check(in), "reports 2 title(s) saved but 3 .mkv file(s) are on disk")
	})
	t.Run("a non-mkv file is not counted", func(t *testing.T) {
		in := fateInput()
		in.Files["notes.txt"] = FileInfo{}
		if got := Check(in); slices.ContainsFunc(got, func(s string) bool { return strings.Contains(s, "on disk") }) {
			t.Errorf("notes.txt was counted: %q", got)
		}
	})
	t.Run("failed tally", func(t *testing.T) {
		in := fateInput()
		in.Rip = parse(append(dropLines(fateRip, "MSG:50"),
			`MSG:5004,0,0,"Copy complete. 2 titles saved, 1 failed.","Copy complete. %1 titles saved, %2 failed.","2","1"`)...)
		assertProblem(t, Check(in), "reports 1 title(s) failed to save")
	})
	t.Run("tally by format on a code nobody has seen", func(t *testing.T) {
		in := fateInput()
		in.Rip = parse(append(dropLines(fateRip, "MSG:50"),
			`MSG:7777,0,0,"Done. 2 titles saved, 1 failed.","Done. %1 titles saved, %2 failed.","2","1"`)...)
		assertProblem(t, Check(in), "failed to save")
	})
	t.Run("synthetic failed tally on the real disc it was framed on", func(t *testing.T) {
		info := parseLog(t, "pwp-d2", "info.log")
		files := map[string]FileInfo{}
		for _, title := range info.Titles() {
			secs, _ := title.Seconds()
			files[title.OutputName] = FileInfo{Seconds: float64(secs)}
		}
		delete(files, "Parallel World Pharmacy D2_t07.mkv")
		got := Check(Input{Info: info, Rip: parseLog(t, "synthetic", "failed-tally-5004.log"), Files: files, MinTitleSeconds: 60})
		assertProblem(t, got, "1 title(s) failed to save")
		assertProblem(t, got, "title 7 (92s) was reported but no file was written for it")
	})
}

func TestSyntheticRips(t *testing.T) {
	info := parseLog(t, "pwp-d2", "info.log")
	files := map[string]FileInfo{}
	for _, title := range info.Titles() {
		secs, _ := title.Seconds()
		files[title.OutputName] = FileInfo{Seconds: float64(secs)}
	}
	tests := []struct {
		log  string
		want string
	}{
		{"scsi-read-error.log", "1 read/write error message(s) from makemkvcon"},
		{"fatal-5021.log", "fatal: This application version is too old"},
	}
	for _, tc := range tests {
		t.Run(tc.log, func(t *testing.T) {
			in := Input{Info: info, Rip: parseLog(t, "synthetic", tc.log), Files: files, MinTitleSeconds: 60}
			assertProblem(t, Check(in), tc.want)
		})
	}
}

func TestInfoProblems(t *testing.T) {
	real := parseLog(t, "pwp-d1", "info.log")
	tests := []struct {
		name string
		info *makemkv.Parser
		exit int
		want string // "" means clean
	}{
		{"real clean pass", real, 0, ""},
		{"non-zero exit", real, 1, "the title info pass exited 1"},
		{"fatal 5010 from the synthetic log", parseLog(t, "synthetic", "info-fatal-5010.log"), 0, "the title info pass reported a fatal error: Failed to open disc"},
		{"read error", parse(append(slices.Clone(fateInfo),
			`MSG:2003,16,3,"Error 'x' occurred while reading 'y' at offset 'z'","Error '%1' occurred while reading '%2' at offset '%3'","x","y","z"`)...), 0,
			"1 read error message(s) from the title info pass"},
		{"empty table", parse(`MSG:5011,0,0,"Operation successfully completed","Operation successfully completed"`), 0, "the title info pass listed no titles"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := InfoProblems(tc.info, tc.exit)
			if tc.want == "" {
				if len(got) != 0 {
					t.Errorf("problems on a clean pass: %q", got)
				}
				return
			}
			assertProblem(t, got, tc.want)
		})
	}

	t.Run("the info gate carries into Check", func(t *testing.T) {
		in := fateInput()
		in.InfoExit = 1
		assertProblem(t, Check(in), "the title info pass exited 1")
		in = fateInput()
		in.Info = parse(`MSG:5010,0,0,"Failed to open disc","Failed to open disc"`)
		got := Check(in)
		assertProblem(t, got, "listed no titles")
		assertProblem(t, got, "fatal error: Failed to open disc")
	})
}

func TestTitleFor(t *testing.T) {
	info := parse(
		`TINFO:0,27,0,"Disc_t00.mkv"`, `TINFO:3,27,0,"Renamed by makemkv.mkv"`, `TINFO:5,9,0,"0:10:00"`,
	)
	tests := []struct {
		file   string
		wantID int
		ok     bool
	}{
		{"Disc_t00.mkv", 0, true},
		{"Renamed by makemkv.mkv", 3, true},
		{"Whatever_t05.mkv", 5, true},  // by suffix, not in TINFO 27
		{"Whatever_t09.mkv", 0, false}, // suffix names no title
		{"stray.mkv", 0, false},
		{"Disc_t99999999999999999999.mkv", 0, false},
	}
	for _, tc := range tests {
		got, ok := TitleFor(info, tc.file)
		if ok != tc.ok || (ok && got.ID != tc.wantID) {
			t.Errorf("TitleFor(%q) = %d, %v; want %d, %v", tc.file, got.ID, ok, tc.wantID, tc.ok)
		}
	}
}
