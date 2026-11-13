package makemkv

import (
	"bufio"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

func logLines(t *testing.T, path ...string) []string {
	t.Helper()
	f, err := os.Open(filepath.Join(append([]string{"testdata"}, path...)...))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var lines []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		lines = append(lines, sc.Text())
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	return lines
}

func parse(lines ...string) *Parser {
	p := NewParser()
	for _, l := range lines {
		p.Feed(l)
	}
	return p
}

func parseLog(t *testing.T, path ...string) *Parser {
	t.Helper()
	return parse(logLines(t, path...)...)
}

func TestCopyTransition(t *testing.T) {
	t.Run("real pwp-d2 rip log", func(t *testing.T) {
		p := NewParser()
		var openings, analyzings int
		for _, line := range logLines(t, "pwp-d2", "rip.log") {
			wasCopying := p.Copying()
			p.Feed(line)
			switch {
			case strings.HasPrefix(line, "PRGC:") && strings.Contains(line, `"Saving to MKV file"`):
				if !p.Copying() {
					t.Fatalf("not copying after %q", line)
				}
				if !wasCopying && (openings == 0 || analyzings == 0) {
					t.Fatal("log never showed the opening and analysing steps before the first save")
				}
			case !wasCopying:
				if strings.HasPrefix(line, "PRGC:") && strings.Contains(line, "Opening") || strings.HasPrefix(line, "PRGT:") && strings.Contains(line, "Opening") {
					openings++
				}
				if strings.HasPrefix(line, "PRGC:") && strings.Contains(line, `"Analyzing seamless segments"`) {
					analyzings++
				}
				if p.Copying() {
					t.Fatalf("copying became true on %q", line)
				}
			}
		}
		if !p.Copying() || p.SavesStarted() != 8 {
			t.Errorf("Copying=%v SavesStarted=%d, want true and 8", p.Copying(), p.SavesStarted())
		}
	})

	t.Run("hand-fed sequence", func(t *testing.T) {
		p := NewParser()
		steps := []struct {
			line string
			want bool
		}{
			{`PRGT:3404,0,"Opening Blu-ray disc"`, false},
			{`PRGC:3400,0,"Processing AV clips"`, false},
			{`PRGC:5017,0,"Saving to MKV file"`, false}, // wrong PRGT: not a save
			{`PRGT:5024,0,"Saving all titles to MKV files"`, false},
			{`PRGC:5057,0,"Analyzing seamless segments"`, false},
			{`PRGC:3400,0,"Opening Blu-ray disc"`, false},
			{`PRGC:5017,0,"Saving to MKV file"`, true},
			{`PRGC:5057,1,"Analyzing seamless segments"`, true}, // never goes back
		}
		for _, s := range steps {
			p.Feed(s.line)
			if p.Copying() != s.want {
				t.Fatalf("after %q Copying = %v, want %v", s.line, p.Copying(), s.want)
			}
		}
		if p.SavesStarted() != 1 {
			t.Errorf("SavesStarted = %d, want 1", p.SavesStarted())
		}
	})
}

func TestParserOnRealDiscs(t *testing.T) {
	tests := []struct {
		dir       string
		titles    int
		saved     int
		disc      Disc
		firstName string
	}{
		{"pwp-d1", 11, 11, Disc{Name: "Parallel World Pharmacy D1", VolumeLabel: "ParallelWorldPharmacy_D1", Type: "Blu-ray disc"}, "Parallel World Pharmacy D1"},
		{"pwp-d2", 8, 8, Disc{Name: "Parallel World Pharmacy D2", VolumeLabel: "ParallelWorldPharmacy_D2", Type: "Blu-ray disc"}, "Parallel World Pharmacy D2"},
		{"revue-d3", 3, 3, Disc{Name: "Revue Starlight BD-3", VolumeLabel: "Revue Starlight BD-3", Type: "Blu-ray disc"}, ""},
		{"penguindrum-d2", 2, 2, Disc{Name: "PENGUINDRUM - Disc 2", VolumeLabel: "PENGUINDRUM_D2", Type: "Blu-ray disc"}, "PENGUINDRUM - Disc 2"},
	}
	const version = "MakeMKV v2.0.0 linux(x64-release)"
	for _, tc := range tests {
		t.Run(tc.dir, func(t *testing.T) {
			info := parseLog(t, tc.dir, "info.log")
			rip := parseLog(t, tc.dir, "rip.log")

			titles := info.Titles()
			if len(titles) != tc.titles {
				t.Fatalf("info table has %d titles, want %d", len(titles), tc.titles)
			}
			for i, ti := range titles {
				if ti.ID != i {
					t.Errorf("titles[%d].ID = %d: not sorted and dense", i, ti.ID)
				}
				if _, ok := ti.Seconds(); !ok {
					t.Errorf("title %d has unparseable length %q", ti.ID, ti.Duration)
				}
				if !strings.HasSuffix(ti.OutputName, fmt.Sprintf("_t%02d.mkv", ti.ID)) || ti.SourcePlaylist == "" || ti.SizeBytes == 0 {
					t.Errorf("title %d is incomplete: %+v", ti.ID, ti)
				}
			}
			if titles[0].Name != tc.firstName {
				t.Errorf("title 0 name = %q, want %q", titles[0].Name, tc.firstName)
			}
			if got := info.Disc(); got != tc.disc {
				t.Errorf("disc = %+v, want %+v", got, tc.disc)
			}
			if info.Version() != version || rip.Version() != version {
				t.Errorf("versions = %q, %q", info.Version(), rip.Version())
			}

			tally, ok := rip.Tally()
			if !ok || tally != (Tally{Saved: tc.saved}) {
				t.Errorf("tally = %+v, %v; want %d saved, 0 failed", tally, ok, tc.saved)
			}
			if len(rip.Errors()) != 0 || rip.Fatal() != "" || len(info.Errors()) != 0 || info.Fatal() != "" {
				t.Errorf("a clean disc reported errors: %q %q %q %q", rip.Errors(), rip.Fatal(), info.Errors(), info.Fatal())
			}
			if len(rip.Titles()) != 0 {
				t.Errorf("a rip log carries no TINFO, got %d titles", len(rip.Titles()))
			}
			if !rip.Copying() || rip.SavesStarted() != tc.saved {
				t.Errorf("Copying=%v SavesStarted=%d, want true and %d", rip.Copying(), rip.SavesStarted(), tc.saved)
			}
			if got := rip.Announced(); len(got) != tc.titles {
				t.Errorf("rip announced %d titles, want %d", len(got), tc.titles)
			}
			for id, playlist := range rip.Announced() {
				if got, _ := info.Title(id); got.SourcePlaylist != playlist {
					t.Errorf("title %d: rip says %s, info table says %s", id, playlist, got.SourcePlaylist)
				}
			}
		})
	}
}

func TestParserPwpD1Titles(t *testing.T) {
	info := parseLog(t, "pwp-d1", "info.log")

	playAll, ok := info.Title(8)
	if !ok {
		t.Fatal("no title 8")
	}
	if secs, ok := playAll.Seconds(); !ok || secs != 3*3600+9*60+36 {
		t.Errorf("title 8 Seconds = %d, %v; want the 3:09:36 Play-All", secs, ok)
	}
	if playAll.Chapters != 49 || playAll.SizeBytes != 44316112896 || playAll.SourcePlaylist != "00008.mpls" {
		t.Errorf("title 8 = %+v", playAll)
	}

	var mpls, m2ts []int
	for _, ti := range info.Titles() {
		switch {
		case strings.HasSuffix(ti.SourcePlaylist, ".mpls"):
			mpls = append(mpls, ti.ID)
		case strings.HasSuffix(ti.SourcePlaylist, ".m2ts"):
			m2ts = append(m2ts, ti.ID)
		}
	}
	if !reflect.DeepEqual(mpls, []int{0, 1, 2, 3, 4, 5, 6, 7, 8}) || !reflect.DeepEqual(m2ts, []int{9, 10}) {
		t.Errorf("source playlists: mpls %v, m2ts %v", mpls, m2ts)
	}
	if ti, _ := info.Title(9); ti.Chapters != 0 {
		t.Errorf("title 9 has no TINFO 8, Chapters = %d", ti.Chapters)
	}
	if _, ok := info.Title(11); ok {
		t.Error("title 11 does not exist")
	}
}

func TestParserOnSyntheticLogs(t *testing.T) {
	t.Run("scsi read error", func(t *testing.T) {
		p := parseLog(t, "synthetic", "scsi-read-error.log")
		errs := p.Errors()
		if len(errs) != 1 || !strings.Contains(errs[0], "Scsi error") {
			t.Errorf("Errors = %q", errs)
		}
		if p.Fatal() != "" {
			t.Errorf("Fatal = %q, a read error is not fatal", p.Fatal())
		}
	})
	t.Run("fatal 5021 keeps the first fatal text", func(t *testing.T) {
		p := parseLog(t, "synthetic", "fatal-5021.log")
		if !strings.HasPrefix(p.Fatal(), "This application version is too old") {
			t.Errorf("Fatal = %q", p.Fatal())
		}
		if _, ok := p.Tally(); ok {
			t.Error("a dead rip has no tally")
		}
	})
	t.Run("failed tally in the 5004 form", func(t *testing.T) {
		p := parseLog(t, "synthetic", "failed-tally-5004.log")
		if got, ok := p.Tally(); !ok || got != (Tally{Saved: 7, Failed: 1}) {
			t.Errorf("Tally = %+v, %v", got, ok)
		}
	})
	t.Run("fatal info pass 5010", func(t *testing.T) {
		p := parseLog(t, "synthetic", "info-fatal-5010.log")
		if p.Fatal() != "Failed to open disc" {
			t.Errorf("Fatal = %q", p.Fatal())
		}
		if len(p.Titles()) != 0 {
			t.Error("a failed info pass has no titles")
		}
	})
}

func TestTally(t *testing.T) {
	tests := []struct {
		name  string
		lines []string
		want  Tally
		ok    bool
	}{
		{"real 5005 then 5036", []string{
			`MSG:5005,128,1,"11 titles saved","%1 titles saved","11"`,
			`MSG:5036,260,1,"Copy complete. 11 titles saved.","Copy complete. %1 titles saved.","11"`,
		}, Tally{Saved: 11}, true},
		{"5004 form, read by format on its own code", []string{
			`MSG:5004,0,0,"Copy complete. 10 titles saved, 1 failed.","Copy complete. %1 titles saved, %2 failed.","10","1"`,
		}, Tally{Saved: 10, Failed: 1}, true},
		{"same format under an unrelated code", []string{
			`MSG:9999,0,0,"Done. 3 titles saved, 2 failed.","Done. %1 titles saved, %2 failed.","3","2"`,
		}, Tally{Saved: 3, Failed: 2}, true},
		{"parameters in a different order are followed by number", []string{
			`MSG:9999,0,0,"x","%2 failed and %1 titles saved","4","5"`,
		}, Tally{Saved: 4, Failed: 5}, true},
		{"a later saved-only line does not clear the failures", []string{
			`MSG:5004,0,0,"Copy complete. 10 titles saved, 1 failed.","Copy complete. %1 titles saved, %2 failed.","10","1"`,
			`MSG:5036,260,1,"Copy complete. 10 titles saved.","Copy complete. %1 titles saved.","10"`,
		}, Tally{Saved: 10, Failed: 1}, true},
		{"no tally at all", []string{`MSG:5014,131072,2,"Saving 2 titles into directory x","Saving %1 titles into directory %2","2","x"`}, Tally{}, false},
		{"missing parameter", []string{`MSG:5005,128,1,"? titles saved","%1 titles saved"`}, Tally{}, false},
		{"non-numeric parameter", []string{`MSG:5005,128,1,"x titles saved","%1 titles saved","x"`}, Tally{}, false},
		{"a failed count without a saved count is no tally", []string{`MSG:9999,0,0,"1 failed","%1 failed","1"`}, Tally{Failed: 1}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := parse(tc.lines...).Tally()
			if got != tc.want || ok != tc.ok {
				t.Errorf("Tally = %+v, %v; want %+v, %v", got, ok, tc.want, tc.ok)
			}
		})
	}
}

