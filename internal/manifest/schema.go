package manifest

import (
	"bytes"
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
)

// fieldSet is what an op kind takes. why and note are allowed on every kind.
type fieldSet struct {
	required, optional []string
}

var kindFields = map[string]fieldSet{
	KindRename:       {[]string{"src", "dst"}, []string{"title", "size"}},
	KindRenameDir:    {[]string{"src", "dst"}, nil},
	KindMoveDir:      {[]string{"src", "dst"}, nil},
	KindArchiveFile:  {[]string{"src", "dst"}, []string{"size"}},
	KindArchiveDir:   {[]string{"src", "dst"}, nil},
	KindRemux:        {[]string{"src", "dst", "archive_dst", "title"}, []string{"size"}},
	KindTitle:        {[]string{"path", "title"}, nil},
	KindFlags:        {[]string{"path", "audio_default", "sub_default"}, nil},
	KindAudioLang:    {[]string{"path", "langs"}, nil},
	KindRmdirIfEmpty: {[]string{"path"}, nil},
}

var langCode = regexp.MustCompile(`^[a-z]{3}$`)

// parseTop reads the optional top-level keys into m and reports any other key.
func parseTop(m *Manifest, top map[string]json.RawMessage) []Finding {
	var fs []Finding
	keys := sortedKeys(top)
	for _, k := range keys {
		raw := top[k]
		switch k {
		case "ops":
		case "name", "note":
			var s string
			if err := json.Unmarshal(raw, &s); err != nil {
				fs = append(fs, Finding{Problem: fmt.Sprintf("%q must be a string", k)})
				continue
			}
			if k == "name" {
				m.Name = s
			} else {
				m.Note = s
			}
		case "generated":
			var obj map[string]json.RawMessage
			if err := json.Unmarshal(raw, &obj); err != nil || obj == nil {
				fs = append(fs, Finding{Problem: `"generated" must be an object`})
			}
		default:
			fs = append(fs, Finding{Problem: fmt.Sprintf("unknown top-level key %q", k)})
		}
	}
	return fs
}

// parseOp reads op n. It fills in what it can and reports everything wrong
// with it; an op with findings is never replayed.
func parseOp(n int, raw json.RawMessage) (Op, []Finding) {
	op := Op{N: n}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		return op, []Finding{opFinding(n, "", "", "the op is not a JSON object")}
	}

	var fs []Finding
	bad := func(path, format string, args ...any) {
		fs = append(fs, opFinding(n, op.Kind, path, fmt.Sprintf(format, args...)))
	}

	if err := json.Unmarshal(fields["kind"], &op.Kind); err != nil {
		op.Kind = ""
	}
	spec, known := kindFields[op.Kind]
	if !known {
		return op, []Finding{opFinding(n, "", "", fmt.Sprintf("unknown or missing kind %q (ops are keyed on \"kind\")", op.Kind))}
	}

	allowed := append(slices.Concat(spec.required, spec.optional), "kind", "why", "note")
	for _, k := range sortedKeys(fields) {
		switch {
		case slices.Contains(allowed, k):
		case k == "names" && (op.Kind == KindFlags || op.Kind == KindAudioLang):
			bad("", `"names" is refused: track names are never changed`)
		default:
			bad("", "unknown field %q for kind %s", k, op.Kind)
		}
	}
	for _, k := range spec.required {
		if _, ok := fields[k]; !ok {
			bad("", "missing required field %q", k)
		}
	}

	path := func(field string, dst *string) {
		raw, ok := fields[field]
		if !ok {
			return
		}
		var s string
		if json.Unmarshal(raw, &s) != nil || s == "" {
			bad("", "%q must be a non-empty string", field)
			return
		}
		*dst = s
		switch {
		case !filepath.IsAbs(s):
			bad(s, "%s is not an absolute path", field)
		case filepath.Clean(s) != s:
			bad(s, "%s is not clean (want %s)", field, filepath.Clean(s))
		}
	}
	text := func(field string, dst *string) {
		raw, ok := fields[field]
		if !ok {
			return
		}
		if json.Unmarshal(raw, dst) != nil || (field == "title" && *dst == "") {
			*dst = ""
			bad("", "%q must be a non-empty string", field)
		}
	}
	path("src", &op.Src)
	path("dst", &op.Dst)
	path("archive_dst", &op.ArchiveDst)
	path("path", &op.Path)
	text("title", &op.Title)
	text("why", &op.Why)
	text("note", &op.Note)

	if raw, ok := fields["size"]; ok {
		if v, ok := parseInt(raw); ok && v >= 0 {
			op.Size = &v
		} else {
			bad("", `"size" must be a non-negative integer byte count`)
		}
	}

	switch op.Kind {
	case KindFlags:
		if raw, ok := fields["audio_default"]; ok {
			if v, ok := parseInt(raw); ok && v >= 0 {
				op.AudioDefault = int(v)
			} else {
				bad("", `"audio_default" must be a non-negative stream index`)
			}
		}
		if raw, ok := fields["sub_default"]; ok && !bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			if v, ok := parseInt(raw); ok && v >= 0 {
				idx := int(v)
				op.SubDefault = &idx
			} else {
				bad("", `"sub_default" must be a non-negative stream index or null`)
			}
		}
	case KindAudioLang:
		if raw, ok := fields["langs"]; ok {
			op.Langs = parseLangs(raw, func(problem string) { bad("", "%s", problem) })
		}
	}

	checkExtensions(op, bad)
	return op, fs
}

