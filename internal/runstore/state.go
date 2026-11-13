// Package runstore owns a run's durable record on the NAS: the schema-2
// state.json, how it is written, listed and judged alive or dead.
package runstore

import (
	"time"

	"github.com/reclyptor/mediaops/internal/health"
)

// Schema is the only state.json version the tools read.
const Schema = 2

// Kind is what a run was started to do.
type Kind string

const (
	KindRip      Kind = "rip"
	KindIdentify Kind = "identify"
)

// RunState is a run's verdict so far.
type RunState string

const (
	RunRunning    RunState = "running"
	RunFinalizing RunState = "finalizing"
	RunComplete   RunState = "complete"
	RunIncomplete RunState = "incomplete"
)

// Terminal reports whether the run has a verdict.
func (s RunState) Terminal() bool { return s == RunComplete || s == RunIncomplete }

// DiscState is where one disc is in its life.
type DiscState string

const (
	DiscPending  DiscState = "pending"
	DiscBlocked  DiscState = "blocked"
	DiscScanning DiscState = "scanning"
	DiscRipping  DiscState = "ripping"
	DiscDone     DiscState = "done"
	DiscFailed   DiscState = "failed"
	DiscSuspect  DiscState = "suspect"
)

// Terminal reports whether the disc will change no further.
func (s DiscState) Terminal() bool {
	return s == DiscDone || s == DiscFailed || s == DiscBlocked || s == DiscSuspect
}

// Active reports whether the disc holds a drive.
func (s DiscState) Active() bool { return s == DiscScanning || s == DiscRipping }

// State is state.json: the one record of a run, and the shape `status` prints.
type State struct {
	Schema      int        `json:"schema"`
	RunID       string     `json:"run_id"`
	Kind        Kind       `json:"kind"`
	State       RunState   `json:"state"`
	Reason      *string    `json:"reason"`
	StartedAt   time.Time  `json:"started_at"`
	FinishedAt  *time.Time `json:"finished_at"`
	ToolVersion string     `json:"tool_version"`
	Controller  Controller `json:"controller"`
	Health      Health     `json:"health"`
	Discs       []Disc     `json:"discs"`
	Problems    []Problem  `json:"problems"`
}

// Controller identifies the process driving the run. Alive is filled in when the
// state is loaded, from the drive lease; the stored value means nothing.
type Controller struct {
	PID   int  `json:"pid"`
	Alive bool `json:"alive"`
}

// Health is what the watch has seen. Events keeps the last 20.
type Health struct {
	Dmesg       health.Dmesg   `json:"dmesg"`
	PausedUntil *time.Time     `json:"paused_until"`
	PauseReason *string        `json:"pause_reason"`
	Events      []health.Event `json:"events"`
}

// MaxEvents is how many health events a run keeps.
const MaxEvents = 20

// DriveRef is the drive a disc was started on.
type DriveRef struct {
	Dev      string `json:"dev"`
	SG       string `json:"sg"`
	USBPort  string `json:"usb_port"`
	LinkMbps int    `json:"link_mbps"`
}

// Progress is makemkvcon's current step.
type Progress struct {
	Phase   string `json:"phase"`
	Current int    `json:"current"`
	Total   int    `json:"total"`
}

// TitleCount is how many titles a disc has finished, out of how many it will.
type TitleCount struct {
	Done  int `json:"done"`
	Total int `json:"total"`
}

// Eject is the outcome of opening the tray. A failed eject never fails a
// verified rip.
type Eject struct {
	OK     bool   `json:"ok"`
	Detail string `json:"detail"`
}

// IdentifiedTitle is one row of the title table an identify scan read.
type IdentifiedTitle struct {
	ID              int    `json:"id"`
	Duration        string `json:"duration"`
	DurationSeconds *int   `json:"duration_seconds"`
	Chapters        *int   `json:"chapters"`
	SourcePlaylist  string `json:"source_playlist"`
	SizeBytes       *int64 `json:"size_bytes"`
}

// Identified is what an identify scan learned about a disc.
type Identified struct {
	Name        string            `json:"name"`
	VolumeLabel string            `json:"volume_label"`
	Type        string            `json:"type"`
	Titles      []IdentifiedTitle `json:"titles"`
}

// Disc is one disc's record. Dest is empty for an identify run.
type Disc struct {
	Index         int            `json:"index"`
	Serial        string         `json:"serial"`
	Label         string         `json:"label"`
	Dest          string         `json:"dest"`
	State         DiscState      `json:"state"`
	Reason        *string        `json:"reason"`
	Drive         *DriveRef      `json:"drive"`
	Progress      *Progress      `json:"progress"`
	Titles        TitleCount     `json:"titles"`
	MakemkvExit   *int           `json:"makemkv_exit"`
	Errors        []string       `json:"errors"`
	SuspectEvents []health.Event `json:"suspect_events"`
	Eject         *Eject         `json:"eject"`
	Identified    *Identified    `json:"identified"`
	StartedAt     *time.Time     `json:"started_at"`
	CopyStartedAt *time.Time     `json:"copy_started_at"`
	FinishedAt    *time.Time     `json:"finished_at"`
}

// Problem is a disc that did not end `done`.
type Problem struct {
	Index  int       `json:"index"`
	Serial string    `json:"serial"`
	Label  string    `json:"label"`
	State  DiscState `json:"state"`
	Reason string    `json:"reason"`
}

// Summary is one row of `mkv-rip list`.
type Summary struct {
	RunID      string     `json:"run_id"`
	Kind       Kind       `json:"kind"`
	State      RunState   `json:"state"`
	StartedAt  time.Time  `json:"started_at"`
	FinishedAt *time.Time `json:"finished_at"`
}
