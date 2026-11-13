// Package plan validates a rip plan and derives from each entry where its
// files go and what its MKV segment title is. A typo caught here costs
// nothing; caught after a drive spins up it costs a disc's worth of time.
package plan

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

// Plan is a validated rip request with every default filled in.
type Plan struct {
	MinTitleSeconds    int     `json:"min_title_seconds"`
	MaxConcurrent      int     `json:"max_concurrent"`
	SpinupGapSeconds   int     `json:"spinup_gap_seconds"`
	HealthPauseSeconds int     `json:"health_pause_seconds"`
	ScanTimeoutSeconds int     `json:"scan_timeout_seconds"`
	Discs              []Entry `json:"discs"`
}

// Entry is one disc of a plan. Movies carry Title; series discs carry Series
// and Disc, and a Season unless they are hentai or a film filed with its series.
type Entry struct {
	Serial         string  `json:"serial"`
	Category       string  `json:"category"`
	Series         string  `json:"series,omitempty"`
	Season         *int    `json:"season,omitempty"`
	Disc           *int    `json:"disc,omitempty"`
	Title          string  `json:"title,omitempty"`
	ContainerTitle *string `json:"container_title,omitempty"`
}

const (
	categoryMovies = "movies"
	categoryHentai = "hentai"
)

var (
	seriesCategories = []string{"anime", "tv", categoryHentai}
	entryKeys        = []string{"serial", "category", "series", "season", "disc", "title", "container_title"}
	slashes          = regexp.MustCompile(`\s*/\s*`)
)

// setting is one numeric top-level key: its default and allowed range.
type setting struct {
	key    string
	dest   *int
	def    int
	lo, hi int // hi 0 means unbounded
}

// Parse validates data as a plan JSON document and returns it with defaults
// filled in. Every error names the offending entry as discs[i]. staging is the
// root the destinations are computed under, to catch two discs claiming one.
func Parse(data []byte, staging string) (Plan, error) {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(data, &top); err != nil {
		var typeErr *json.UnmarshalTypeError
		if errors.As(err, &typeErr) {
			return Plan{}, errors.New("plan must be a JSON object")
		}
		return Plan{}, fmt.Errorf("plan is not valid JSON: %w", err)
	}
	if top == nil {
		return Plan{}, errors.New("plan must be a JSON object")
	}

	var p Plan
	settings := []setting{
		{"min_title_seconds", &p.MinTitleSeconds, 60, 1, 0},
		{"max_concurrent", &p.MaxConcurrent, 3, 1, 16},
		{"spinup_gap_seconds", &p.SpinupGapSeconds, 30, 0, 0},
		{"health_pause_seconds", &p.HealthPauseSeconds, 120, 1, 0},
		{"scan_timeout_seconds", &p.ScanTimeoutSeconds, 900, 1, 0},
	}
	known := []string{"discs"}
	for _, s := range settings {
		known = append(known, s.key)
	}
	if unknown := unknownKeys(top, known); len(unknown) > 0 {
		return Plan{}, fmt.Errorf("unknown plan keys: %q", unknown)
	}
	for _, s := range settings {
		*s.dest = s.def
		if raw, ok := top[s.key]; ok {
			v, err := decodeInt(raw, s.key, s.lo, s.hi)
			if err != nil {
				return Plan{}, err
			}
			*s.dest = v
		}
	}

	var rawDiscs []json.RawMessage
	if err := json.Unmarshal(top["discs"], &rawDiscs); err != nil || len(rawDiscs) == 0 {
		return Plan{}, errors.New("plan.discs must be a non-empty list")
	}

	serials := map[string]bool{}
	dests := map[string]bool{}
	for i, raw := range rawDiscs {
		who := fmt.Sprintf("discs[%d]", i)
		e, err := parseEntry(raw)
		if err != nil {
			return Plan{}, fmt.Errorf("%s: %w", who, err)
		}
		if serials[e.Serial] {
			return Plan{}, fmt.Errorf("%s: serial %s appears twice", who, e.Serial)
		}
		serials[e.Serial] = true
		dest := e.Destination(staging)
		if dests[dest] {
			return Plan{}, fmt.Errorf("%s: destination %s is already claimed by an earlier disc", who, dest)
		}
		dests[dest] = true
		p.Discs = append(p.Discs, e)
	}
	return p, nil
}

func parseEntry(raw json.RawMessage) (Entry, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		return Entry{}, fmt.Errorf("expected an object with keys from %q", entryKeys)
	}
	if unknown := unknownKeys(fields, entryKeys); len(unknown) > 0 {
		return Entry{}, fmt.Errorf("unknown keys %q (allowed: %q)", unknown, entryKeys)
	}

	var e Entry
	var err error
	if e.Serial, err = decodeString(fields, "serial"); err != nil || e.Serial == "" {
		return Entry{}, errors.New("serial is required")
	}
	if e.Category, err = decodeString(fields, "category"); err != nil ||
		(e.Category != categoryMovies && !slices.Contains(seriesCategories, e.Category)) {
		return Entry{}, fmt.Errorf("category must be one of %q", append([]string{categoryMovies}, seriesCategories...))
	}

	if e.Category == categoryMovies {
		err = parseMovie(&e, fields)
	} else {
		err = parseSeriesDisc(&e, fields)
	}
	if err != nil {
		return Entry{}, err
	}
	return e, nil
}

