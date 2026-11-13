package plan

import (
	"reflect"
	"strings"
	"testing"
)

const staging = "/stage"

const (
	anime  = `{"serial":"S","category":"anime","series":"Fate/Zero","season":1,"disc":2}`
	movie  = `{"serial":"M","category":"movies","title":"Akira (1988)"}`
	hentai = `{"serial":"H","category":"hentai","series":"X","season":null,"disc":1}`
	film   = `{"serial":"F","category":"anime","series":"Revue Starlight","season":null,"disc":3,` +
		`"container_title":"Revue Starlight: The Movie (2021)"}`
)

func discs(entries ...string) string { return `{"discs":[` + strings.Join(entries, ",") + `]}` }

func mustParse(t *testing.T, doc string) Plan {
	t.Helper()
	p, err := Parse([]byte(doc), staging)
	if err != nil {
		t.Fatalf("Parse(%s): %v", doc, err)
	}
	return p
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name    string
		doc     string
		wantErr string // substring; empty means the plan must be accepted
	}{
		{"series, movie, hentai and film", discs(anime, movie, hentai, film), ""},
		{"not an object", `[]`, "must be a JSON object"},
		{"null document", `null`, "must be a JSON object"},
		{"not JSON", `{"discs":`, "not valid JSON"},
		{"empty discs", `{"discs":[]}`, "plan.discs must be a non-empty list"},
		{"discs missing", `{}`, "plan.discs must be a non-empty list"},
		{"discs not a list", `{"discs":{}}`, "plan.discs must be a non-empty list"},
		{"same disc twice", discs(anime, anime), "discs[1]: serial S appears twice"},
		{"season null on an anime disc", discs(strings.Replace(anime, `"season":1`, `"season":null`, 1)), "discs[0]: season is required"},
		{"season missing on a tv disc", discs(`{"serial":"T","category":"tv","series":"X","disc":1}`), "discs[0]: season is required"},
		{"hentai season may be missing", discs(`{"serial":"H","category":"hentai","series":"X","disc":1}`), ""},
		{"container_title disc season may be null", discs(film), ""},
		{"movie with disc", discs(strings.Replace(movie, `"title"`, `"disc":1,"title"`, 1)), `["disc"] do not apply to movies`},
		{"movie with series", discs(strings.Replace(movie, `"title"`, `"series":"x","title"`, 1)), `["series"] do not apply to movies`},
		{"movie with null season", discs(strings.Replace(movie, `"title"`, `"season":null,"title"`, 1)), `["season"] do not apply to movies`},
		{"movie with container_title", discs(strings.Replace(movie, `"title"`, `"container_title":"x","title"`, 1)), "do not apply to movies"},
		{"series disc with a title", discs(strings.Replace(anime, `"series"`, `"title":"t","series"`, 1)), "title does not apply"},
		{"same destination", discs(anime, strings.Replace(anime, `"S"`, `"T"`, 1)), "discs[1]: destination /stage/anime/Fate - Zero/Season 01/Disc 02 is already claimed"},
		{"destinations collide after sanitising", discs(anime, strings.Replace(strings.Replace(anime, `"S"`, `"T"`, 1), "Fate/Zero", "Fate - Zero", 1)), "already claimed"},
		{"unknown top-level key", `{"discs":[` + anime + `],"typo":1}`, `unknown plan keys: ["typo"]`},
		{"unknown entry key", discs(strings.Replace(anime, `"disc"`, `"dsic":1,"disc"`, 1)), `discs[0]: unknown keys ["dsic"]`},
		{"entry not an object", `{"discs":[5]}`, "discs[0]: expected an object"},
		{"entry null", `{"discs":[null]}`, "discs[0]: expected an object"},
		{"serial missing", discs(`{"category":"movies","title":"x"}`), "discs[0]: serial is required"},
		{"serial empty", discs(`{"serial":"","category":"movies","title":"x"}`), "discs[0]: serial is required"},
		{"serial a number", discs(`{"serial":7,"category":"movies","title":"x"}`), "discs[0]: serial is required"},
		{"category unknown", discs(`{"serial":"S","category":"music","series":"x","disc":1}`), "discs[0]: category must be one of"},
		{"category missing", discs(`{"serial":"S","series":"x","disc":1}`), "discs[0]: category must be one of"},
		{"series missing", discs(`{"serial":"S","category":"tv","season":1,"disc":1}`), "discs[0]: series must be a non-empty string"},
		{"series blank", discs(strings.Replace(anime, "Fate/Zero", "  ", 1)), "series must be a non-empty string"},
		{"series is dot-dot", discs(strings.Replace(anime, "Fate/Zero", "..", 1)), "series must be a non-empty string"},
		{"series a number", discs(strings.Replace(anime, `"Fate/Zero"`, `5`, 1)), "series must be a non-empty string"},
		{"series with NUL", discs(strings.Replace(anime, "Fate/Zero", `a\u0000b`, 1)), "series contains a NUL"},
		{"title blank", discs(`{"serial":"M","category":"movies","title":" "}`), "title must be a non-empty string"},
		{"title missing", discs(`{"serial":"M","category":"movies"}`), "title must be a non-empty string"},
		{"disc missing", discs(`{"serial":"S","category":"tv","series":"x","season":1}`), "discs[0]: disc is required"},
		{"disc zero", discs(strings.Replace(anime, `"disc":2`, `"disc":0`, 1)), "disc must be an integer >= 1"},
		{"disc a string", discs(strings.Replace(anime, `"disc":2`, `"disc":"2"`, 1)), "disc must be an integer >= 1"},
		{"disc a bool", discs(strings.Replace(anime, `"disc":2`, `"disc":true`, 1)), "disc must be an integer >= 1"},
		{"disc a fraction", discs(strings.Replace(anime, `"disc":2`, `"disc":1.5`, 1)), "disc must be an integer >= 1"},
		{"disc null", discs(strings.Replace(anime, `"disc":2`, `"disc":null`, 1)), "disc must be an integer >= 1"},
		{"season zero is allowed", discs(strings.Replace(anime, `"season":1`, `"season":0`, 1)), ""},
		{"season negative", discs(strings.Replace(anime, `"season":1`, `"season":-1`, 1)), "season must be an integer >= 0"},
		{"season a string", discs(strings.Replace(anime, `"season":1`, `"season":"1"`, 1)), "season must be an integer >= 0"},
		{"container_title blank", discs(strings.Replace(film, "Revue Starlight: The Movie (2021)", " ", 1)), "container_title must be a non-empty string"},
		{"container_title a number", discs(strings.Replace(anime, `"disc"`, `"container_title":3,"disc"`, 1)), "container_title must be a non-empty string"},
		{"container_title null", discs(strings.Replace(anime, `"disc"`, `"container_title":null,"disc"`, 1)), "container_title must be a non-empty string"},
		{"max_concurrent zero", `{"discs":[` + anime + `],"max_concurrent":0}`, "max_concurrent must be an integer >= 1 and <= 16"},
		{"max_concurrent too high", `{"discs":[` + anime + `],"max_concurrent":17}`, "max_concurrent must be an integer >= 1 and <= 16"},
		{"max_concurrent a string", `{"discs":[` + anime + `],"max_concurrent":"3"}`, "max_concurrent must be an integer"},
		{"min_title_seconds zero", `{"discs":[` + anime + `],"min_title_seconds":0}`, "min_title_seconds must be an integer >= 1"},
		{"spinup gap zero is allowed", `{"discs":[` + anime + `],"spinup_gap_seconds":0}`, ""},
		{"spinup gap negative", `{"discs":[` + anime + `],"spinup_gap_seconds":-1}`, "spinup_gap_seconds must be an integer >= 0"},
		{"health pause zero", `{"discs":[` + anime + `],"health_pause_seconds":0}`, "health_pause_seconds must be an integer >= 1"},
		{"scan timeout a bool", `{"discs":[` + anime + `],"scan_timeout_seconds":false}`, "scan_timeout_seconds must be an integer >= 1"},
		{"scan timeout null", `{"discs":[` + anime + `],"scan_timeout_seconds":null}`, "scan_timeout_seconds must be an integer >= 1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse([]byte(tt.doc), staging)
			switch {
			case tt.wantErr == "" && err != nil:
				t.Fatalf("rejected: %v", err)
			case tt.wantErr != "" && err == nil:
				t.Fatalf("accepted, want error containing %q", tt.wantErr)
			case tt.wantErr != "" && !strings.Contains(err.Error(), tt.wantErr):
				t.Fatalf("error %q does not contain %q", err, tt.wantErr)
			}
		})
	}
}

