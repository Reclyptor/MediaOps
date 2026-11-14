// Package anilist fetches a series' episode titles from AniList's GraphQL
// API. streamingEpisodes is the only place AniList exposes per-episode titles,
// and it is ordered but not numbered, so the result says its order is advisory.
package anilist

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/reclyptor/mediaops/internal/cli"
)

const (
	defaultEndpoint = "https://graphql.anilist.co"
	requestTimeout  = 30 * time.Second
	maxBody         = 8 << 20

	// Note is printed with every entry.
	Note = "streamingEpisodes is ordered but not numbered; treat its order as advisory"
)

// query is the Python tool's query. `id` must be omitted from the variables
// rather than sent as null: AniList reads an explicit null as "match id =
// null" and answers 404 even when the search term is good.
const query = `
query ($s: String, $id: Int) {
  Media(search: $s, id: $id, type: ANIME) {
    id
    title { romaji english native }
    episodes
    format
    startDate { year }
    streamingEpisodes { title }
  }
}
`

// ErrNotFound is AniList's answer when nothing matches the search or id.
var ErrNotFound = errors.New("AniList has no such series")

// Config is where and how to reach AniList.
type Config struct {
	Endpoint   string
	HTTPClient *http.Client
	// UserAgent is mandatory in practice: AniList answers 403 without one.
	UserAgent string
}

// DefaultConfig talks to the real AniList.
func DefaultConfig() Config {
	return Config{
		Endpoint:   defaultEndpoint,
		HTTPClient: &http.Client{Timeout: requestTimeout},
		UserAgent:  "mediaops/" + cli.Version + " (library normalisation)",
	}
}

// Title is a series' name in AniList's three scripts; any may be null.
type Title struct {
	Romaji  *string `json:"romaji"`
	English *string `json:"english"`
	Native  *string `json:"native"`
}

// Entry is the tool's output.
type Entry struct {
	ID                int       `json:"id"`
	Title             Title     `json:"title"`
	Episodes          *int      `json:"episodes"`
	Format            *string   `json:"format"`
	Year              *int      `json:"year"`
	StreamingEpisodes []*string `json:"streaming_episodes"`
	Note              string    `json:"note"`
}

type graphQLError struct {
	Message string `json:"message"`
	Status  int    `json:"status"`
}

type response struct {
	Data *struct {
		Media *struct {
			ID        int     `json:"id"`
			Title     Title   `json:"title"`
			Episodes  *int    `json:"episodes"`
			Format    *string `json:"format"`
			StartDate *struct {
				Year *int `json:"year"`
			} `json:"startDate"`
			StreamingEpisodes []struct {
				Title *string `json:"title"`
			} `json:"streamingEpisodes"`
		} `json:"Media"`
	} `json:"data"`
	Errors []graphQLError `json:"errors"`
}

// Search looks a series up by name.
func (c Config) Search(ctx context.Context, search string) (*Entry, error) {
	return c.fetch(ctx, map[string]any{"s": search})
}

// ByID looks a series up by its AniList id.
func (c Config) ByID(ctx context.Context, id int) (*Entry, error) {
	return c.fetch(ctx, map[string]any{"id": id})
}

func (c Config) fetch(ctx context.Context, variables map[string]any) (*Entry, error) {
	body, err := json.Marshal(map[string]any{"query": query, "variables": variables})
	if err != nil {
		return nil, fmt.Errorf("encoding the AniList request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.Endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("building the AniList request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", c.UserAgent)

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("could not reach AniList: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	if err != nil {
		return nil, fmt.Errorf("reading the AniList response failed: %w", err)
	}
	if len(raw) > maxBody {
		return nil, fmt.Errorf("the AniList response is larger than %d MiB", maxBody>>20)
	}

	// AniList's Not Found arrives as HTTP 404 with an errors body, so the body
	// is read whatever the status was.
	var r response
	parseErr := json.Unmarshal(raw, &r)
	for _, e := range r.Errors {
		if e.Message == "Not Found." && e.Status == http.StatusNotFound {
			return nil, ErrNotFound
		}
	}
	if len(r.Errors) > 0 {
		return nil, fmt.Errorf("AniList returned an error (HTTP %d): %s", resp.StatusCode, r.Errors[0].Message)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("AniList answered HTTP %d", resp.StatusCode)
	}
	if parseErr != nil {
		return nil, fmt.Errorf("the AniList response is not valid JSON: %w", parseErr)
	}
	if r.Data == nil || r.Data.Media == nil {
		return nil, errors.New("AniList returned no entry (Media is null)")
	}

	m := r.Data.Media
	e := &Entry{
		ID:                m.ID,
		Title:             m.Title,
		Episodes:          m.Episodes,
		Format:            m.Format,
		StreamingEpisodes: make([]*string, 0, len(m.StreamingEpisodes)),
		Note:              Note,
	}
	if m.StartDate != nil {
		e.Year = m.StartDate.Year
	}
	for _, s := range m.StreamingEpisodes {
		e.StreamingEpisodes = append(e.StreamingEpisodes, s.Title)
	}
	return e, nil
}
