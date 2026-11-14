package manifest

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/reclyptor/mediaops/internal/cli"
)

var gnNow = time.Date(2026, 11, 13, 9, 30, 0, 0, time.UTC)

func gnWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func gnMkdir(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
}

// gnLoads marshals g, loads it back and fails on any finding.
func gnLoads(t *testing.T, g Generated) []byte {
	t.Helper()
	data, err := json.Marshal(g)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "gen.json")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	m, findings, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(findings) != 0 {
		t.Fatalf("findings: %+v", findings)
	}
	if len(m.Ops) != len(g.Ops) {
		t.Fatalf("loaded %d ops, generated %d", len(m.Ops), len(g.Ops))
	}
	return data
}

func gnUsage(t *testing.T, err error) {
	t.Helper()
	if err == nil || cli.Code(err) != cli.ExitUsage {
		t.Fatalf("err = %v (code %d), want a usage error", err, cli.Code(err))
	}
}

type gnOp struct{ kind, src, dst, path string }

func gnShape(dir string, g Generated) []gnOp {
	rel := func(p string) string {
		if p == "" {
			return ""
		}
		r, _ := filepath.Rel(dir, p)
		return r
	}
	var out []gnOp
	for _, op := range g.Ops {
		out = append(out, gnOp{op.Kind, rel(op.Src), rel(op.Dst), rel(op.Path)})
	}
	return out
}

func TestGenRenameSeriesPadsSeason(t *testing.T) {
	dir := t.TempDir()
	gnWrite(t, filepath.Join(dir, "Season 1", "Old - S01E01 - Cruelty.mkv"), "aaaa")
	gnWrite(t, filepath.Join(dir, "Season 1", "Old - S01E02 - Trainer.mkv"), "bb")
	gnWrite(t, filepath.Join(dir, "Season 1", "cover.jpg"), "c")
	gnWrite(t, filepath.Join(dir, "Season 1", "OP ED", "op.mkv"), "x")
	gnWrite(t, filepath.Join(dir, "Season 2", "Old - S02E01 - Again.mkv"), "dd")

	g, err := GenRenameSeries(dir, "Demon Slayer", nil, false, gnNow)
	if err != nil {
		t.Fatal(err)
	}
	want := []gnOp{
		{KindMoveDir, "Season 1/OP ED", "Season 01/OP ED", ""},
		{KindRename, "Season 1/Old - S01E01 - Cruelty.mkv", "Season 01/Demon Slayer - S01E01 - Cruelty.mkv", ""},
		{KindRename, "Season 1/Old - S01E02 - Trainer.mkv", "Season 01/Demon Slayer - S01E02 - Trainer.mkv", ""},
		{KindRename, "Season 1/cover.jpg", "Season 01/cover.jpg", ""},
		{KindRename, "Season 2/Old - S02E01 - Again.mkv", "Season 02/Demon Slayer - S02E01 - Again.mkv", ""},
		{KindRmdirIfEmpty, "", "", "Season 1"},
		{KindRmdirIfEmpty, "", "", "Season 2"},
	}
	if got := gnShape(dir, g); !reflect.DeepEqual(got, want) {
		t.Fatalf("ops:\n got %+v\nwant %+v", got, want)
	}
	if sz := g.Ops[1].Size; sz == nil || *sz != 4 {
		t.Fatalf("size of op 2 = %v, want 4", sz)
	}
	if g.Ops[0].Size != nil {
		t.Fatal("a move_dir carries no size")
	}
	if sz := g.Ops[3].Size; sz == nil || *sz != 1 {
		t.Fatalf("size of cover.jpg = %v, want 1", sz)
	}
	if g.Name != "Demon Slayer" || !strings.Contains(g.Note, "review before exec") {
		t.Fatalf("name/note = %q/%q", g.Name, g.Note)
	}
	if len(g.Generated.Unmatched) != 0 {
		t.Fatalf("unmatched = %v", g.Generated.Unmatched)
	}
	gnLoads(t, g)
}

func TestGenRenameSeriesPaddedFolderLeavesStrays(t *testing.T) {
	dir := t.TempDir()
	gnWrite(t, filepath.Join(dir, "Season 01", "Old - S01E01 - A.mkv"), "a")
	gnWrite(t, filepath.Join(dir, "Season 01", "notes.txt"), "n")
	gnMkdir(t, filepath.Join(dir, "Season 01", "extras"))

	g, err := GenRenameSeries(dir, "New", nil, false, gnNow)
	if err != nil {
		t.Fatal(err)
	}
	want := []gnOp{{KindRename, "Season 01/Old - S01E01 - A.mkv", "Season 01/New - S01E01 - A.mkv", ""}}
	if got := gnShape(dir, g); !reflect.DeepEqual(got, want) {
		t.Fatalf("ops = %+v", got)
	}
	wantUnmatched := []string{"Season 01/extras", "Season 01/notes.txt"}
	if !reflect.DeepEqual(g.Generated.Unmatched, wantUnmatched) {
		t.Fatalf("unmatched = %v, want %v", g.Generated.Unmatched, wantUnmatched)
	}
	gnLoads(t, g)
}

