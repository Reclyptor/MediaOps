package fingerprint

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func sampleRecord() Record {
	start := 1301.5
	return NewRecord("/m/t03.mkv", &start, []uint64{1, 2, 0xffffffffffffffff}, []uint64{0x2aac26bbaeb1442b})
}

func TestRecordRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "t03.json")
	want := sampleRecord()
	if err := Write(path, want); err != nil {
		t.Fatal(err)
	}
	got, err := Read(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.Path != want.Path || *got.PreviewStart != 1301.5 || strings.Join(got.Body, ",") != strings.Join(want.Body, ",") || got.Preview[0] != "2aac26bbaeb1442b" {
		t.Errorf("round trip = %+v", got)
	}
	raw, _ := os.ReadFile(path)
	if !strings.HasPrefix(string(raw), `{"tool":"mkv-fingerprint","schema":1,"path":"/m/t03.mkv","preview_start":1301.5,"body_fps":2,"preview_fps":4,"body":["0000000000000001",`) {
		t.Errorf("record = %s", raw)
	}
	if info, _ := os.Stat(path); info.Mode().Perm() != 0o660 {
		t.Errorf("mode = %v, want 0660", info.Mode().Perm())
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 1 {
		t.Errorf("directory holds %d entries after a write, want just the record", len(entries))
	}
}

func TestRecordWithoutPreviewStoresEmptyList(t *testing.T) {
	path := filepath.Join(t.TempDir(), "t05.json")
	if err := Write(path, NewRecord("/m/t05.mkv", nil, []uint64{7}, nil)); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(path)
	if !strings.Contains(string(raw), `"preview_start":null`) || !strings.Contains(string(raw), `"preview":[]`) {
		t.Errorf("record = %s", raw)
	}
	if _, err := Read(path); err != nil {
		t.Error(err)
	}
}

func TestReadRejects(t *testing.T) {
	good := func(edit func(m map[string]any)) string {
		raw, _ := json.Marshal(sampleRecord())
		var m map[string]any
		_ = json.Unmarshal(raw, &m)
		edit(m)
		out, _ := json.Marshal(m)
		return string(out)
	}
	cases := map[string]string{
		"python era":           `{"path": "/m/a.mkv", "preview_start": 1300.0, "body": [12345678901234567890], "preview": [1]}`,
		"other tool":           good(func(m map[string]any) { m["tool"] = "mkv-staticcheck" }),
		"no tool":              good(func(m map[string]any) { delete(m, "tool") }),
		"schema 2":             good(func(m map[string]any) { m["schema"] = 2 }),
		"schema as string":     good(func(m map[string]any) { m["schema"] = "1" }),
		"relative path":        good(func(m map[string]any) { m["path"] = "t03.mkv" }),
		"short hash":           good(func(m map[string]any) { m["body"] = []string{"abc"} }),
		"non-hex hash":         good(func(m map[string]any) { m["body"] = []string{"000000000000000g"} }),
		"signed hash":          good(func(m map[string]any) { m["body"] = []string{"+00000000000000a"} }),
		"empty body":           good(func(m map[string]any) { m["body"] = []string{} }),
		"null body":            good(func(m map[string]any) { m["body"] = nil }),
		"preview, no start":    good(func(m map[string]any) { m["preview_start"] = nil }),
		"start, no preview":    good(func(m map[string]any) { m["preview"] = []string{} }),
		"zero start":           good(func(m map[string]any) { m["preview_start"] = 0 }),
		"negative start":       good(func(m map[string]any) { m["preview_start"] = -3 }),
		"wrong body fps":       good(func(m map[string]any) { m["body_fps"] = 1 }),
		"unknown field":        good(func(m map[string]any) { m["extra"] = true }),
		"trailing data":        good(func(m map[string]any) {}) + `{}`,
		"not an object":        `[1, 2]`,
		"empty file":           ``,
		"truncated":            good(func(m map[string]any) {})[:40],
		"integer hash in body": good(func(m map[string]any) { m["body"] = []any{1} }),
	}
	dir := t.TempDir()
	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(dir, strings.ReplaceAll(name, " ", "_")+".json")
			if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
				t.Fatal(err)
			}
			_, err := Read(path)
			if !errors.Is(err, ErrInvalid) {
				t.Fatalf("err = %v, want ErrInvalid", err)
			}
			if !strings.Contains(err.Error(), path) {
				t.Errorf("the error does not name the file: %v", err)
			}
		})
	}
}

