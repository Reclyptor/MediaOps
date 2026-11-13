package makemkv

import (
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Title is one row of the disc's title table, from TINFO lines. Only the
// `info` pass prints them; a rip log carries none.
type Title struct {
	ID             int
	Name           string // TINFO 2
	Chapters       int    // TINFO 8, 0 when unknown
	Duration       string // TINFO 9, "1:23:45"
	SizeBytes      int64  // TINFO 11, 0 when unknown
	SourcePlaylist string // TINFO 16
	OutputName     string // TINFO 27
}

// Seconds parses Duration. It reports false when the length is absent or not
// h:m:s.
func (t Title) Seconds() (int, bool) {
	parts := strings.Split(t.Duration, ":")
	if len(parts) != 3 {
		return 0, false
	}
	secs := 0
	for _, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return 0, false
		}
		secs = secs*60 + n
	}
	return secs, true
}

// Disc is what CINFO says about the disc.
type Disc struct {
	Name        string // CINFO 2
	VolumeLabel string // CINFO 32
	Type        string // CINFO 1
}

// Progress is the latest position makemkvcon reported.
type Progress struct {
	Phase   string // the current PRGC, else the current PRGT
	Current int    // PRGV field 1
	Max     int    // PRGV field 2
	Percent float64
}

// Tally is the saved/failed count makemkvcon prints at the end of a copy.
type Tally struct{ Saved, Failed int }

const (
	savePhasePrefix = "Saving"
	copyPhase       = "Saving to MKV file"
)

// fatalCodes end the whole run rather than one title: the licence, or the disc
// not opening.
var fatalCodes = map[int]bool{5009: true, 5010: true, 5020: true, 5021: true}

// badMessage matches MSG lines that mean data was not read or written
// faithfully. It is applied to the format string ("Error '%1' occurred while
// reading '%2' ..."), which is stable where the formatted text is not.
var badMessage = regexp.MustCompile(
	`(?i)occurred while|Scsi error|Failed to save title|corrupt or invalid|Hash check failed|Too many hash check errors`)

// The tally is read from the format string, not the code, because the codes
// have moved between releases while the English has not.
var (
	savedFormat  = regexp.MustCompile(`%(\d+) titles saved`)
	failedFormat = regexp.MustCompile(`%(\d+) failed`)
)

// Parser accumulates what one makemkvcon robot-mode run says about itself.
// It is not safe for concurrent use.
type Parser struct {
	version      string
	disc         Disc
	titles       map[int]*Title
	prgt, prgc   string
	progress     Progress
	copying      bool
	savesStarted int
	errors       []string
	fatal        string
	tally        Tally
	hasTally     bool
	announced    map[int]string
}

// NewParser returns an empty Parser.
func NewParser() *Parser {
	return &Parser{titles: map[int]*Title{}, announced: map[int]string{}}
}

// Feed consumes one output line. Lines it does not recognise or cannot read
// are ignored; the raw log keeps them.
func (p *Parser) Feed(line string) {
	kind, rest, ok := strings.Cut(strings.TrimRight(line, "\r\n"), ":")
	if !ok {
		return
	}
	f := SplitFields(rest)
	switch kind {
	case "MSG":
		p.msg(f, line)
	case "PRGT":
		p.prgtLine(f)
	case "PRGC":
		p.prgcLine(f)
	case "PRGV":
		p.prgvLine(f)
	case "TINFO":
		p.tinfo(f)
	case "CINFO":
		p.cinfo(f)
	}
}

func (p *Parser) prgtLine(f []string) {
	if len(f) < 3 {
		return
	}
	p.prgt, p.prgc = f[2], ""
}

func (p *Parser) prgcLine(f []string) {
	if len(f) < 3 {
		return
	}
	p.prgc = f[2]
	if strings.HasPrefix(p.prgt, savePhasePrefix) && p.prgc == copyPhase {
		p.copying = true
		p.savesStarted++
	}
}

func (p *Parser) prgvLine(f []string) {
	if len(f) < 3 {
		return
	}
	cur, err1 := strconv.Atoi(f[1])
	limit, err2 := strconv.Atoi(f[2])
	if err1 != nil || err2 != nil {
		return
	}
	p.progress.Current, p.progress.Max = cur, limit
	if limit > 0 {
		p.progress.Percent = math.Round(1000*float64(cur)/float64(limit)) / 10
	}
}

