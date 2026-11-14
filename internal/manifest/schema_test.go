package manifest

import (
	"os"
	"strings"
	"testing"

	"golang.org/x/sys/unix"

	"github.com/reclyptor/mediaops/internal/cli"
)

// loadOne loads a manifest holding the one op and returns its findings.
func loadOne(t *testing.T, op string) (*Manifest, []Finding) {
	t.Helper()
	r := newRig(t)
	m, findings, _ := r.manifestJSON("m.json", `{"ops": [`+op+`]}`)
	return m, findings
}

func wantFinding(t *testing.T, findings []Finding, substr string) {
	t.Helper()
	for _, f := range findings {
		if strings.Contains(f.Problem, substr) {
			return
		}
	}
	t.Fatalf("no finding mentions %q: %+v", substr, findings)
}

func TestSchemaValidOpsHaveNoFindings(t *testing.T) {
	ops := []string{
		`{"kind":"rename","src":"/a/x.mkv","dst":"/a/y.mkv"}`,
		`{"kind":"rename","src":"/a/x.mp4","dst":"/a/y.mkv","title":"T","size":0}`,
		`{"kind":"rename_dir","src":"/a/x","dst":"/a/y","why":"w","note":"n"}`,
		`{"kind":"move_dir","src":"/a/x","dst":"/a/y"}`,
		`{"kind":"archive_file","src":"/a/x","dst":"/b/x","size":12}`,
		`{"kind":"archive_dir","src":"/a/x","dst":"/b/x"}`,
		`{"kind":"remux","src":"/a/x.mp4","dst":"/a/x.mkv","archive_dst":"/b/x.mp4","title":"T","size":5}`,
		`{"kind":"title","path":"/a/x.mkv","title":"T"}`,
		`{"kind":"title","path":"/a/x.MKA","title":"T"}`,
		`{"kind":"flags","path":"/a/x.mkv","audio_default":1,"sub_default":3}`,
		`{"kind":"flags","path":"/a/x.mkv","audio_default":1,"sub_default":null}`,
		`{"kind":"audio_lang","path":"/a/x.mks","langs":{"1":"jpn","2":"eng"}}`,
		`{"kind":"rmdir_if_empty","path":"/a/x"}`,
	}
	for _, op := range ops {
		if _, findings := loadOne(t, op); len(findings) != 0 {
			t.Errorf("%s: %+v", op, findings)
		}
	}
}