func TestValidateNamesTheEntry(t *testing.T) {
	_, err := Parse([]byte(discs(anime, movie, `{"serial":"Z","category":"tv","series":"x","disc":1}`)), staging)
	if err == nil || !strings.HasPrefix(err.Error(), "discs[2]: ") {
		t.Fatalf("error = %v, want a discs[2]: prefix", err)
	}
}

func TestParseDefaults(t *testing.T) {
	p := mustParse(t, discs(anime))
	want := Plan{MinTitleSeconds: 60, MaxConcurrent: 3, SpinupGapSeconds: 30, HealthPauseSeconds: 120, ScanTimeoutSeconds: 900}
	got := p
	got.Discs = nil
	if !reflect.DeepEqual(got, want) {
		t.Errorf("defaults = %+v, want %+v", got, want)
	}
	if len(p.Discs) != 1 || p.Discs[0].Series != "Fate/Zero" || *p.Discs[0].Season != 1 || *p.Discs[0].Disc != 2 {
		t.Errorf("entry = %+v", p.Discs)
	}

	p = mustParse(t, `{"discs":[`+anime+`],"max_concurrent":5,"spinup_gap_seconds":0,"min_title_seconds":10}`)
	if p.MaxConcurrent != 5 || p.SpinupGapSeconds != 0 || p.MinTitleSeconds != 10 || p.HealthPauseSeconds != 120 {
		t.Errorf("overrides = %+v", p)
	}
}