func TestParserMessages(t *testing.T) {
	t.Run("bad messages match on the format string", func(t *testing.T) {
		p := parse(
			`MSG:2003,16,3,"Error 'x' occurred while reading 'y' at offset 'z'","Error '%1' occurred while reading '%2' at offset '%3'","x","y","z"`,
			`MSG:2004,0,1,"fine text","Failed to save title %1","3"`,
			`MSG:5011,0,0,"Operation successfully completed","Operation successfully completed"`,
		)
		if len(p.Errors()) != 2 {
			t.Errorf("Errors = %q", p.Errors())
		}
	})
	t.Run("every fatal code, first wins", func(t *testing.T) {
		for _, code := range []string{"5009", "5010", "5020", "5021"} {
			p := parse(`MSG:`+code+`,0,0,"boom `+code+`","fmt"`, `MSG:5010,0,0,"later","fmt"`)
			if p.Fatal() != "boom "+code {
				t.Errorf("code %s: Fatal = %q", code, p.Fatal())
			}
		}
		if parse(`MSG:5011,0,0,"ok","ok"`).Fatal() != "" {
			t.Error("a harmless message set Fatal")
		}
	})
	t.Run("announcements", func(t *testing.T) {
		p := parse(`MSG:3307,0,2,"File 00009.mpls was added as title #0","File %1 was added as title #%2","00009.mpls","0"`,
			`MSG:3307,0,2,"bad","fmt","x.mpls","notanumber"`)
		if got := p.Announced(); !reflect.DeepEqual(got, map[int]string{0: "00009.mpls"}) {
			t.Errorf("Announced = %v", got)
		}
	})
}

