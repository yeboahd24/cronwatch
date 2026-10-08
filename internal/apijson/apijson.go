// Package apijson is the JSON form of jobs and runs, shared by the CLI's
// --json output and the dashboard's API so the two always agree. Field
// names are stable; times are RFC 3339 in UTC, and absent values are null.
package apijson

import (
	"time"

	"github.com/yeboahd24/cronwatch/internal/model"
)

// Run is a run as JSON.
type Run struct {
	ID         string     `json:"id"`
	Job        string     `json:"job"`
	JobSlug    string     `json:"job_slug"`
	Status     string     `json:"status"`
	StartedAt  time.Time  `json:"started_at"`
	EndedAt    *time.Time `json:"ended_at"`
	DurationMS *int64     `json:"duration_ms"`
	ExitCode   *int       `json:"exit_code"`
	Reason     string     `json:"reason,omitempty"`
}

// NewRun returns run r of job as JSON.
func NewRun(r model.Run, job model.Job) Run {
	out := Run{ID: r.ID, Job: job.Name, JobSlug: job.Slug, Status: r.Status, StartedAt: r.StartedAt.UTC(),
		DurationMS: r.DurationMS, ExitCode: r.ExitCode, Reason: r.Reason}
	if r.EndedAt != nil {
		ended := r.EndedAt.UTC()
		out.EndedAt = &ended
	}
	return out
}

// Job is a job and its current status as JSON.
type Job struct {
	Slug         string  `json:"slug"`
	Name         string  `json:"name"`
	Status       string  `json:"status"`
	Schedule     *string `json:"schedule"`
	GraceSeconds int64   `json:"grace_seconds"`
	// MaxDurationSeconds is null when heartbeat runs have no limit.
	MaxDurationSeconds *int64 `json:"max_duration_seconds"`
	// PausedUntil is set while a pause with an end is in effect.
	PausedUntil    *time.Time `json:"paused_until"`
	ArchivedAt     *time.Time `json:"archived_at"`
	LastRun        *Run       `json:"last_run"`
	NextExpectedAt *time.Time `json:"next_expected_at"`
	MissedAt       *time.Time `json:"missed_at"`
	Tags           []string   `json:"tags"` // [] for none
}

// NewJob returns the job view v as JSON.
func NewJob(v model.JobView) Job {
	out := Job{Slug: v.Slug, Name: v.Name, Status: v.Status, Schedule: v.Schedule, GraceSeconds: v.GraceSeconds,
		Tags: append([]string{}, v.Tags...)}
	if v.MaxDurationSeconds > 0 {
		out.MaxDurationSeconds = &v.MaxDurationSeconds
	}
	if v.LastRun != nil {
		r := NewRun(*v.LastRun, v.Job)
		out.LastRun = &r
	}
	utc := func(t *time.Time) *time.Time {
		if t == nil {
			return nil
		}
		u := t.UTC()
		return &u
	}
	out.NextExpectedAt, out.MissedAt = utc(v.NextExpectedAt), utc(v.MissedAt)
	if v.Status == "paused" {
		out.PausedUntil = utc(v.PausedUntil)
	}
	out.ArchivedAt = utc(v.ArchivedAt)
	return out
}
