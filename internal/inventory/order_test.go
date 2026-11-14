package inventory

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reclyptor/mediaops/internal/cli"
)

type episode struct {
	duration float64
	chapters []float64
}

// threeEpisodes are 25, 24 and 26 minute episodes with 3, 2 and 3 chapters.
var threeEpisodes = []episode{
	{1500, []float64{0, 400, 900}},
	{1440, []float64{0, 700}},
	{1560, []float64{0, 300, 1000}},
}

// disc builds the records of one disc: the episode tracks in order, then a
// Play-All whose chapters are the episodes' laid end to end (the boundary marks
// included), and one short extra.
func disc(dir string, eps []episode) []Record {
	var recs []Record
	var all []float64
	cursor := 0.0
	for i, e := range eps {
		recs = append(recs, Record{
			Path: fmt.Sprintf("%s/_t%02d.mkv", dir, i+1), Duration: e.duration, Chapters: e.chapters,
		})
		for _, c := range e.chapters {
			all = append(all, cursor+c)
		}
		cursor += e.duration
	}
	recs = append(recs,
		Record{Path: dir + "/_t00.mkv", Duration: cursor, Chapters: all},
		Record{Path: dir + "/_t09.mkv", Duration: 90, Chapters: []float64{0}},
	)
	return recs
}

var opt = OrderOptions{MinEpisode: 15, MaxEpisode: 40}

func TestOrderConfirmsADiscThatReproducesItsPlayAll(t *testing.T) {
	inv := Inventory{Files: disc("/m/anime/Show/Disc 01", threeEpisodes)}
	rep := Order(inv, "", opt)
	if rep.Total != 1 || rep.Confirmed != 1 {
		t.Fatalf("report = %+v", rep)
	}
	d := rep.Discs[0]
	if d.Status != Confirmed || d.PlayAll != "/m/anime/Show/Disc 01/_t00.mkv" || len(d.Episodes) != 3 {
		t.Fatalf("disc = %+v", d)
	}
	if d.Detail != "3 episodes, 8/8 chapters consumed" {
		t.Errorf("detail = %q", d.Detail)
	}
	if d.EpisodeMinutesSum != 75 || d.PlayAllMinutes != 75 {
		t.Errorf("minutes = %v %v", d.EpisodeMinutesSum, d.PlayAllMinutes)
	}
}

func TestOrderToleratesThreeSecondsOfDrift(t *testing.T) {
	recs := disc("/m/Show/Disc 01", threeEpisodes)
	playall := &recs[3]
	playall.Chapters = append([]float64(nil), playall.Chapters...)
	playall.Chapters[4] += 2.9
	if rep := Order(Inventory{Files: recs}, "", opt); rep.Confirmed != 1 {
		t.Fatalf("2.9 s drift: %+v", rep.Discs[0])
	}
	playall.Chapters[4] += 0.2
	if rep := Order(Inventory{Files: recs}, "", opt); rep.Confirmed != 0 {
		t.Fatalf("3.1 s drift confirmed: %+v", rep.Discs[0])
	}
}

func TestOrderFlagsSwappedEpisodes(t *testing.T) {
	recs := disc("/m/Show/Disc 01", threeEpisodes)
	// the Play-All is authored from the order 1,2,3; the track files are named 2,1,3
	recs[0].Duration, recs[1].Duration = recs[1].Duration, recs[0].Duration
	recs[0].Chapters, recs[1].Chapters = recs[1].Chapters, recs[0].Chapters
	rep := Order(Inventory{Files: recs}, "", opt)
	d := rep.Discs[0]
	if d.Status != Mismatch || !strings.Contains(d.Detail, "episode 1 (_t01.mkv)") {
		t.Fatalf("disc = %+v", d)
	}
	if d.EpisodeMinutesSum != 75 {
		t.Errorf("sum = %v", d.EpisodeMinutesSum)
	}
}

func TestOrderBoundaryMismatch(t *testing.T) {
	// same chapter shapes, but episode 1 is 10 s longer than the Play-All says
	recs := disc("/m/Show/Disc 01", threeEpisodes)
	recs[0].Duration += 10
	d := Order(Inventory{Files: recs}, "", opt).Discs[0]
	if d.Status != Mismatch || !strings.Contains(d.Detail, "boundary after episode 1") {
		t.Fatalf("disc = %+v", d)
	}
}

