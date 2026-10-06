// Package hub lets servers report their jobs to one CronWatch, the hub,
// which shows them together. A server pushes a summary of its jobs, without
// their output, over HTTPS with a token the hub issued it.
package hub

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/yeboahd24/cronwatch/internal/logs"
	"github.com/yeboahd24/cronwatch/internal/model"
	"github.com/yeboahd24/cronwatch/internal/storage"
)

// ReportVersion is the version of the report format.
const ReportVersion = 1

// Limits on a report, so one host cannot fill the hub.
const (
	MaxReportBytes = 1 << 20
	MaxJobs        = 2000
	maxText        = 500 // bytes of a reason or error line
	maxName        = 200 // bytes of a name, slug, status or schedule
)

// Report is what a server sends its hub: its jobs, without their output.
type Report struct {
	Version   int       `json:"version"`
	Cronwatch string    `json:"cronwatch"` // the sender's version
	Hostname  string    `json:"hostname"`  // as the sender knows itself
	SentAt    time.Time `json:"sent_at"`
	// MaintainedAt is when the sender last checked for missed runs; nil if
	// it never has.
	MaintainedAt *time.Time `json:"maintained_at"`
	Jobs         []Job      `json:"jobs"`
}

// Job is one job in a report.
type Job struct {
	Slug              string     `json:"slug"`
	Name              string     `json:"name"`
	Status            string     `json:"status"`
	Schedule          *string    `json:"schedule"`
	LastRun           *Run       `json:"last_run"`
	NextExpectedAt    *time.Time `json:"next_expected_at"`
	MissedAt          *time.Time `json:"missed_at"`
	PausedUntil       *time.Time `json:"paused_until"`
	UndeliveredAlerts int        `json:"undelivered_alerts"`
}

// Run is a job's last run in a report.
type Run struct {
	StartedAt  time.Time  `json:"started_at"`
	EndedAt    *time.Time `json:"ended_at"`
	DurationMS *int64     `json:"duration_ms"`
	Status     string     `json:"status"`
	ExitCode   *int       `json:"exit_code"`
	Reason     string     `json:"reason"`
	// LastError is the last line the run wrote to stderr: the only output a
	// report carries.
	LastError string `json:"last_error"`
}

// Problem reports whether the job needs attention: it is failing, missed a
// run or has an impossible schedule.
func (j Job) Problem() bool {
	return model.Failing(j.Status) || j.Status == "missed" || j.Status == "invalid_schedule"
}

// BuildReport summarizes the jobs in s, leaving out archived jobs.
func BuildReport(ctx context.Context, s *storage.Store, version string, now time.Time) (Report, error) {
	r := Report{Version: ReportVersion, Cronwatch: version, SentAt: now.UTC(), Jobs: []Job{}}
	r.Hostname, _ = os.Hostname()
	var err error
	if r.MaintainedAt, err = s.Maintained(ctx); err != nil {
		return r, err
	}
	views, err := s.ListJobViews(ctx, now)
	if err != nil {
		return r, err
	}
	undelivered, err := s.UndeliveredAlerts(ctx)
	if err != nil {
		return r, err
	}
	for _, v := range model.Unarchived(views) {
		j := Job{Slug: v.Slug, Name: v.Name, Status: v.Status, Schedule: v.Schedule, NextExpectedAt: v.NextExpectedAt,
			MissedAt: v.MissedAt, UndeliveredAlerts: undelivered[v.ID]}
		if v.Status == "paused" {
			j.PausedUntil = v.PausedUntil
		}
		if last := v.LastRun; last != nil {
			run := &Run{StartedAt: last.StartedAt, EndedAt: last.EndedAt, DurationMS: last.DurationMS, Status: last.Status,
				ExitCode: last.ExitCode, Reason: last.Reason}
			// Views carry run summaries; only a failed run's error is sent.
			if model.Failing(last.Status) {
				if full, err := s.GetRun(ctx, last.ID); err == nil {
					run.LastError = logs.LastError(logs.ParseAs(full.Stderr, logs.Stderr))
				}
			}
			j.LastRun = run
		}
		r.Jobs = append(r.Jobs, j)
	}
	if len(r.Jobs) > MaxJobs {
		r.Jobs = r.Jobs[:MaxJobs]
	}
	r.trim()
	return r, nil
}

// Check validates a received report and trims its text to the limits.
func (r *Report) Check() error {
	switch {
	case r.Version != ReportVersion:
		return fmt.Errorf("report version %d is not supported; this hub reads version %d", r.Version, ReportVersion)
	case len(r.Jobs) > MaxJobs:
		return fmt.Errorf("a report may list at most %d jobs", MaxJobs)
	}
	for _, j := range r.Jobs {
		if j.Slug == "" || j.Status == "" {
			return errors.New("every job needs a slug and a status")
		}
	}
	r.trim()
	return nil
}

func (r *Report) trim() {
	r.Cronwatch, r.Hostname = clip(r.Cronwatch, maxName), clip(r.Hostname, maxName)
	for i := range r.Jobs {
		j := &r.Jobs[i]
		j.Slug, j.Name, j.Status = clip(j.Slug, maxName), clip(j.Name, maxName), clip(j.Status, maxName)
		if j.Schedule != nil {
			s := clip(*j.Schedule, maxName)
			j.Schedule = &s
		}
		if j.LastRun != nil {
			j.LastRun.Status = clip(j.LastRun.Status, maxName)
			j.LastRun.Reason, j.LastRun.LastError = clip(j.LastRun.Reason, maxText), clip(j.LastRun.LastError, maxText)
		}
	}
}

// clip shortens s to at most n bytes of valid UTF-8.
func clip(s string, n int) string {
	s = strings.ToValidUTF8(s, "")
	if len(s) <= n {
		return s
	}
	s = s[:n]
	for !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s + "…"
}