func TestGenRenameSeriesPaddingListsSpecialFiles(t *testing.T) {
	dir := t.TempDir()
	gnWrite(t, filepath.Join(dir, "Season 3", "Old - S03E01.mkv"), "a")
	if err := os.Symlink("Old - S03E01.mkv", filepath.Join(dir, "Season 3", "link.mkv")); err != nil {
		t.Skip(err)
	}
	g, err := GenRenameSeries(dir, "New", nil, false, gnNow)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"Season 3/link.mkv"}; !reflect.DeepEqual(g.Generated.Unmatched, want) {
		t.Fatalf("unmatched = %v", g.Generated.Unmatched)
	}
	if len(g.Ops) != 2 || g.Ops[1].Kind != KindRmdirIfEmpty {
		t.Fatalf("ops = %+v", g.Ops)
	}
}

func TestGenRenameSeriesOnly(t *testing.T) {
	dir := t.TempDir()
	for _, s := range []string{"Season 1", "Season 2", "Season 3"} {
		n := s[len(s)-1:]
		gnWrite(t, filepath.Join(dir, s, "Old - S0"+n+"E01.mkv"), "x")
	}
	g, err := GenRenameSeries(dir, "New", []int{3, 1}, false, gnNow)
	if err != nil {
		t.Fatal(err)
	}
	want := []gnOp{
		{KindRename, "Season 1/Old - S01E01.mkv", "Season 01/New - S01E01.mkv", ""},
		{KindRename, "Season 3/Old - S03E01.mkv", "Season 03/New - S03E01.mkv", ""},
		{KindRmdirIfEmpty, "", "", "Season 1"},
		{KindRmdirIfEmpty, "", "", "Season 3"},
	}
	if got := gnShape(dir, g); !reflect.DeepEqual(got, want) {
		t.Fatalf("ops = %+v", got)
	}
	if want := []string{dir, "New", "--only", "3,1"}; !reflect.DeepEqual(g.Generated.Args, want) {
		t.Fatalf("args = %v", g.Generated.Args)
	}
	gnLoads(t, g)
}

func TestGenRenameSeriesOnlyMatchingNothing(t *testing.T) {
	dir := t.TempDir()
	gnWrite(t, filepath.Join(dir, "Season 1", "Old - S01E01.mkv"), "x")
	_, err := GenRenameSeries(dir, "New", []int{9}, false, gnNow)
	gnUsage(t, err)
	if !strings.Contains(err.Error(), "no Season directories") {
		t.Fatalf("err = %v", err)
	}
}

func TestGenRenameSeriesStripDate(t *testing.T) {
	dir := t.TempDir()
	gnWrite(t, filepath.Join(dir, "Season 01", "Old - S01E01 - Title - [2017-01-12].mkv"), "x")
	gnWrite(t, filepath.Join(dir, "Season 01", "Old - S01E02 - Fire - Water.mkv"), "x")
	gnWrite(t, filepath.Join(dir, "Season 01", "Old - S01E03 - [2017-01-12].mkv"), "x")

	g, err := GenRenameSeries(dir, "New", nil, true, gnNow)
	if err != nil {
		t.Fatal(err)
	}
	var dsts []string
	for _, op := range g.Ops {
		dsts = append(dsts, filepath.Base(op.Dst))
	}
	want := []string{"New - S01E01 - Title.mkv", "New - S01E02 - Fire - Water.mkv", "New - S01E03.mkv"}
	if !reflect.DeepEqual(dsts, want) {
		t.Fatalf("dsts = %v", dsts)
	}
	if want := []string{dir, "New", "--strip-date"}; !reflect.DeepEqual(g.Generated.Args, want) {
		t.Fatalf("args = %v", g.Generated.Args)
	}

	kept, err := GenRenameSeries(dir, "New", nil, false, gnNow)
	if err != nil {
		t.Fatal(err)
	}
	if base := filepath.Base(kept.Ops[0].Dst); base != "New - S01E01 - Title - [2017-01-12].mkv" {
		t.Fatalf("without --strip-date: %s", base)
	}
	gnLoads(t, g)
}