func TestSchemaFieldRules(t *testing.T) {
	cases := []struct{ name, op, want string }{
		{"rename missing dst", `{"kind":"rename","src":"/a/x"}`, `missing required field "dst"`},
		{"rename missing src", `{"kind":"rename","dst":"/a/x"}`, `missing required field "src"`},
		{"rename_dir forbids title", `{"kind":"rename_dir","src":"/a","dst":"/b","title":"T"}`, `unknown field "title"`},
		{"archive_dir forbids size", `{"kind":"archive_dir","src":"/a","dst":"/b","size":1}`, `unknown field "size"`},
		{"archive_file forbids title", `{"kind":"archive_file","src":"/a","dst":"/b","title":"T"}`, `unknown field "title"`},
		{"remux needs archive_dst", `{"kind":"remux","src":"/a.mp4","dst":"/a.mkv","title":"T"}`, `"archive_dst"`},
		{"remux needs title", `{"kind":"remux","src":"/a.mp4","dst":"/a.mkv","archive_dst":"/b/a.mp4"}`, `"title"`},
		{"remux dst must be mkv", `{"kind":"remux","src":"/a.mp4","dst":"/a.mp4x","archive_dst":"/b/a","title":"T"}`, "must end .mkv"},
		{"title needs title", `{"kind":"title","path":"/a/x.mkv"}`, `missing required field "title"`},
		{"title empty", `{"kind":"title","path":"/a/x.mkv","title":""}`, "non-empty"},
		{"title path must be matroska", `{"kind":"title","path":"/a/x.mp4","title":"T"}`, ".mkv, .mka or .mks"},
		{"flags non-mkv", `{"kind":"flags","path":"/a/x.avi","audio_default":1,"sub_default":null}`, ".mkv, .mka or .mks"},
		{"flags sub_default absent", `{"kind":"flags","path":"/a/x.mkv","audio_default":1}`, `missing required field "sub_default"`},
		{"flags audio_default absent", `{"kind":"flags","path":"/a/x.mkv","sub_default":1}`, `missing required field "audio_default"`},
		{"flags negative", `{"kind":"flags","path":"/a/x.mkv","audio_default":-1,"sub_default":null}`, "audio_default"},
		{"flags float", `{"kind":"flags","path":"/a/x.mkv","audio_default":1.5,"sub_default":null}`, "audio_default"},
		{"flags string sub", `{"kind":"flags","path":"/a/x.mkv","audio_default":1,"sub_default":"3"}`, "sub_default"},
		{"flags names", `{"kind":"flags","path":"/a/x.mkv","audio_default":1,"sub_default":null,"names":{}}`, "track names are never changed"},
		{"audio_lang names", `{"kind":"audio_lang","path":"/a/x.mkv","langs":{"1":"jpn"},"names":{}}`, "track names are never changed"},
		{"langs empty", `{"kind":"audio_lang","path":"/a/x.mkv","langs":{}}`, "must not be empty"},
		{"langs not object", `{"kind":"audio_lang","path":"/a/x.mkv","langs":["jpn"]}`, "object"},
		{"langs bad key", `{"kind":"audio_lang","path":"/a/x.mkv","langs":{"a":"jpn"}}`, "not a stream index"},
		{"langs padded key", `{"kind":"audio_lang","path":"/a/x.mkv","langs":{"01":"jpn"}}`, "not a stream index"},
		{"langs upper", `{"kind":"audio_lang","path":"/a/x.mkv","langs":{"1":"JPN"}}`, "three lowercase letters"},
		{"langs two letters", `{"kind":"audio_lang","path":"/a/x.mkv","langs":{"1":"ja"}}`, "three lowercase letters"},
		{"langs non-string", `{"kind":"audio_lang","path":"/a/x.mkv","langs":{"1":5}}`, "three lowercase letters"},
		{"unknown key typo", `{"kind":"flags","path":"/a/x.mkv","audio_defualt":1,"audio_default":1,"sub_default":null}`, `unknown field "audio_defualt"`},
		{"extra is unknown", `{"kind":"rename","src":"/a","dst":"/b","extra":true}`, `unknown field "extra"`},
		{"after_dir_rename is unknown", `{"kind":"rename","src":"/a","dst":"/b","after_dir_rename":true}`, `unknown field "after_dir_rename"`},
		{"defer is unknown", `{"kind":"rename","src":"/a","dst":"/b","defer":true}`, `unknown field "defer"`},
		{"size negative", `{"kind":"rename","src":"/a","dst":"/b","size":-1}`, "non-negative"},
		{"size float", `{"kind":"rename","src":"/a","dst":"/b","size":1.5}`, "non-negative"},
		{"size string", `{"kind":"rename","src":"/a","dst":"/b","size":"7"}`, "non-negative"},
		{"why not a string", `{"kind":"rename","src":"/a","dst":"/b","why":3}`, `"why"`},
		{"path not a string", `{"kind":"rename","src":5,"dst":"/b"}`, "non-empty string"},
		{"relative src", `{"kind":"rename","src":"a/x","dst":"/b"}`, "not an absolute path"},
		{"unclean dot", `{"kind":"rename","src":"/a/./x","dst":"/b"}`, "not clean"},
		{"unclean dotdot", `{"kind":"rename","src":"/a/../x","dst":"/b"}`, "not clean"},
		{"unclean trailing slash", `{"kind":"rename_dir","src":"/a/x/","dst":"/b"}`, "not clean"},
		{"unclean double slash", `{"kind":"rename","src":"/a//x","dst":"/b"}`, "not clean"},
		{"unknown kind", `{"kind":"delete","path":"/a"}`, "unknown or missing kind"},
		{"missing kind", `{"op":"rename","src":"/a","dst":"/b"}`, "unknown or missing kind"},
		{"op not an object", `"rename"`, "not a JSON object"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, findings := loadOne(t, c.op)
			wantFinding(t, findings, c.want)
			for _, f := range findings {
				if f.Op == nil || *f.Op != 1 {
					t.Errorf("finding is not attached to op 1: %+v", f)
				}
			}
		})
	}
}

