package cards

import (
	"encoding/json"
	"fmt"
	"io"
)

// FindParams are the thresholds of a find run, echoed in its report.
type FindParams struct {
	Lo     float64 `json:"lo"`
	Hi     float64 `json:"hi"`
	MinY   float64 `json:"miny"`
	MaxY   float64 `json:"maxy"`
	MinU   float64 `json:"minu"`
	MaxU   float64 `json:"maxu"`
	MinV   float64 `json:"minv"`
	MaxV   float64 `json:"maxv"`
	MinRun int     `json:"minrun"`
	Static bool    `json:"static"`
	Tol    float64 `json:"tol"`
}

// FindCandidate is one qualifying run of samples. Start and End are the times
// of its first and last sample, At the time of its middle one, and the three
// averages are the run's means.
type FindCandidate struct {
	Start  float64 `json:"start"`
	End    float64 `json:"end"`
	At     float64 `json:"at"`
	Frames int     `json:"frames"`
	YAvg   float64 `json:"yavg"`
	UAvg   float64 `json:"uavg"`
	VAvg   float64 `json:"vavg"`
}

// FindFile is one file's result. Best is the At of the most saturated run and
// is absent when there is no candidate; a file that could not be read carries
// only Path and Error.
type FindFile struct {
	Path       string          `json:"path"`
	Error      string          `json:"error,omitempty"`
	Window     [2]float64      `json:"window"`
	Samples    int             `json:"samples"`
	Candidates []FindCandidate `json:"candidates"`
	Best       *float64        `json:"best,omitempty"`
}

// FindReport is mkv-cards find's one JSON value.
type FindReport struct {
	Mode   string     `json:"mode"`
	Params FindParams `json:"params"`
	Files  []FindFile `json:"files"`
	Found  int        `json:"found"`
	Total  int        `json:"total"`
}

// MarshalJSON writes a failed file as {"path", "error"} and a read one in full.
func (f FindFile) MarshalJSON() ([]byte, error) {
	if f.Error != "" {
		return json.Marshal(struct {
			Path  string `json:"path"`
			Error string `json:"error"`
		}{f.Path, f.Error})
	}
	type plain FindFile
	return json.Marshal(plain(f))
}

// FreezeParams are the thresholds of a freeze run, echoed in its report.
type FreezeParams struct {
	Lo      float64 `json:"lo"`
	Hi      float64 `json:"hi"`
	DB      float64 `json:"db"`
	MinDur  float64 `json:"mindur"`
	MinV    float64 `json:"minv"`
	MaxV    float64 `json:"maxv"`
	MinU    float64 `json:"minu"`
	MaxU    float64 `json:"maxu"`
	MinYMax float64 `json:"minymax"`
}

// FreezeCandidate is a freeze whose colour fits. Start is in the file's
// timeline, At is the freeze's midpoint, where the colour was sampled.
type FreezeCandidate struct {
	Start    float64 `json:"start"`
	Duration float64 `json:"duration"`
	At       float64 `json:"at"`
	YMax     float64 `json:"ymax"`
	UAvg     float64 `json:"uavg"`
	VAvg     float64 `json:"vavg"`
}

// FreezeRejected is a freeze that did not fit, with what was measured. The
// figures are absent when no frame could be sampled.
type FreezeRejected struct {
	Start    float64  `json:"start"`
	Duration float64  `json:"duration"`
	At       float64  `json:"at"`
	YMax     *float64 `json:"ymax,omitempty"`
	UAvg     *float64 `json:"uavg,omitempty"`
	VAvg     *float64 `json:"vavg,omitempty"`
	Reason   string   `json:"reason"`
}

// FreezeFile is one file's result; see FindFile.
type FreezeFile struct {
	Path       string            `json:"path"`
	Error      string            `json:"error,omitempty"`
	Window     [2]float64        `json:"window"`
	Freezes    int               `json:"freezes"`
	Candidates []FreezeCandidate `json:"candidates"`
	Rejected   []FreezeRejected  `json:"rejected"`
	Notes      []string          `json:"notes,omitempty"`
	Best       *float64          `json:"best,omitempty"`
}

// FreezeReport is mkv-cards freeze's one JSON value.
type FreezeReport struct {
	Mode   string       `json:"mode"`
	Params FreezeParams `json:"params"`
	Files  []FreezeFile `json:"files"`
	Found  int          `json:"found"`
	Total  int          `json:"total"`
}

// MarshalJSON writes a failed file as {"path", "error"} and a read one in full.
func (f FreezeFile) MarshalJSON() ([]byte, error) {
	if f.Error != "" {
		return json.Marshal(struct {
			Path  string `json:"path"`
			Error string `json:"error"`
		}{f.Path, f.Error})
	}
	type plain FreezeFile
	return json.Marshal(plain(f))
}

// Located is what a contact sheet needs from either report: the times to draw
// for one file, and which of them was the best.
type Located struct {
	Path  string
	Error string
	// At is every candidate's time, in report order.
	At []float64
	// Best is the At of the best candidate, nil when there is none.
	Best *float64
}

// ReadLocated reads a find or freeze report back as one Located per file, in
// the report's order.
func ReadLocated(r io.Reader) ([]Located, error) {
	var doc struct {
		Mode  string `json:"mode"`
		Files []struct {
			Path       string `json:"path"`
			Error      string `json:"error"`
			Candidates []struct {
				At float64 `json:"at"`
			} `json:"candidates"`
			Best *float64 `json:"best"`
		} `json:"files"`
	}
	if err := json.NewDecoder(r).Decode(&doc); err != nil {
		return nil, fmt.Errorf("reading the candidates report: %w", err)
	}
	if doc.Mode != "find" && doc.Mode != "freeze" {
		return nil, fmt.Errorf("the candidates report has mode %q, want find or freeze", doc.Mode)
	}
	out := make([]Located, len(doc.Files))
	for i, f := range doc.Files {
		out[i] = Located{Path: f.Path, Error: f.Error, Best: f.Best}
		for _, c := range f.Candidates {
			out[i].At = append(out[i].At, c.At)
		}
	}
	return out, nil
}
