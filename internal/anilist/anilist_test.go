package anilist

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reclyptor/mediaops/internal/cli"
)

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

type recorded struct {
	method, path string
	header       http.Header
	body         map[string]any
}

// serve answers every request with status and body and records what it saw.
func serve(t *testing.T, status int, body []byte) (Config, *recorded) {
	t.Helper()
	rec := &recorded{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.method, rec.path, rec.header = r.Method, r.URL.Path, r.Header.Clone()
		raw, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(raw, &rec.body); err != nil {
			t.Errorf("request body is not JSON: %v", err)
		}
		w.WriteHeader(status)
		w.Write(body)
	}))
	t.Cleanup(srv.Close)
	return Config{Endpoint: srv.URL + "/graphql", HTTPClient: srv.Client(), UserAgent: "mediaops/test (library normalisation)"}, rec
}

func vars(t *testing.T, rec *recorded) map[string]any {
	t.Helper()
	v, ok := rec.body["variables"].(map[string]any)
	if !ok {
		t.Fatalf("no variables in %v", rec.body)
	}
	return v
}

func TestSearchRequestAndEntry(t *testing.T) {
	cfg, rec := serve(t, 200, fixture(t, "search-penguindrum.json"))
	e, err := cfg.Search(context.Background(), "Penguindrum")
	if err != nil {
		t.Fatal(err)
	}
	if rec.method != http.MethodPost || rec.path != "/graphql" {
		t.Errorf("request was %s %s", rec.method, rec.path)
	}
	for k, want := range map[string]string{
		"Content-Type": "application/json",
		"Accept":       "application/json",
		"User-Agent":   "mediaops/test (library normalisation)",
	} {
		if got := rec.header.Get(k); got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
	v := vars(t, rec)
	if _, has := v["id"]; has || v["s"] != "Penguindrum" || len(v) != 1 {
		t.Errorf("search variables = %v, want only s", v)
	}
	if q, _ := rec.body["query"].(string); !strings.Contains(q, "streamingEpisodes") || !strings.Contains(q, "type: ANIME") {
		t.Errorf("query = %q", q)
	}
	if e.ID != 10721 || e.Note != Note || e.StreamingEpisodes == nil {
		t.Errorf("entry = %+v", e)
	}
}

func TestByIDRequestAndOrder(t *testing.T) {
	cfg, rec := serve(t, 200, fixture(t, "id-21355.json"))
	e, err := cfg.ByID(context.Background(), 21355)
	if err != nil {
		t.Fatal(err)
	}
	v := vars(t, rec)
	if _, has := v["s"]; has || v["id"] != float64(21355) || len(v) != 1 {
		t.Errorf("id variables = %v, want only id", v)
	}
	if e.ID != 21355 || e.Year == nil || *e.Year != 2016 || e.Episodes == nil || *e.Episodes != 25 {
		t.Errorf("entry = %+v", e)
	}
	// AniList's order is kept as it came, broadcast order or not.
	if got := *e.StreamingEpisodes[0]; !strings.HasPrefix(got, "Episode 66") {
		t.Errorf("first streaming episode = %q, want AniList's order kept", got)
	}
}

func TestNullFieldsStayNull(t *testing.T) {
	body := `{"data":{"Media":{"id":7,"title":{"romaji":"X","english":null,"native":null},"episodes":null,"format":"TV","startDate":{"year":null},"streamingEpisodes":[]}}}`
	cfg, _ := serve(t, 200, []byte(body))
	e, err := cfg.ByID(context.Background(), 7)
	if err != nil {
		t.Fatal(err)
	}
	out, _ := json.Marshal(e)
	want := `{"id":7,"title":{"romaji":"X","english":null,"native":null},"episodes":null,"format":"TV","year":null,"streaming_episodes":[],"note":"` + Note + `"}`
	if string(out) != want {
		t.Errorf("got  %s\nwant %s", out, want)
	}
}

func TestNotFound(t *testing.T) {
	cfg, _ := serve(t, 404, fixture(t, "notfound.json"))
	if _, err := cfg.ByID(context.Background(), 1); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestFailures(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		want   string
	}{
		{"forbidden", 403, "forbidden", "HTTP 403"},
		{"rate limited", 429, `{"errors":[{"message":"Too Many Requests.","status":429}]}`, "Too Many Requests."},
		{"rate limited without body", 429, "", "HTTP 429"},
		{"server error", 500, "oops", "HTTP 500"},
		{"not json", 200, "<html>maintenance</html>", "not valid JSON"},
		{"other graphql error", 200, `{"errors":[{"message":"Validation failed.","status":400}]}`, "Validation failed."},
		{"not found with another status", 200, `{"errors":[{"message":"Not Found.","status":500}]}`, "Not Found."},
		{"null media", 200, `{"data":{"Media":null}}`, "Media is null"},
		{"oversized", 200, strings.Repeat("x", maxBody+1), "larger than 8 MiB"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cfg, _ := serve(t, c.status, []byte(c.body))
			_, err := cfg.Search(context.Background(), "x")
			if err == nil || errors.Is(err, ErrNotFound) || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err = %v, want one containing %q", err, c.want)
			}
		})
	}
}