func TestSchemaSubDefaultNullVersusAbsent(t *testing.T) {
	m, findings := loadOne(t, `{"kind":"flags","path":"/a/x.mkv","audio_default":2,"sub_default":null}`)
	if len(findings) != 0 || m.Ops[0].SubDefault != nil || m.Ops[0].AudioDefault != 2 {
		t.Fatalf("null: %+v %+v", m.Ops[0], findings)
	}
	m, findings = loadOne(t, `{"kind":"flags","path":"/a/x.mkv","audio_default":2,"sub_default":4}`)
	if len(findings) != 0 || m.Ops[0].SubDefault == nil || *m.Ops[0].SubDefault != 4 {
		t.Fatalf("4: %+v %+v", m.Ops[0], findings)
	}
	_, findings = loadOne(t, `{"kind":"flags","path":"/a/x.mkv","audio_default":2}`)
	wantFinding(t, findings, "sub_default")
}

func TestSchemaOpFieldsAreParsed(t *testing.T) {
	m, findings := loadOne(t, `{"kind":"remux","src":"/a/x.mp4","dst":"/a/x.mkv","archive_dst":"/b/x.mp4","title":"T","size":9,"why":"w","note":"n"}`)
	if len(findings) != 0 {
		t.Fatal(findings)
	}
	op := m.Ops[0]
	if op.N != 1 || op.Kind != KindRemux || op.Src != "/a/x.mp4" || op.Dst != "/a/x.mkv" || op.ArchiveDst != "/b/x.mp4" ||
		op.Title != "T" || op.Size == nil || *op.Size != 9 || op.Why != "w" || op.Note != "n" {
		t.Fatalf("%+v", op)
	}
	m, _ = loadOne(t, `{"kind":"audio_lang","path":"/a/x.mkv","langs":{"3":"jpn","10":"eng"}}`)
	if got := m.Ops[0].Langs; len(got) != 2 || got[3] != "jpn" || got[10] != "eng" {
		t.Fatalf("%v", got)
	}
}

func TestSchemaEveryProblemReportedAtOnce(t *testing.T) {
	r := newRig(t)
	_, findings, _ := r.manifestJSON("m.json", `{"ops":[
	  {"kind":"rename","src":"a","dst":"/b"},
	  {"kind":"rename","src":"/a","dst":"/b"},
	  {"kind":"title","path":"/a.mp4","title":""},
	  {"kind":"nope"}],"colour":1,"name":3}`)
	var ops []int
	top := 0
	for _, f := range findings {
		if f.Op == nil {
			top++
		} else {
			ops = append(ops, *f.Op)
		}
	}
	if top != 2 {
		t.Errorf("top-level findings = %d, want 2 (colour, name): %+v", top, findings)
	}
	seen := map[int]bool{}
	for _, n := range ops {
		seen[n] = true
	}
	if !seen[1] || seen[2] || !seen[3] || !seen[4] {
		t.Errorf("ops with findings = %v", ops)
	}
}

func TestSchemaTopLevel(t *testing.T) {
	r := newRig(t)
	m, findings, _ := r.manifestJSON("m.json", `{"name":"n","note":"x","generated":{"by":"gen"},"ops":[]}`)
	if len(findings) != 0 || m.Name != "n" || m.Note != "x" || len(m.Ops) != 0 {
		t.Fatalf("%+v %+v", m, findings)
	}
	if len(m.SHA256) != 64 || m.Path != r.plans+"/m.json" {
		t.Fatalf("%q %q", m.SHA256, m.Path)
	}
	_, findings, _ = r.manifestJSON("bad.json", `{"ops":[],"generated":[1],"extra":1}`)
	wantFinding(t, findings, `"generated" must be an object`)
	wantFinding(t, findings, `unknown top-level key "extra"`)
	for _, f := range findings {
		if f.Op != nil {
			t.Errorf("whole-manifest finding carries an op: %+v", f)
		}
	}
}