func parseMovie(e *Entry, fields map[string]json.RawMessage) error {
	var extra []string
	for _, k := range []string{"series", "season", "disc", "container_title"} {
		if _, ok := fields[k]; ok {
			extra = append(extra, k)
		}
	}
	if len(extra) > 0 {
		return fmt.Errorf("%q do not apply to movies", extra)
	}
	title, err := decodeString(fields, "title")
	if err != nil {
		return err
	}
	if _, err = component(title, "title"); err != nil {
		return err
	}
	e.Title = title
	return nil
}

func parseSeriesDisc(e *Entry, fields map[string]json.RawMessage) error {
	if _, ok := fields["title"]; ok {
		return errors.New("title does not apply to series discs; use series")
	}
	var err error
	if e.Series, err = decodeString(fields, "series"); err != nil {
		return err
	}
	if _, err = component(e.Series, "series"); err != nil {
		return err
	}
	disc, err := requireInt(fields, "disc", 1)
	if err != nil {
		return err
	}
	e.Disc = &disc

	if raw, ok := fields["container_title"]; ok {
		var ct string
		if json.Unmarshal(raw, &ct) != nil || strings.TrimSpace(ct) == "" {
			return errors.New("container_title must be a non-empty string")
		}
		e.ContainerTitle = &ct
	}

	if raw, ok := fields["season"]; ok && !isNull(raw) {
		season, err := decodeInt(raw, "season", 0, 0)
		if err != nil {
			return err
		}
		e.Season = &season
		return nil
	}
	// A season-less disc is hentai, or a film filed with its series, which says
	// so by carrying its own container title. Anything else is a forgotten season.
	if e.Category != categoryHentai && e.ContainerTitle == nil {
		return errors.New("season is required (null only for hentai, or a film disc with container_title)")
	}
	return nil
}

// Destination is the folder the disc is ripped into under staging. Only '/'
// is illegal in a path component, so it is the only thing sanitised.
func (e Entry) Destination(staging string) string {
	if e.Category == categoryMovies {
		return filepath.Join(staging, categoryMovies, sanitize(e.Title))
	}
	parts := []string{staging, e.Category, sanitize(e.Series)}
	if e.Season != nil {
		parts = append(parts, fmt.Sprintf("Season %02d", *e.Season))
	}
	if e.Disc != nil {
		parts = append(parts, fmt.Sprintf("Disc %02d", *e.Disc))
	}
	return filepath.Join(parts...)
}

// SegmentTitle is the MKV segment title anime-normalize sets: the series name
// on every title of a series disc, the movie's own title for a movie, and the
// container title for a film that ships inside a series box set. Titles are
// never altered, so a series called "Fate/Zero" keeps its slash here.
func (e Entry) SegmentTitle() string {
	if e.Category == categoryMovies {
		return e.Title
	}
	if e.ContainerTitle != nil {
		return *e.ContainerTitle
	}
	return e.Series
}

// Label is the disc's human name in status output.
func (e Entry) Label() string {
	if e.Category == categoryMovies {
		return e.Title
	}
	label := e.Series
	if e.Season != nil {
		label += fmt.Sprintf(" S%02d", *e.Season)
	}
	if e.Disc != nil {
		label += fmt.Sprintf(" D%02d", *e.Disc)
	}
	return label
}

func sanitize(name string) string {
	return strings.TrimSpace(slashes.ReplaceAllString(name, " - "))
}

// component checks that value is usable as a single path component.
func component(value, what string) (string, error) {
	clean := sanitize(value)
	if clean == "" || clean == "." || clean == ".." {
		return "", fmt.Errorf("%s must be a non-empty string", what)
	}
	if strings.ContainsRune(value, 0) {
		return "", fmt.Errorf("%s contains a NUL", what)
	}
	return clean, nil
}

func unknownKeys(m map[string]json.RawMessage, known []string) []string {
	var unknown []string
	for k := range m {
		if !slices.Contains(known, k) {
			unknown = append(unknown, k)
		}
	}
	slices.Sort(unknown)
	return unknown
}

func isNull(raw json.RawMessage) bool { return bytes.Equal(bytes.TrimSpace(raw), []byte("null")) }

func decodeString(fields map[string]json.RawMessage, key string) (string, error) {
	var s string
	if err := json.Unmarshal(fields[key], &s); err != nil || isNull(fields[key]) {
		return "", fmt.Errorf("%s must be a non-empty string", key)
	}
	return s, nil
}

func requireInt(fields map[string]json.RawMessage, key string, lo int) (int, error) {
	raw, ok := fields[key]
	if !ok {
		return 0, fmt.Errorf("%s is required", key)
	}
	return decodeInt(raw, key, lo, 0)
}

// decodeInt reads a JSON integer in [lo, hi]; hi 0 means no upper bound.
// Strings, booleans, nulls and fractions are all refused.
func decodeInt(raw json.RawMessage, what string, lo, hi int) (int, error) {
	var v int
	if err := json.Unmarshal(raw, &v); err != nil || isNull(raw) || v < lo || (hi > 0 && v > hi) {
		msg := fmt.Sprintf("%s must be an integer >= %d", what, lo)
		if hi > 0 {
			msg += fmt.Sprintf(" and <= %d", hi)
		}
		return 0, errors.New(msg)
	}
	return v, nil
}
