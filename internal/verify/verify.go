// Package verify decides whether a rip can be trusted. It fails closed: anything
// it cannot read or reconcile is a problem, and a rip passes only when the
// returned list is empty.
package verify

import (
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/reclyptor/mediaops/internal/makemkv"
)

const (
	durationTolerance    = 0.02
	durationFloorSeconds = 2.0
)

// FileInfo is what ffprobe made of one ripped file.
type FileInfo struct {
	Seconds float64
	Err     error // non-nil when ffprobe could not read the file
}

// Input is everything a verdict is drawn from. Info is the title-info pass and
// Rip the copy; both parsers are required.
type Input struct {
	Info            *makemkv.Parser
	InfoExit        int
	Rip             *makemkv.Parser
	RipExit         int
	Files           map[string]FileInfo // by file name
	MinTitleSeconds int
}

// InfoProblems is the gate on the title-info pass, run before the rip starts:
// a rip is never started against a table that cannot vouch for it.
func InfoProblems(info *makemkv.Parser, exit int) []string {
	var bad []string
	if exit != 0 {
		bad = append(bad, fmt.Sprintf("the title info pass exited %d", exit))
	}
	if fatal := info.Fatal(); fatal != "" {
		bad = append(bad, "the title info pass reported a fatal error: "+fatal)
	}
	if n := len(info.Errors()); n > 0 {
		bad = append(bad, fmt.Sprintf("%d read error message(s) from the title info pass", n))
	}
	if len(info.Titles()) == 0 {
		bad = append(bad, "the title info pass listed no titles")
	}
	return bad
}

// Check returns every reason the rip cannot be trusted. Empty means it can.
func Check(in Input) []string {
	bad := InfoProblems(in.Info, in.InfoExit)
	bad = append(bad, playlistProblems(in.Info, in.Rip)...)

	if in.RipExit != 0 {
		bad = append(bad, fmt.Sprintf("makemkvcon exited %d", in.RipExit))
	}
	if fatal := in.Rip.Fatal(); fatal != "" {
		bad = append(bad, "fatal: "+fatal)
	}
	if n := len(in.Rip.Errors()); n > 0 {
		bad = append(bad, fmt.Sprintf("%d read/write error message(s) from makemkvcon", n))
	}
	bad = append(bad, tallyProblems(in.Rip, in.Files)...)
	if len(in.Files) == 0 {
		bad = append(bad, "no .mkv files were produced")
	}
	return append(bad, fileProblems(in)...)
}

// playlistProblems cross-checks the rip against the title table. Title ids are
// positions in a list each makemkvcon invocation builds for itself, so if the
// two passes disagree about which playlist an id is, the rip would be verified
// against the wrong lengths.
func playlistProblems(info, rip *makemkv.Parser) []string {
	announced := rip.Announced()
	if len(announced) == 0 {
		return []string{"the rip log announces no titles, so it cannot be matched to the title info"}
	}
	ids := make([]int, 0, len(announced))
	for id := range announced {
		ids = append(ids, id)
	}
	sort.Ints(ids)
	var bad []string
	for _, id := range ids {
		listed := "missing"
		if t, ok := info.Title(id); ok && t.SourcePlaylist != "" {
			listed = t.SourcePlaylist
		}
		if listed != announced[id] {
			bad = append(bad, fmt.Sprintf(
				"title %d is %s in the rip but %s in the title info pass; the disc may have changed between them",
				id, announced[id], listed))
		}
	}
	return bad
}

// tallyProblems requires a "titles saved" line, because without one nothing
// says every title was written, and requires its count to match the files.
func tallyProblems(rip *makemkv.Parser, files map[string]FileInfo) []string {
	tally, ok := rip.Tally()
	if !ok {
		return []string{"makemkvcon's log has no titles-saved tally, so it is not known that every title was saved"}
	}
	var bad []string
	if tally.Failed > 0 {
		bad = append(bad, fmt.Sprintf("makemkvcon reports %d title(s) failed to save", tally.Failed))
	}
	if n := countMKV(files); tally.Saved != n {
		bad = append(bad, fmt.Sprintf("makemkvcon reports %d title(s) saved but %d .mkv file(s) are on disk", tally.Saved, n))
	}
	return bad
}

func countMKV(files map[string]FileInfo) int {
	n := 0
	for name := range files {
		if strings.HasSuffix(strings.ToLower(name), ".mkv") {
			n++
		}
	}
	return n
}

func fileProblems(in Input) []string {
	names := make([]string, 0, len(in.Files))
	for name := range in.Files {
		names = append(names, name)
	}
	sort.Strings(names)

	var bad []string
	matched := map[int]bool{}
	for _, name := range names {
		title, ok := TitleFor(in.Info, name)
		if !ok {
			bad = append(bad, name+": not a title makemkvcon reported, cannot verify its length")
			continue
		}
		matched[title.ID] = true
		want, haveLength := title.Seconds()
		fi := in.Files[name]
		switch {
		case fi.Err != nil:
			bad = append(bad, fmt.Sprintf("%s: ffprobe could not read it (%v)", name, fi.Err))
		case !haveLength:
			bad = append(bad, fmt.Sprintf("%s: makemkvcon gave no length for title %d", name, title.ID))
		case math.Abs(fi.Seconds-float64(want)) > math.Max(durationTolerance*float64(want), durationFloorSeconds):
			bad = append(bad, fmt.Sprintf("%s: %.1fs on disk vs %ds reported", name, fi.Seconds, want))
		}
	}
	if len(in.Files) == 0 {
		return bad
	}
	for _, title := range in.Info.Titles() {
		if want, ok := title.Seconds(); ok && want >= in.MinTitleSeconds && !matched[title.ID] {
			bad = append(bad, fmt.Sprintf("title %d (%ds) was reported but no file was written for it", title.ID, want))
		}
	}
	return bad
}

var titleSuffix = regexp.MustCompile(`_t(\d+)\.mkv$`)

// TitleFor finds the title a ripped file belongs to: by its reported output
// name (TINFO 27), else by the _tNN suffix MakeMKV puts on every file it names.
func TitleFor(info *makemkv.Parser, file string) (makemkv.Title, bool) {
	for _, t := range info.Titles() {
		if t.OutputName == file {
			return t, true
		}
	}
	m := titleSuffix.FindStringSubmatch(file)
	if m == nil {
		return makemkv.Title{}, false
	}
	id, err := strconv.Atoi(m[1])
	if err != nil {
		return makemkv.Title{}, false
	}
	return info.Title(id)
}