func TestDestination(t *testing.T) {
	tests := []struct {
		name  string
		entry string
		want  string
	}{
		{"series slash becomes a dash", anime, "/stage/anime/Fate - Zero/Season 01/Disc 02"},
		{"hentai without a season", hentai, "/stage/hentai/X/Disc 01"},
		{"movie", movie, "/stage/movies/Akira (1988)"},
		{"film filed with its series", film, "/stage/anime/Revue Starlight/Disc 03"},
		{"season zero", strings.Replace(anime, `"season":1`, `"season":0`, 1), "/stage/anime/Fate - Zero/Season 00/Disc 02"},
		{"slash with spaces and a colon kept", strings.Replace(anime, "Fate/Zero", "Fate / Zero: Redux", 1), "/stage/anime/Fate - Zero: Redux/Season 01/Disc 02"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := mustParse(t, discs(tt.entry)).Discs[0]
			if got := e.Destination(staging); got != tt.want {
				t.Errorf("Destination = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestSegmentTitle(t *testing.T) {
	tests := []struct {
		name  string
		entry string
		want  string
	}{
		{"series keeps its slash", anime, "Fate/Zero"},
		{"hentai", hentai, "X"},
		{"movie", movie, "Akira (1988)"},
		{"film uses its container title", film, "Revue Starlight: The Movie (2021)"},
		{"container title on a seasoned disc", strings.Replace(anime, `"disc"`, `"container_title":"Fate/Zero: Special","disc"`, 1), "Fate/Zero: Special"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := mustParse(t, discs(tt.entry)).Discs[0]
			if got := e.SegmentTitle(); got != tt.want {
				t.Errorf("SegmentTitle = %q, want %q", got, tt.want)
			}
		})
	}
	// The directory is sanitised, the title is not.
	e := mustParse(t, discs(anime)).Discs[0]
	if strings.Contains(e.Destination(staging), "Fate/Zero") || e.SegmentTitle() != "Fate/Zero" {
		t.Errorf("destination %q, title %q", e.Destination(staging), e.SegmentTitle())
	}
}

func TestLabel(t *testing.T) {
	tests := []struct {
		entry string
		want  string
	}{
		{anime, "Fate/Zero S01 D02"},
		{hentai, "X D01"},
		{movie, "Akira (1988)"},
		{film, "Revue Starlight D03"},
	}
	for _, tt := range tests {
		e := mustParse(t, discs(tt.entry)).Discs[0]
		if got := e.Label(); got != tt.want {
			t.Errorf("Label = %q, want %q", got, tt.want)
		}
	}
}