func TestParserProgress(t *testing.T) {
	p := parse(`PRGT:5018,0,"Opening Blu-ray disc"`, `PRGC:3307,0,"Scanning contents"`, `PRGV:10,20,100`)
	if got := p.Progress(); got != (Progress{Phase: "Scanning contents", Current: 20, Max: 100, Percent: 20}) {
		t.Errorf("Progress = %+v", got)
	}
	p.Feed(`PRGT:5024,0,"Saving all titles to MKV files"`)
	if got := p.Progress().Phase; got != "Saving all titles to MKV files" {
		t.Errorf("a new PRGT clears the stale PRGC, Phase = %q", got)
	}
	p.Feed(`PRGV:50,33333,100000`)
	if got := p.Progress(); got.Percent != 33.3 || got.Current != 33333 {
		t.Errorf("Progress = %+v", got)
	}
	p.Feed(`PRGV:1,2,0`)
	if got := p.Progress(); got.Max != 0 || got.Percent != 33.3 {
		t.Errorf("a zero max keeps the last percent: %+v", got)
	}
}

func TestParserIgnoresMalformedLines(t *testing.T) {
	p := parse(
		"garbage line with no colon", "TINFO:oops", "TINFO:x,9,0,\"1:00:00\"", "MSG:notanumber,0,0,\"x\"",
		"PRGV:a,b,c", "PRGC:1", "PRGT:", "CINFO:2", "MSG:", "", "TCOUNT:4",
	)
	if len(p.Titles()) != 0 || p.Version() != "" || p.Fatal() != "" || p.Copying() || len(p.Errors()) != 0 {
		t.Error("malformed lines changed parser state")
	}
	p.Feed("TINFO:0,9,0,\"0:24:30\"\r\n")
	if ti, _ := p.Title(0); ti.Duration != "0:24:30" {
		t.Errorf("CRLF line not handled: %+v", ti)
	}
}

func TestTitleSeconds(t *testing.T) {
	tests := []struct {
		in   string
		want int
		ok   bool
	}{
		{"0:24:30", 1470, true},
		{"3:09:36", 11376, true},
		{"0:00:00", 0, true},
		{"", 0, false},
		{"24:30", 0, false},
		{"a:b:c", 0, false},
		{"0:-1:00", 0, false},
	}
	for _, tc := range tests {
		got, ok := Title{Duration: tc.in}.Seconds()
		if got != tc.want || ok != tc.ok {
			t.Errorf("Seconds(%q) = %d, %v; want %d, %v", tc.in, got, ok, tc.want, tc.ok)
		}
	}
}

func TestTestdataHoldsNoKey(t *testing.T) {
	keyPattern := regexp.MustCompile(`T-[A-Za-z0-9@_-]{16,}|app_Key`)
	n := 0
	err := filepath.WalkDir("testdata", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		n++
		if loc := keyPattern.Find(b); loc != nil {
			t.Errorf("%s contains a key-like string (%d bytes match)", path, len(loc))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if n < 13 {
		t.Errorf("scanned only %d files; the fixtures went missing", n)
	}
}