func TestGenRenameSeriesFractionalAndOVA(t *testing.T) {
	dir := t.TempDir()
	gnWrite(t, filepath.Join(dir, "Season 01", "Old - S01E10.5 - Recap.mkv"), "x")
	gnWrite(t, filepath.Join(dir, "Season 01", "Old - S01E11 - Special (OVA).mkv"), "x")
	gnWrite(t, filepath.Join(dir, "Season 01", "Old - S01E12 (OVA).mkv"), "x")

	g, err := GenRenameSeries(dir, "New", nil, false, gnNow)
	if err != nil {
		t.Fatal(err)
	}
	var dsts []string
	for _, op := range g.Ops {
		dsts = append(dsts, filepath.Base(op.Dst))
	}
	want := []string{"New - S01E10.5 - Recap.mkv", "New - S01E11 - Special (OVA).mkv", "New - S01E12 (OVA).mkv"}
	if !reflect.DeepEqual(dsts, want) {
		t.Fatalf("dsts = %v", dsts)
	}
	gnLoads(t, g)
}

func TestGenRenameSeriesSkipsFileAlreadyNamed(t *testing.T) {
	dir := t.TempDir()
	gnWrite(t, filepath.Join(dir, "Season 01", "New - S01E01 - A.mkv"), "x")
	gnWrite(t, filepath.Join(dir, "Season 01", "Old - S01E02 - B.mkv"), "x")

	g, err := GenRenameSeries(dir, "New", nil, false, gnNow)
	if err != nil {
		t.Fatal(err)
	}
	if len(g.Ops) != 1 || filepath.Base(g.Ops[0].Src) != "Old - S01E02 - B.mkv" {
		t.Fatalf("ops = %+v", g.Ops)
	}
}

func TestGenRenameSeriesNothingToRename(t *testing.T) {
	dir := t.TempDir()
	gnWrite(t, filepath.Join(dir, "Season 01", "New - S01E01 - A.mkv"), "x")
	_, err := GenRenameSeries(dir, "New", nil, false, gnNow)
	gnUsage(t, err)
	if !strings.Contains(err.Error(), "nothing to rename") {
		t.Fatalf("err = %v", err)
	}
}

func TestGenRenameSeriesIgnoresSymlinkAndOtherDirs(t *testing.T) {
	dir := t.TempDir()
	gnWrite(t, filepath.Join(dir, "Season 1", "Old - S01E01.mkv"), "x")
	gnWrite(t, filepath.Join(dir, "Specials", "Old - S00E01.mkv"), "x")
	gnWrite(t, filepath.Join(dir, "Season 1 extras", "Old - S01E09.mkv"), "x")
	gnWrite(t, filepath.Join(dir, "elsewhere", "Old - S02E01.mkv"), "x")
	if err := os.Symlink(filepath.Join(dir, "elsewhere"), filepath.Join(dir, "Season 2")); err != nil {
		t.Skip(err)
	}
	g, err := GenRenameSeries(dir, "New", nil, false, gnNow)
	if err != nil {
		t.Fatal(err)
	}
	if len(g.Ops) != 2 {
		t.Fatalf("ops = %+v", gnShape(dir, g))
	}
	gnLoads(t, g)
}

func TestGenRenameSeriesOnlySymlinkSeason(t *testing.T) {
	dir := t.TempDir()
	gnWrite(t, filepath.Join(dir, "elsewhere", "Old - S02E01.mkv"), "x")
	if err := os.Symlink(filepath.Join(dir, "elsewhere"), filepath.Join(dir, "Season 2")); err != nil {
		t.Skip(err)
	}
	_, err := GenRenameSeries(dir, "New", nil, false, gnNow)
	gnUsage(t, err)
}

func TestGenRenameSeriesErrors(t *testing.T) {
	dir := t.TempDir()
	gnWrite(t, filepath.Join(dir, "Season 1", "notes.txt"), "x")
	empty := t.TempDir()
	file := filepath.Join(t.TempDir(), "file")
	gnWrite(t, file, "x")

	cases := []struct {
		name, dir, series, want string
	}{
		{"missing dir", filepath.Join(dir, "nope"), "N", "no such directory"},
		{"empty dir arg", "", "N", "no such directory"},
		{"a file", file, "N", "no such directory"},
		{"empty name", dir, "", "series name"},
		{"slash in name", dir, "a/b", "series name"},
		{"no Season dir", empty, "N", "no Season directories"},
		{"no episodes", dir, "N", "no episode files"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := GenRenameSeries(c.dir, c.series, nil, false, gnNow)
			gnUsage(t, err)
			if !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err = %v, want %q", err, c.want)
			}
		})
	}
}

