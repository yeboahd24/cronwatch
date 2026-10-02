package storage

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"syscall"
	"time"

	"github.com/yeboahd24/cronwatch/internal/db"
	"github.com/yeboahd24/cronwatch/internal/model"
	"github.com/yeboahd24/cronwatch/internal/schedule"
)

func newID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

// Fixed-width UTC timestamps preserve chronological order in SQLite TEXT sorts.
func timestamp(t time.Time) string              { return t.UTC().Format("2006-01-02T15:04:05.000000000Z") }
func parseTime(value string) (time.Time, error) { return time.Parse(time.RFC3339Nano, value) }

func convertJob(row db.Job) (model.Job, error) {
	j := model.Job{ID: row.ID, Slug: row.Slug, Name: row.Name, Command: row.Command, GraceSeconds: row.GraceSeconds}
	if row.Schedule.Valid {
		j.Schedule = &row.Schedule.String
	}
	var err error
	j.CreatedAt, err = parseTime(row.CreatedAt)
	if err != nil {
		return j, err
	}
	j.UpdatedAt, err = parseTime(row.UpdatedAt)
	if err != nil {
		return j, err
	}
	if row.MissedCheckedUntil.Valid {
		checked, err := parseTime(row.MissedCheckedUntil.String)
		if err != nil {
			return j, err
		}
		j.MissedCheckedUntil = &checked
	}
	return j, nil
}
func convertRun(row db.Run) (model.Run, error) {
	r := model.Run{ID: row.ID, JobID: row.JobID, Status: row.Status, Stdout: row.Stdout, Stderr: row.Stderr, CombinedLog: row.CombinedLog, Truncated: row.Truncated != 0}
	var err error
	r.StartedAt, err = parseTime(row.StartedAt)
	if err != nil {
		return r, err
	}
	r.CreatedAt, err = parseTime(row.CreatedAt)
	if err != nil {
		return r, err
	}
	if row.EndedAt.Valid {
		ended, e := parseTime(row.EndedAt.String)
		if e != nil {
			return r, e
		}
		r.EndedAt = &ended
	}
	if row.DurationMs.Valid {
		v := row.DurationMs.Int64
		r.DurationMS = &v
	}
	if row.ExitCode.Valid {
		v := int(row.ExitCode.Int64)
		r.ExitCode = &v
	}
	return r, nil
}

// DefaultGrace is the missed-run grace period for jobs that never set one.
const DefaultGrace = 5 * time.Minute

// JobSpec describes a job registration. A nil Schedule or Grace keeps the
// stored value, so an ad-hoc run without flags does not reset the job.
type JobSpec struct {
	Slug, Name, Command string
	Schedule            *string
	Grace               *time.Duration
}

func (s *Store) UpsertJob(ctx context.Context, spec JobSpec) (model.Job, error) {
	id, err := newID()
	if err != nil {
		return model.Job{}, err
	}
	expression, grace := "", DefaultGrace
	if spec.Schedule == nil || spec.Grace == nil {
		existing, err := s.GetJobBySlug(ctx, spec.Slug)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return model.Job{}, err
		}
		if err == nil {
			if existing.Schedule != nil {
				expression = *existing.Schedule
			}
			grace = time.Duration(existing.GraceSeconds) * time.Second
		}
	}
	if spec.Schedule != nil {
		expression = *spec.Schedule
	}
	if spec.Grace != nil {
		grace = *spec.Grace
	}
	now := timestamp(time.Now())
	err = db.New(s.DB).UpsertJob(ctx, db.UpsertJobParams{ID: id, Slug: spec.Slug, Name: spec.Name, Command: spec.Command,
		Schedule: sql.NullString{String: expression, Valid: expression != ""}, GraceSeconds: int64(grace / time.Second), CreatedAt: now, UpdatedAt: now})
	if err != nil {
		return model.Job{}, err
	}
	return s.GetJobBySlug(ctx, spec.Slug)
}
func (s *Store) GetJob(ctx context.Context, id string) (model.Job, error) {
	row, err := db.New(s.DB).GetJob(ctx, id)
	if err != nil {
		return model.Job{}, err
	}
	return convertJob(row)
}
func (s *Store) GetJobBySlug(ctx context.Context, slug string) (model.Job, error) {
	row, err := db.New(s.DB).GetJobBySlug(ctx, slug)
	if err != nil {
		return model.Job{}, err
	}
	return convertJob(row)
}
func (s *Store) ListJobs(ctx context.Context) ([]model.Job, error) {
	rows, err := db.New(s.DB).ListJobs(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]model.Job, 0, len(rows))
	for _, row := range rows {
		j, e := convertJob(row)
		if e != nil {
			return nil, e
		}
		out = append(out, j)
	}
	return out, nil
}
func (s *Store) CreateRun(ctx context.Context, jobID string, started time.Time) (model.Run, error) {
	id, err := newID()
	if err != nil {
		return model.Run{}, err
	}
	host, _ := os.Hostname()
	err = db.New(s.DB).CreateRun(ctx, db.CreateRunParams{ID: id, JobID: jobID, StartedAt: timestamp(started), CreatedAt: timestamp(started),
		Pid: sql.NullInt64{Int64: int64(os.Getpid()), Valid: true}, Host: sql.NullString{String: host, Valid: host != ""}})
	if err != nil {
		return model.Run{}, err
	}
	return s.GetRun(ctx, id)
}
func (s *Store) FinishRun(ctx context.Context, id string, ended time.Time, duration time.Duration, status string, exitCode *int, stdout, stderr, combined string, truncated bool) error {
	code := sql.NullInt64{}
	if exitCode != nil {
		code = sql.NullInt64{Int64: int64(*exitCode), Valid: true}
	}
	truncatedInt := int64(0)
	if truncated {
		truncatedInt = 1
	}
	n, err := db.New(s.DB).FinishRun(ctx, db.FinishRunParams{ID: id, EndedAt: sql.NullString{String: timestamp(ended), Valid: true},
		DurationMs: sql.NullInt64{Int64: duration.Milliseconds(), Valid: true}, Status: status, ExitCode: code,
		Stdout: stdout, Stderr: stderr, CombinedLog: combined, Truncated: truncatedInt})
	if err != nil {
		return err
	}
	if n != 1 {
		return fmt.Errorf("run %s is not running", id)
	}
	return nil
}
func (s *Store) GetRun(ctx context.Context, id string) (model.Run, error) {
	row, err := db.New(s.DB).GetRun(ctx, id)
	if err != nil {
		return model.Run{}, err
	}
	return convertRun(row)
}
func convertRuns(rows []db.Run) ([]model.Run, error) {
	out := make([]model.Run, 0, len(rows))
	for _, row := range rows {
		r, e := convertRun(row)
		if e != nil {
			return nil, e
		}
		out = append(out, r)
	}
	return out, nil
}
func (s *Store) ListRunsForJob(ctx context.Context, jobID string, limit int) ([]model.Run, error) {
	rows, err := db.New(s.DB).ListRunsForJob(ctx, db.ListRunsForJobParams{JobID: jobID, Limit: int64(limit)})
	if err != nil {
		return nil, err
	}
	return convertRuns(rows)
}

