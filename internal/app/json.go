package app

import (
	"encoding/json"
	"io"
	"time"

	"github.com/yeboahd24/cronwatch/internal/model"
)

// The --json output of jobs, runs and sync. Field names are stable; times
// are RFC 3339 in UTC, and absent values are null.

type jsonRun struct {
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

func newJSONRun(r model.Run, job model.Job) jsonRun {
	out := jsonRun{ID: r.ID, Job: job.Name, JobSlug: job.Slug, Status: r.Status, StartedAt: r.StartedAt.UTC(),
		DurationMS: r.DurationMS, ExitCode: r.ExitCode, Reason: r.Reason}
	if r.EndedAt != nil {
		ended := r.EndedAt.UTC()
		out.EndedAt = &ended
	}
	return out
}

type jsonJob struct {
	Slug         string  `json:"slug"`
	Name         string  `json:"name"`
	Status       string  `json:"status"`
	Schedule     *string `json:"schedule"`
	GraceSeconds int64   `json:"grace_seconds"`
	// MaxDurationSeconds is null when heartbeat runs have no limit.
	MaxDurationSeconds *int64     `json:"max_duration_seconds"`
	LastRun            *jsonRun   `json:"last_run"`
	NextExpectedAt     *time.Time `json:"next_expected_at"`
	MissedAt           *time.Time `json:"missed_at"`
}

func newJSONJob(v model.JobView) jsonJob {
	out := jsonJob{Slug: v.Slug, Name: v.Name, Status: v.Status, Schedule: v.Schedule, GraceSeconds: v.GraceSeconds}
	if v.MaxDurationSeconds > 0 {
		out.MaxDurationSeconds = &v.MaxDurationSeconds
	}
	if v.LastRun != nil {
		r := newJSONRun(*v.LastRun, v.Job)
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
	return out
}

type jsonSync struct {
	Jobs        int               `json:"jobs"`
	Added       []string          `json:"added"`
	Updated     []string          `json:"updated"`
	Unscheduled []string          `json:"unscheduled"`
	Problems    []string          `json:"problems"`
	Unmonitored []jsonCrontabLine `json:"unmonitored"`
	Changes     []jsonChange      `json:"crontab_changes"`
}

type jsonChange struct {
	JobSlug string `json:"job_slug,omitempty"`
	Kind    string `json:"kind"`
	Before  string `json:"before,omitempty"`
	After   string `json:"after,omitempty"`
}

type jsonCrontabLine struct {
	Line     int    `json:"line"`
	Schedule string `json:"schedule"`
	Command  string `json:"command"`
}

func newJSONSync(r syncResult) jsonSync {
	out := jsonSync{Jobs: r.Jobs, Added: nonNil(r.Added), Updated: nonNil(r.Updated), Unscheduled: nonNil(r.Unscheduled), Problems: nonNil(r.Problems),
		Unmonitored: []jsonCrontabLine{}, Changes: []jsonChange{}}
	for _, c := range r.Changes {
		out.Changes = append(out.Changes, jsonChange{JobSlug: c.JobSlug, Kind: c.Kind, Before: c.Before, After: c.After})
	}
	for _, e := range r.Unmonitored {
		out.Unmonitored = append(out.Unmonitored, jsonCrontabLine{Line: e.Line, Schedule: e.Raw, Command: e.Command})
	}
	return out
}

// nonNil makes an empty list print as [] rather than null.
func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func writeJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}