func TestGenRenameSeriesDirSymlinkRefused(t *testing.T) {
	real := t.TempDir()
	gnWrite(t, filepath.Join(real, "Season 1", "Old - S01E01.mkv"), "x")
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(real, link); err != nil {
		t.Skip(err)
	}
	_, err := GenRenameSeries(link, "N", nil, false, gnNow)
	gnUsage(t, err)
}

func TestGenRenameSeriesDeterministic(t *testing.T) {
	dir := t.TempDir()
	for i := 1; i <= 3; i++ {
		gnWrite(t, filepath.Join(dir, "Season "+string(rune('0'+i)), "Old - S0"+string(rune('0'+i))+"E01 - A.mkv"), "x")
		gnWrite(t, filepath.Join(dir, "Season "+string(rune('0'+i)), "extra.txt"), "x")
	}
	var runs [2][]byte
	for i := range runs {
		g, err := GenRenameSeries(dir, "N", nil, true, gnNow)
		if err != nil {
			t.Fatal(err)
		}
		runs[i] = gnLoads(t, g)
	}
	if string(runs[0]) != string(runs[1]) {
		t.Fatalf("two runs differ:\n%s\n%s", runs[0], runs[1])
	}
}

func TestGenRenameSeriesInfo(t *testing.T) {
	dir := t.TempDir()
	gnWrite(t, filepath.Join(dir, "Season 1", "Old - S01E01.mkv"), "x")
	g, err := GenRenameSeries(dir, "N", nil, false, gnNow)
	if err != nil {
		t.Fatal(err)
	}
	if g.Generated.By != "mkv-manifest gen rename-series" || g.Generated.At != "2026-11-13T09:30:00Z" {
		t.Fatalf("info = %+v", g.Generated)
	}
	if want := []string{dir, "N"}; !reflect.DeepEqual(g.Generated.Args, want) {
		t.Fatalf("args = %v", g.Generated.Args)
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(gnLoads(t, g), &top); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"name", "note", "generated", "ops"} {
		if _, ok := top[key]; !ok {
			t.Fatalf("no %q key", key)
		}
	}
}

func gnArchiveTree(t *testing.T) (src string) {
	t.Helper()
	src = filepath.Join(t.TempDir(), "rip")
	gnWrite(t, filepath.Join(src, "a.mkv"), "aaa")
	gnWrite(t, filepath.Join(src, "disc1", "b.mkv"), "bb")
	gnWrite(t, filepath.Join(src, "disc1", "extras", "c.mkv"), "c")
	gnMkdir(t, filepath.Join(src, "empty"))
	return src
}

func TestGenArchiveTree(t *testing.T) {
	src := gnArchiveTree(t)
	dst := filepath.Join(t.TempDir(), "archive", "rip")

	g, err := GenArchiveTree(src, dst, gnNow)
	if err != nil {
		t.Fatal(err)
	}
	want := []gnOp{
		{KindArchiveFile, "a.mkv", "a.mkv", ""},
		{KindArchiveFile, "disc1/b.mkv", "disc1/b.mkv", ""},
		{KindArchiveFile, "disc1/extras/c.mkv", "disc1/extras/c.mkv", ""},
		{KindRmdirIfEmpty, "", "", "disc1/extras"},
		{KindRmdirIfEmpty, "", "", "disc1"},
		{KindRmdirIfEmpty, "", "", "empty"},
		{KindRmdirIfEmpty, "", "", "."},
	}
	got := make([]gnOp, len(g.Ops))
	for i, op := range g.Ops {
		got[i] = gnOp{op.Kind, "", "", ""}
		if op.Kind == KindArchiveFile {
			rs, _ := filepath.Rel(src, op.Src)
			rd, _ := filepath.Rel(dst, op.Dst)
			got[i].src, got[i].dst = rs, rd
		} else {
			got[i].path, _ = filepath.Rel(src, op.Path)
		}
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ops:\n got %+v\nwant %+v", got, want)
	}
	for i, size := range []int64{3, 2, 1} {
		if sz := g.Ops[i].Size; sz == nil || *sz != size {
			t.Fatalf("op %d size = %v, want %d", i+1, sz, size)
		}
	}
	if g.Name != "rip" || !strings.Contains(g.Note, "review before exec") {
		t.Fatalf("name/note = %q/%q", g.Name, g.Note)
	}
	if g.Ops[len(g.Ops)-1].Path != src {
		t.Fatalf("last op = %+v, want the source itself", g.Ops[len(g.Ops)-1])
	}
	gnLoads(t, g)
}