// RunWithJob pairs a run with the name of its job.
type RunWithJob struct {
	Run     model.Run
	JobName string
}

func (s *Store) ListRunsWithJob(ctx context.Context, limit int) ([]RunWithJob, error) {
	rows, err := db.New(s.DB).ListRunsWithJob(ctx, int64(limit))
	if err != nil {
		return nil, err
	}
	out := make([]RunWithJob, 0, len(rows))
	for _, row := range rows {
		r, e := convertRun(row.Run)
		if e != nil {
			return nil, e
		}
		out = append(out, RunWithJob{Run: r, JobName: row.JobName})
	}
	return out, nil
}

// processGone reports whether pid definitely no longer exists. EPERM means the
// process exists under another user, so it is treated as alive.
func processGone(pid int64) bool {
	return errors.Is(syscall.Kill(int(pid), 0), syscall.ESRCH)
}

// ReapAbandonedRuns marks running runs as failed when the cronwatch process
// that owns them died on this host without recording a result (SIGKILL, OOM,
// reboot). Runs from other hosts or without an owner PID are left alone.
func (s *Store) ReapAbandonedRuns(ctx context.Context) (int, error) {
	rows, err := db.New(s.DB).ListRunningRuns(ctx)
	if err != nil {
		return 0, err
	}
	host, _ := os.Hostname()
	reaped := 0
	for _, row := range rows {
		if !row.Pid.Valid || !row.Host.Valid || row.Host.String != host || !processGone(row.Pid.Int64) {
			continue
		}
		started, err := parseTime(row.StartedAt)
		if err != nil {
			return reaped, err
		}
		ended := time.Now()
		note := fmt.Sprintf("\ncronwatch: run abandoned; owner process %d exited without recording a result\n", row.Pid.Int64)
		err = s.FinishRun(ctx, row.ID, ended, ended.Sub(started), "failed", nil,
			row.Stdout, row.Stderr+note, row.CombinedLog+note, row.Truncated != 0)
		if err != nil {
			// The owner may have finished the run concurrently.
			continue
		}
		reaped++
	}
	return reaped, nil
}

func (s *Store) LatestMissedOccurrence(ctx context.Context, jobID string) (*time.Time, error) {
	value, err := db.New(s.DB).LatestMissedOccurrence(ctx, jobID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	t, err := parseTime(value)
	return &t, err
}

func (s *Store) JobView(ctx context.Context, j model.Job, now time.Time) (model.JobView, error) {
	v := model.JobView{Job: j, Status: "never_run"}
	runs, err := s.ListRunsForJob(ctx, j.ID, 1)
	if err != nil {
		return v, err
	}
	if len(runs) > 0 {
		v.LastRun = &runs[0]
		v.Status = runs[0].Status
	}
	if j.Schedule == nil {
		return v, nil
	}
	// A bad stored schedule is reported on this job instead of failing every view.
	sched, err := schedule.Parse(*j.Schedule)
	if err != nil {
		v.Status = "invalid_schedule"
		return v, nil
	}
	next := sched.Next(now.In(time.Local))
	if next.IsZero() {
		v.Status = "invalid_schedule"
		return v, nil
	}
	v.NextExpectedAt = &next
	// Missed occurrences are recorded by DetectMissed; a run started at or
	// after the latest one clears the missed state.
	missed, err := s.LatestMissedOccurrence(ctx, j.ID)
	if err != nil {
		return v, err
	}
	if missed != nil && (v.LastRun == nil || v.LastRun.StartedAt.Before(*missed)) {
		v.Status = "missed"
	}
	return v, nil
}
func (s *Store) ListJobViews(ctx context.Context, now time.Time) ([]model.JobView, error) {
	jobs, err := s.ListJobs(ctx)
	if err != nil {
		return nil, err
	}
	views := make([]model.JobView, 0, len(jobs))
	for _, j := range jobs {
		v, e := s.JobView(ctx, j, now)
		if e != nil {
			return nil, e
		}
		views = append(views, v)
	}
	return views, nil
}