func (p *Parser) tinfo(f []string) {
	if len(f) < 4 {
		return
	}
	id, err1 := strconv.Atoi(f[0])
	attr, err2 := strconv.Atoi(f[1])
	if err1 != nil || err2 != nil {
		return
	}
	t := p.titles[id]
	if t == nil {
		t = &Title{ID: id}
		p.titles[id] = t
	}
	switch attr {
	case 2:
		t.Name = f[3]
	case 8:
		t.Chapters, _ = strconv.Atoi(f[3])
	case 9:
		t.Duration = f[3]
	case 11:
		t.SizeBytes, _ = strconv.ParseInt(f[3], 10, 64)
	case 16:
		t.SourcePlaylist = f[3]
	case 27:
		t.OutputName = f[3]
	}
}

func (p *Parser) cinfo(f []string) {
	if len(f) < 3 {
		return
	}
	switch f[0] {
	case "1":
		p.disc.Type = f[2]
	case "2":
		p.disc.Name = f[2]
	case "32":
		p.disc.VolumeLabel = f[2]
	}
}

// msg fields: code, flags, count, text, format, params...
func (p *Parser) msg(f []string, line string) {
	code, err := strconv.Atoi(f[0])
	if err != nil {
		return
	}
	text, format := field(f, 3), field(f, 4)
	if format == "" {
		format = text
	}
	switch code {
	case 1005:
		p.version = field(f, 5)
	case 3307:
		if id, err := strconv.Atoi(field(f, 6)); err == nil {
			p.announced[id] = field(f, 5)
		}
	}
	p.readTally(f, format)
	if fatalCodes[code] && p.fatal == "" {
		p.fatal = text
	}
	if badMessage.MatchString(format) || badMessage.MatchString(text) {
		p.errors = append(p.errors, strings.TrimRight(line, "\r\n"))
	}
}

// readTally takes "%N titles saved" and "%N failed" from a format string and
// the matching parameter, wherever they appear.
func (p *Parser) readTally(f []string, format string) {
	if m := savedFormat.FindStringSubmatch(format); m != nil {
		if n, ok := param(f, m[1]); ok {
			p.tally.Saved, p.hasTally = n, true
		}
	}
	if m := failedFormat.FindStringSubmatch(format); m != nil {
		if n, ok := param(f, m[1]); ok {
			p.tally.Failed = n
		}
	}
}

// param is the integer value of the 1-based parameter n of a split MSG line.
func param(f []string, n string) (int, bool) {
	i, err := strconv.Atoi(n)
	if err != nil || i < 1 {
		return 0, false
	}
	v, err := strconv.Atoi(field(f, 4+i))
	return v, err == nil
}

func field(f []string, i int) string {
	if i < len(f) {
		return f[i]
	}
	return ""
}

// Version is the banner's version, such as "MakeMKV v2.0.0 linux(x64-release)".
func (p *Parser) Version() string { return p.version }

// Disc is the disc's CINFO.
func (p *Parser) Disc() Disc { return p.disc }

// Titles lists the title table ordered by ID.
func (p *Parser) Titles() []Title {
	out := make([]Title, 0, len(p.titles))
	for _, t := range p.titles {
		out = append(out, *t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Title looks one title up by ID.
func (p *Parser) Title(id int) (Title, bool) {
	t, ok := p.titles[id]
	if !ok {
		return Title{}, false
	}
	return *t, true
}

// Progress is the latest PRGC/PRGT text and PRGV position.
func (p *Parser) Progress() Progress {
	pr := p.progress
	pr.Phase = p.prgc
	if pr.Phase == "" {
		pr.Phase = p.prgt
	}
	return pr
}

// Copying reports whether data has started flowing: a "Saving to MKV file"
// PRGC under a PRGT starting "Saving". "Analyzing seamless segments" and the
// "Opening ... disc" steps are still analysis, with the drive seeking, so they
// keep the scanning slot. It never goes back to false.
func (p *Parser) Copying() bool { return p.copying }

// SavesStarted counts the "Saving to MKV file" steps seen.
func (p *Parser) SavesStarted() int { return p.savesStarted }

// Errors holds the raw lines of every message that voids the rip.
func (p *Parser) Errors() []string { return append([]string(nil), p.errors...) }

// Fatal is the text of the first message that ends the run, or "".
func (p *Parser) Fatal() string { return p.fatal }

// Tally is the saved/failed count, false when no line ever carried one.
func (p *Parser) Tally() (Tally, bool) { return p.tally, p.hasTally }

// Announced maps each title id the rip announced (MSG 3307, "File 00009.mpls
// was added as title #0") to its source playlist.
func (p *Parser) Announced() map[int]string {
	out := make(map[int]string, len(p.announced))
	for id, pl := range p.announced {
		out[id] = pl
	}
	return out
}