// checkExtensions holds the kinds that name a container to the extension the
// tool that edits it needs.
func checkExtensions(op Op, bad func(path, format string, args ...any)) {
	switch op.Kind {
	case KindTitle, KindFlags, KindAudioLang:
		if op.Path != "" && !isMatroska(op.Path) {
			bad(op.Path, "%s edits Matroska tags, so the path must end .mkv, .mka or .mks", op.Kind)
		}
	case KindRename:
		if op.Title != "" && op.Dst != "" && !isMatroska(op.Dst) {
			bad(op.Dst, "a rename that sets a title writes a Matroska tag, so dst must end .mkv, .mka or .mks")
		}
	case KindRemux:
		if op.Dst != "" && !strings.EqualFold(filepath.Ext(op.Dst), ".mkv") {
			bad(op.Dst, "a remux writes Matroska, so dst must end .mkv")
		}
	}
}

func isMatroska(p string) bool {
	switch strings.ToLower(filepath.Ext(p)) {
	case ".mkv", ".mka", ".mks":
		return true
	}
	return false
}

// parseLangs reads {"<abs index>": "<ISO 639-2>"}; every problem goes to bad.
func parseLangs(raw json.RawMessage, bad func(string)) map[int]string {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil || obj == nil {
		bad(`"langs" must be an object of stream index to ISO 639-2 code`)
		return nil
	}
	if len(obj) == 0 {
		bad(`"langs" must not be empty`)
		return nil
	}
	langs := map[int]string{}
	for _, k := range sortedKeys(obj) {
		idx, err := strconv.Atoi(k)
		if err != nil || idx < 0 || strconv.Itoa(idx) != k {
			bad(fmt.Sprintf("langs key %q is not a stream index", k))
			continue
		}
		var code string
		if json.Unmarshal(obj[k], &code) != nil || !langCode.MatchString(code) {
			bad(fmt.Sprintf("langs[%s] must be three lowercase letters (ISO 639-2), not %s", k, obj[k]))
			continue
		}
		langs[idx] = code
	}
	return langs
}

// parseInt reads a JSON integer. A float, an exponent or a string is not one.
func parseInt(raw json.RawMessage) (int64, bool) {
	v, err := strconv.ParseInt(string(bytes.TrimSpace(raw)), 10, 64)
	return v, err == nil
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// opJSON is the manifest spelling of an op, in the order the files write it.
type opJSON struct {
	Kind         string            `json:"kind"`
	Src          string            `json:"src,omitempty"`
	Dst          string            `json:"dst,omitempty"`
	ArchiveDst   string            `json:"archive_dst,omitempty"`
	Path         string            `json:"path,omitempty"`
	Title        string            `json:"title,omitempty"`
	Size         *int64            `json:"size,omitempty"`
	AudioDefault *int              `json:"audio_default,omitempty"`
	SubDefault   json.RawMessage   `json:"sub_default,omitempty"`
	Langs        map[string]string `json:"langs,omitempty"`
	Why          string            `json:"why,omitempty"`
	Note         string            `json:"note,omitempty"`
}

// MarshalJSON writes the op the way a manifest spells it, so a Generated
// manifest and a test's hand-built one both Load.
func (o Op) MarshalJSON() ([]byte, error) {
	j := opJSON{Kind: o.Kind, Src: o.Src, Dst: o.Dst, ArchiveDst: o.ArchiveDst, Path: o.Path,
		Title: o.Title, Size: o.Size, Why: o.Why, Note: o.Note}
	switch o.Kind {
	case KindFlags:
		audio := o.AudioDefault
		j.AudioDefault = &audio
		j.SubDefault = json.RawMessage("null")
		if o.SubDefault != nil {
			j.SubDefault = json.RawMessage(strconv.Itoa(*o.SubDefault))
		}
	case KindAudioLang:
		j.Langs = make(map[string]string, len(o.Langs))
		for idx, code := range o.Langs {
			j.Langs[strconv.Itoa(idx)] = code
		}
	}
	return json.Marshal(j)
}