func TestGenArchiveTreeInfoAndDeterminism(t *testing.T) {
	src := gnArchiveTree(t)
	dst := t.TempDir()

	var runs [2][]byte
	var g Generated
	for i := range runs {
		var err error
		if g, err = GenArchiveTree(src, dst, gnNow); err != nil {
			t.Fatal(err)
		}
		runs[i] = gnLoads(t, g)
	}
	if string(runs[0]) != string(runs[1]) {
		t.Fatal("two runs differ")
	}
	if g.Generated.By != "mkv-manifest gen archive-tree" || g.Generated.At != "2026-11-13T09:30:00Z" {
		t.Fatalf("info = %+v", g.Generated)
	}
	if want := []string{src, dst}; !reflect.DeepEqual(g.Generated.Args, want) {
		t.Fatalf("args = %v", g.Generated.Args)
	}
	var top struct {
		Generated map[string]json.RawMessage `json:"generated"`
	}
	if err := json.Unmarshal(runs[0], &top); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"by", "args", "at"} {
		if _, ok := top.Generated[key]; !ok {
			t.Fatalf("generated has no %q", key)
		}
	}
}

func TestGenArchiveTreeSymlinkRefused(t *testing.T) {
	src := gnArchiveTree(t)
	if err := os.Symlink("a.mkv", filepath.Join(src, "disc1", "link")); err != nil {
		t.Skip(err)
	}
	_, err := GenArchiveTree(src, t.TempDir(), gnNow)
	gnUsage(t, err)
	if !strings.Contains(err.Error(), filepath.Join("disc1", "link")) {
		t.Fatalf("err = %v, want the link named", err)
	}
}

func TestGenArchiveTreeSpecialFileRefused(t *testing.T) {
	src := gnArchiveTree(t)
	fifo := filepath.Join(src, "pipe")
	if err := syscall.Mkfifo(fifo, 0o644); err != nil {
		t.Skip(err)
	}
	_, err := GenArchiveTree(src, t.TempDir(), gnNow)
	gnUsage(t, err)
	if !strings.Contains(err.Error(), fifo) {
		t.Fatalf("err = %v, want the fifo named", err)
	}
}

func TestGenArchiveTreeEmptySource(t *testing.T) {
	empty := t.TempDir()
	_, err := GenArchiveTree(empty, t.TempDir(), gnNow)
	gnUsage(t, err)
	if !strings.Contains(err.Error(), "source is empty") {
		t.Fatalf("err = %v", err)
	}

	gnMkdir(t, filepath.Join(empty, "a", "b"))
	_, err = GenArchiveTree(empty, t.TempDir(), gnNow)
	gnUsage(t, err)
	if !strings.Contains(err.Error(), "source is empty") {
		t.Fatalf("err = %v", err)
	}
}

func TestGenArchiveTreeDestinationInsideSource(t *testing.T) {
	src := gnArchiveTree(t)
	for _, dst := range []string{src, filepath.Join(src, "disc1"), filepath.Join(src, "new")} {
		_, err := GenArchiveTree(src, dst, gnNow)
		gnUsage(t, err)
	}
	if _, err := GenArchiveTree(src, src+"-archive", gnNow); err != nil {
		t.Fatalf("a sibling that shares a name prefix is outside: %v", err)
	}
}

func TestGenArchiveTreeSourceMustBeRealDirectory(t *testing.T) {
	real := gnArchiveTree(t)
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(real, link); err != nil {
		t.Skip(err)
	}
	file := filepath.Join(t.TempDir(), "file")
	gnWrite(t, file, "x")

	for name, src := range map[string]string{
		"symlink": link,
		"missing": filepath.Join(t.TempDir(), "nope"),
		"file":    file,
		"empty":   "",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := GenArchiveTree(src, t.TempDir(), gnNow)
			gnUsage(t, err)
		})
	}
}

func TestGenArchiveTreeEmptyDestination(t *testing.T) {
	_, err := GenArchiveTree(gnArchiveTree(t), "", gnNow)
	gnUsage(t, err)
}

func TestGenArchiveTreeDestinationMayExist(t *testing.T) {
	src := gnArchiveTree(t)
	dst := t.TempDir()
	gnWrite(t, filepath.Join(dst, "a.mkv"), "already")
	g, err := GenArchiveTree(src, dst, gnNow)
	if err != nil {
		t.Fatal(err)
	}
	gnLoads(t, g)
}