func TestLoadFatalShapes(t *testing.T) {
	r := newRig(t)
	for name, body := range map[string]string{
		"empty file":    ``,
		"not json":      `{"ops": [`,
		"array":         `[]`,
		"string":        `"ops"`,
		"null":          `null`,
		"no ops":        `{"name":"x"}`,
		"ops object":    `{"ops":{}}`,
		"ops null":      `{"ops":null}`,
		"ops string":    `{"ops":"rename"}`,
		"trailing junk": `{"ops":[]} x`,
	} {
		path := putFile(t, r.plans+"/"+strings.ReplaceAll(name, " ", "-")+".json", body)
		if _, _, err := Load(path); cli.Code(err) != cli.ExitUsage {
			t.Errorf("%s: err = %v, code %d", name, err, cli.Code(err))
		}
	}
	if _, _, err := Load(r.plans + "/missing.json"); cli.Code(err) != cli.ExitUsage {
		t.Errorf("missing file: %v", err)
	}
	if _, _, err := Load(r.plans); cli.Code(err) != cli.ExitUsage {
		t.Errorf("directory: %v", err)
	}
	if _, _, err := Load("/dev/zero"); cli.Code(err) != cli.ExitUsage {
		t.Errorf("character device: %v", err)
	}
	fifo := r.plans + "/fifo.json"
	if err := unix.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Load(fifo); cli.Code(err) != cli.ExitUsage {
		t.Errorf("fifo: %v", err)
	}
}

func TestLoadFollowsSymlinkToManifest(t *testing.T) {
	r := newRig(t)
	real := putFile(t, r.plans+"/real.json", `{"ops":[]}`)
	link := r.plans + "/link.json"
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Load(link); err != nil {
		t.Fatalf("symlink to a manifest: %v", err)
	}
}

func TestOpMarshalRoundTrips(t *testing.T) {
	r := newRig(t)
	ops := []Op{
		{Kind: KindRename, Src: "/a/x.mkv", Dst: "/a/y.mkv", Title: "T", Size: i64(3), Why: "w"},
		{Kind: KindFlags, Path: "/a/y.mkv", AudioDefault: 1},
		{Kind: KindFlags, Path: "/a/y.mkv", AudioDefault: 1, SubDefault: intp(3)},
		{Kind: KindAudioLang, Path: "/a/y.mkv", Langs: map[int]string{2: "jpn"}},
		{Kind: KindRemux, Src: "/a/x.mp4", Dst: "/a/x.mkv", ArchiveDst: "/b/x.mp4", Title: "T"},
		{Kind: KindRmdirIfEmpty, Path: "/a"},
	}
	m, findings, _ := r.manifestOps(ops...)
	if len(findings) != 0 {
		t.Fatal(findings)
	}
	for i, op := range m.Ops {
		want := ops[i]
		want.N = i + 1
		if op.Kind != want.Kind || op.Src != want.Src || op.Path != want.Path || op.Title != want.Title ||
			op.AudioDefault != want.AudioDefault || (op.SubDefault == nil) != (want.SubDefault == nil) ||
			len(op.Langs) != len(want.Langs) || op.Why != want.Why {
			t.Errorf("op %d: %+v != %+v", i+1, op, want)
		}
	}
}

func TestJournalPathName(t *testing.T) {
	if got := JournalPath("/p/rename-manifest.json"); got != "/p/rename-manifest.journal.jsonl" {
		t.Errorf("got %s", got)
	}
	if got := JournalPath("/p/plan"); got != "/p/plan.journal.jsonl" {
		t.Errorf("got %s", got)
	}
}