func TestOrderRanOutOfChapters(t *testing.T) {
	recs := disc("/m/Show/Disc 01", threeEpisodes)
	recs[3].Chapters = recs[3].Chapters[:4]
	d := Order(Inventory{Files: recs}, "", opt).Discs[0]
	if d.Status != Mismatch || d.Detail != "ran out of Play-All chapters at episode 2" {
		t.Fatalf("disc = %+v", d)
	}

	// the boundary after the last chapter of an episode runs out too
	recs = disc("/m/Show/Disc 01", threeEpisodes)
	recs[3].Chapters = recs[3].Chapters[:3]
	d = Order(Inventory{Files: recs}, "", opt).Discs[0]
	if d.Status != Mismatch || !strings.Contains(d.Detail, "boundary after episode 1") || !strings.Contains(d.Detail, "no chapter left") {
		t.Fatalf("disc = %+v", d)
	}
}

func TestOrderNoPlayAll(t *testing.T) {
	cases := map[string][]Record{
		"nothing at least 40 minutes": {
			{Path: "/m/S/D/_t00.mkv", Duration: 39 * 60, Chapters: []float64{0}},
			{Path: "/m/S/D/_t01.mkv", Duration: 20 * 60, Chapters: []float64{0}},
		},
		"no episode-length tracks": {
			{Path: "/m/S/D/_t00.mkv", Duration: 90 * 60, Chapters: []float64{0}},
			{Path: "/m/S/D/_t01.mkv", Duration: 5 * 60, Chapters: []float64{0}},
		},
		"one lone track": {{Path: "/m/S/D/_t00.mkv", Duration: 90 * 60}},
	}
	for name, recs := range cases {
		rep := Order(Inventory{Files: recs}, "", opt)
		if d := rep.Discs[0]; d.Status != NoPlayAll || d.Episodes == nil || rep.Confirmed != 0 {
			t.Errorf("%s: %+v", name, d)
		}
	}
}

func TestOrderUnreadable(t *testing.T) {
	recs := append(disc("/m/Show/Disc 01", threeEpisodes), Record{Path: "/m/Show/Disc 01/_t05.mkv", Error: "ffprobe exited 1"})
	d := Order(Inventory{Files: recs}, "", opt).Discs[0]
	if d.Status != Unreadable || !strings.Contains(d.Detail, "_t05.mkv") {
		t.Fatalf("disc = %+v", d)
	}
}

func TestOrderEpisodeWindow(t *testing.T) {
	recs := disc("/m/Show/Disc 01", threeEpisodes)
	// a window that excludes the 24-minute episode leaves two, which no longer
	// reproduce the Play-All
	d := Order(Inventory{Files: recs}, "", OrderOptions{MinEpisode: 24.5, MaxEpisode: 40}).Discs[0]
	if d.Status != Mismatch || len(d.Episodes) != 2 {
		t.Fatalf("disc = %+v", d)
	}
}

func TestOrderGroupsByDirectoryAndFiltersByPath(t *testing.T) {
	var files []Record
	files = append(files, disc("/m/anime/Show/Disc 02", threeEpisodes)...)
	files = append(files, disc("/m/anime/Show/Disc 01", threeEpisodes)...)
	files = append(files, disc("/m/anime/Showcase/Disc 01", threeEpisodes)...)
	bad := disc("/m/anime/Other/Disc 01", threeEpisodes)
	bad[3].Chapters = bad[3].Chapters[:4]
	files = append(files, bad...)
	inv := Inventory{Files: files}

	all := Order(inv, "", opt)
	if all.Total != 4 || all.Confirmed != 3 || all.Discs[0].Dir != "/m/anime/Other/Disc 01" {
		t.Fatalf("all = %+v", all)
	}

	show := Order(inv, "/m/anime/Show", opt)
	if show.Total != 2 || show.Confirmed != 2 || show.Discs[0].Dir != "/m/anime/Show/Disc 01" || show.Dir != "/m/anime/Show" {
		t.Fatalf("Show = %+v", show)
	}
	// a path prefix is not a directory: Showcase is not under Show
	for _, d := range show.Discs {
		if strings.Contains(d.Dir, "Showcase") {
			t.Errorf("Show selected %s", d.Dir)
		}
	}
	if one := Order(inv, "/m/anime/Show/Disc 02", opt); one.Total != 1 {
		t.Errorf("a disc directory selects itself: %+v", one)
	}
	if none := Order(inv, "/m/anime/Absent", opt); none.Total != 0 {
		t.Errorf("Absent = %+v", none)
	}
}