func TestReadNonRegular(t *testing.T) {
	dir := t.TempDir()
	valid := filepath.Join(dir, "valid.json")
	if err := Write(valid, sampleRecord()); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link.json")
	if err := os.Symlink(valid, link); err != nil {
		t.Fatal(err)
	}
	fifo := filepath.Join(dir, "fifo.json")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	subdir := filepath.Join(dir, "dir.json")
	if err := os.Mkdir(subdir, 0o755); err != nil {
		t.Fatal(err)
	}
	huge := filepath.Join(dir, "huge.json")
	f, err := os.Create(huge)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(maxRecordBytes + 1); err != nil {
		t.Fatal(err)
	}
	f.Close()

	for _, p := range []string{link, fifo, subdir, huge, filepath.Join(dir, "dangling.json")} {
		if p == filepath.Join(dir, "dangling.json") {
			if err := os.Symlink(filepath.Join(dir, "nowhere"), p); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := Read(p); !errors.Is(err, ErrInvalid) {
			t.Errorf("Read(%s) = %v, want ErrInvalid", filepath.Base(p), err)
		}
	}
	if _, err := Read(filepath.Join(dir, "absent.json")); err == nil || errors.Is(err, ErrInvalid) {
		t.Errorf("a missing file is an I/O error, got %v", err)
	}
}

func TestWriteRefusals(t *testing.T) {
	dir := t.TempDir()
	if err := Write(filepath.Join(dir, "x.mkv"), sampleRecord()); err == nil {
		t.Error("wrote a path not ending in .json")
	}
	sub := filepath.Join(dir, "d.json")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := Write(sub, sampleRecord()); err == nil {
		t.Error("replaced a directory")
	}
	target := filepath.Join(dir, "t.json")
	link := filepath.Join(dir, "l.json")
	if err := os.WriteFile(target, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if err := Write(link, sampleRecord()); err == nil {
		t.Error("wrote through a symlink")
	}
	if b, _ := os.ReadFile(target); string(b) != "keep" {
		t.Errorf("symlink target changed: %q", b)
	}

	empty := NewRecord("/m/a.mkv", nil, nil, nil)
	out := filepath.Join(dir, "empty.json")
	if err := Write(out, empty); err == nil {
		t.Error("wrote a record with no body frames")
	}
	if _, err := os.Lstat(out); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a refused record left %s behind", out)
	}
}

func TestWriteReplacesRegularFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "t.json")
	if err := os.WriteFile(path, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Write(path, sampleRecord()); err != nil {
		t.Fatal(err)
	}
	if _, err := Read(path); err != nil {
		t.Error(err)
	}
}

func TestParsePreviewStart(t *testing.T) {
	if got, err := ParsePreviewStart("none"); err != nil || got != nil {
		t.Errorf("none = %v, %v", got, err)
	}
	if got, err := ParsePreviewStart("1301.5"); err != nil || *got != 1301.5 {
		t.Errorf("1301.5 = %v, %v", got, err)
	}
	for _, bad := range []string{"", "0", "0.0", "-1", "-0.5", "abc", "NaN", "inf", "+Inf", "None", "1:30"} {
		if got, err := ParsePreviewStart(bad); err == nil {
			t.Errorf("ParsePreviewStart(%q) = %v, want an error", bad, *got)
		}
	}
}