func TestDroppedConnection(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		conn.Close()
	}))
	defer srv.Close()
	cfg := Config{Endpoint: srv.URL, HTTPClient: srv.Client(), UserAgent: "t"}
	_, err := cfg.Search(context.Background(), "x")
	if err == nil || !strings.Contains(err.Error(), "could not reach AniList") {
		t.Fatalf("err = %v", err)
	}
}

func TestDefaultConfig(t *testing.T) {
	c := DefaultConfig()
	if c.Endpoint != "https://graphql.anilist.co" || c.HTTPClient.Timeout.Seconds() != 30 ||
		c.UserAgent != "mediaops/"+cli.Version+" (library normalisation)" {
		t.Errorf("DefaultConfig = %+v", c)
	}
}

// run drives the command as the process would and returns the exit code and
// both streams.
func run(cfg Config, args ...string) (int, string, string) {
	var out, errOut bytes.Buffer
	code := cli.Run(context.Background(), "mkv-anilist", args, cli.IO{Stdout: &out, Stderr: &errOut},
		func(ctx context.Context, args []string, streams cli.IO) error {
			return Command(ctx, cfg, args, streams)
		})
	return code, out.String(), errOut.String()
}

func TestCommandSearch(t *testing.T) {
	cfg, rec := serve(t, 200, fixture(t, "search-penguindrum.json"))
	code, out, errOut := run(cfg, "Penguindrum")
	if code != 0 || errOut != "" {
		t.Fatalf("code %d, stderr %q", code, errOut)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("stdout is not one JSON value: %v\n%s", err, out)
	}
	for _, k := range []string{"id", "title", "episodes", "format", "year", "streaming_episodes", "note"} {
		if _, ok := got[k]; !ok {
			t.Errorf("output lacks %q", k)
		}
	}
	if _, has := vars(t, rec)["id"]; has {
		t.Error("a search sent an id")
	}
}

func TestCommandID(t *testing.T) {
	cfg, rec := serve(t, 200, fixture(t, "id-21355.json"))
	for _, args := range [][]string{{"--id", "21355"}, {"--id=21355"}} {
		code, out, errOut := run(cfg, args...)
		if code != 0 || errOut != "" || !strings.Contains(out, `"id": 21355`) {
			t.Fatalf("%v: code %d, stderr %q, stdout %q", args, code, errOut, out)
		}
		if _, has := vars(t, rec)["s"]; has {
			t.Error("an --id lookup sent a search")
		}
	}
}

func TestCommandNotFoundNamesTheQuery(t *testing.T) {
	cfg, _ := serve(t, 404, fixture(t, "notfound.json"))
	code, out, errOut := run(cfg, "--id", "999999999")
	if code != cli.ExitUsage || out != "" || errOut != "mkv-anilist: FATAL: AniList has no series for id 999999999\n" {
		t.Fatalf("id: code %d, stdout %q, stderr %q", code, out, errOut)
	}
	code, out, errOut = run(cfg, "No Such Show")
	if code != cli.ExitUsage || out != "" || errOut != "mkv-anilist: FATAL: AniList has no series for search \"No Such Show\"\n" {
		t.Fatalf("search: code %d, stdout %q, stderr %q", code, out, errOut)
	}
}

func TestCommandUsage(t *testing.T) {
	cfg, rec := serve(t, 200, fixture(t, "search-penguindrum.json"))
	cases := [][]string{
		{},
		{"--id"},
		{"--id", "0"},
		{"--id", "-3"},
		{"--id", "abc"},
		{"--id", "1.5"},
		{"--id", ""},
		{"--id", "5", "Penguindrum"},
		{"A", "B"},
		{"   "},
		{""},
		{"--nope", "x"},
	}
	for _, args := range cases {
		code, out, errOut := run(cfg, args...)
		if code != cli.ExitUsage || out != "" || !strings.HasPrefix(errOut, "mkv-anilist: FATAL: ") {
			t.Errorf("%q: code %d, stdout %q, stderr %q", args, code, out, errOut)
		}
	}
	if rec.method != "" {
		t.Error("a usage error still reached the server")
	}
}

func TestCommandInternalFailures(t *testing.T) {
	for name, status := range map[string]int{"forbidden": 403, "rate limited": 429, "server error": 500} {
		t.Run(name, func(t *testing.T) {
			cfg, _ := serve(t, status, []byte("no"))
			code, out, errOut := run(cfg, "x")
			if code != cli.ExitInternal || out != "" || !strings.Contains(errOut, "mkv-anilist: FATAL: AniList answered HTTP") {
				t.Fatalf("code %d, stdout %q, stderr %q", code, out, errOut)
			}
		})
	}
	cfg, _ := serve(t, 200, []byte(`{"data":{"Media":null}}`))
	if code, _, errOut := run(cfg, "x"); code != cli.ExitInternal || !strings.Contains(errOut, "Media is null") {
		t.Fatalf("null media: code %d, stderr %q", code, errOut)
	}
}

func TestCommandVersion(t *testing.T) {
	code, out, _ := run(Config{}, "--version")
	if code != 0 || !strings.HasPrefix(out, "mkv-anilist ") {
		t.Fatalf("code %d, stdout %q", code, out)
	}
}
