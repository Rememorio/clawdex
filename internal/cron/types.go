// Package cron implements persistent scheduled jobs for the gateway.
package cron

import (
	"time"

	"github.com/Rememorio/clawdex/internal/channel"
)

const (
	ScheduleAt    = "at"
	ScheduleEvery = "every"
	ScheduleCron  = "cron"

	PayloadMessage = "message"
	PayloadAgent   = "agent"

	StatusOK      = "ok"
	StatusError   = "error"
	StatusRunning = "running"
)

// Schedule describes when a job should run.
type Schedule struct {
	Kind         string `json:"kind"`
	At           string `json:"at,omitempty"`
	EverySeconds int64  `json:"every_seconds,omitempty"`
	Anchor       string `json:"anchor,omitempty"`
	Expr         string `json:"expr,omitempty"`
	Timezone     string `json:"timezone,omitempty"`
}

// Payload describes what a scheduled job should do.
type Payload struct {
	Kind string `json:"kind"`
	Text string `json:"text"`
}

// JobState stores mutable execution state.
type JobState struct {
	NextRunAt   *time.Time `json:"next_run_at,omitempty"`
	LastRunAt   *time.Time `json:"last_run_at,omitempty"`
	LastStatus  string     `json:"last_status,omitempty"`
	LastError   string     `json:"last_error,omitempty"`
	RunningAt   *time.Time `json:"running_at,omitempty"`
	RunCount    int        `json:"run_count,omitempty"`
	Delivered   bool       `json:"delivered,omitempty"`
	DisabledAt  *time.Time `json:"disabled_at,omitempty"`
	CompletedAt *time.Time `json:"completed_at,omitempty"`
}

// Job is the persisted scheduled job record.
type Job struct {
	ID        string                 `json:"id"`
	Name      string                 `json:"name,omitempty"`
	Enabled   bool                   `json:"enabled"`
	Schedule  Schedule               `json:"schedule"`
	Payload   Payload                `json:"payload"`
	Delivery  channel.DeliveryTarget `json:"delivery"`
	ScopeID   int64                  `json:"scope_id,omitempty"`
	CreatedAt time.Time              `json:"created_at"`
	UpdatedAt time.Time              `json:"updated_at"`
	State     JobState               `json:"state"`
}

// CreateJob is the validated input for adding a job.
type CreateJob struct {
	Name     string
	Schedule Schedule
	Payload  Payload
	Delivery channel.DeliveryTarget
	ScopeID  int64
	Enabled  *bool
}

// PatchJob is a partial update for a job.
type PatchJob struct {
	Name     *string
	Enabled  *bool
	Schedule *Schedule
	Payload  *Payload
}

// RunResult summarizes one manual or scheduled run.
type RunResult struct {
	JobID     string `json:"job_id"`
	Status    string `json:"status"`
	Error     string `json:"error,omitempty"`
	Delivered bool   `json:"delivered,omitempty"`
}