// The Python selected the series by the third component of rel, which had come
// to name the category, so it matched nothing and printed nothing. Selection is
// by the record's own path now, and rel is not consulted at all.
func TestOrderSelectionDoesNotDependOnRel(t *testing.T) {
	cat := "dxp-share/anime/Show/Disc 01/_t00.mkv"
	recs := disc("/m/library/anime/Show/Disc 01", threeEpisodes)
	for i := range recs {
		rel := fmt.Sprintf("%s/%d", cat, i)
		recs[i].Rel = &rel
	}
	rep := Order(Inventory{Files: recs}, "/m/library/anime/Show", opt)
	if rep.Total != 1 || rep.Confirmed != 1 {
		t.Fatalf("report = %+v", rep)
	}
	recs2 := disc("/m/library/anime/Show/Disc 01", threeEpisodes) // rel null
	if rep := Order(Inventory{Files: recs2}, "/m/library/anime/Show", opt); rep.Confirmed != 1 {
		t.Fatalf("null rel: %+v", rep)
	}
}

func TestOrderCommand(t *testing.T) {
	dir := t.TempDir()
	show := filepath.Join(dir, "anime", "Show")
	var files []Record
	files = append(files, disc(filepath.Join(show, "Disc 01"), threeEpisodes)...)
	bad := disc(filepath.Join(show, "Disc 02"), threeEpisodes)
	bad[3].Chapters = bad[3].Chapters[:4]
	files = append(files, bad...)
	invPath := filepath.Join(dir, "inv.json")
	writeInventory(t, invPath, Inventory{Roots: []string{dir}, Probed: len(files), Files: files})

	run := func(args ...string) (int, string, string) {
		var out, errOut bytes.Buffer
		code := cli.Run(context.Background(), "mkv-order", args, cli.IO{Stdout: &out, Stderr: &errOut}, OrderCommand)
		return code, out.String(), errOut.String()
	}

	t.Run("all confirmed is exit 0, DIR before or after the flags", func(t *testing.T) {
		code, out, errOut := run(invPath, "--max-episode-minutes", "40", filepath.Join(show, "Disc 01"))
		if code != 0 || errOut != "" {
			t.Fatalf("code %d stderr %q out %s", code, errOut, out)
		}
		var rep OrderReport
		if err := json.Unmarshal([]byte(out), &rep); err != nil {
			t.Fatal(err)
		}
		if rep.Confirmed != 1 || rep.Total != 1 || rep.Inventory != invPath || rep.Dir != filepath.Join(show, "Disc 01") {
			t.Fatalf("report = %+v", rep)
		}
	})

	t.Run("a mismatch is exit 1 with the report", func(t *testing.T) {
		code, out, errOut := run(invPath)
		if code != 1 || errOut != "" {
			t.Fatalf("code %d stderr %q", code, errOut)
		}
		if !strings.Contains(out, `"confirmed": 1`) || !strings.Contains(out, `"total": 2`) || !strings.Contains(out, `"status": "mismatch"`) {
			t.Fatalf("out = %s", out)
		}
	})

	t.Run("usage", func(t *testing.T) {
		empty := filepath.Join(dir, "empty.json")
		writeInventory(t, empty, Inventory{})
		notInv := filepath.Join(dir, "other.json")
		if err := os.WriteFile(notInv, []byte(`{"discs": []}`), 0o644); err != nil {
			t.Fatal(err)
		}
		for name, args := range map[string][]string{
			"no inventory":      {},
			"too many args":     {invPath, show, show},
			"missing inventory": {filepath.Join(dir, "absent.json")},
			"not an inventory":  {notInv},
			"DIR matches none":  {invPath, filepath.Join(dir, "anime", "Nothing")},
			"no records":        {empty},
			"min over max":      {invPath, "--min-episode-minutes", "50"},
			"min zero":          {invPath, "--min-episode-minutes", "0"},
			"unknown flag":      {invPath, "--nope"},
		} {
			code, out, errOut := run(args...)
			if code != 2 || out != "" || !strings.HasPrefix(errOut, "mkv-order: FATAL: ") {
				t.Errorf("%s: code %d stdout %q stderr %q", name, code, out, errOut)
			}
		}
	})
}

func writeInventory(t *testing.T, path string, inv Inventory) {
	t.Helper()
	b, err := json.Marshal(inv)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
}
