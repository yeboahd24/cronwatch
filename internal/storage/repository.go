package storage

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
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
	return j, err
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

func (s *Store) UpsertJob(ctx context.Context, slug, name, command, expression string, grace time.Duration) (model.Job, error) {
	id, err := newID()
	if err != nil {
		return model.Job{}, err
	}
	now := timestamp(time.Now())
	err = db.New(s.DB).UpsertJob(ctx, db.UpsertJobParams{ID: id, Slug: slug, Name: name, Command: command,
		Schedule: sql.NullString{String: expression, Valid: expression != ""}, GraceSeconds: int64(grace / time.Second), CreatedAt: now, UpdatedAt: now})
	if err != nil {
		return model.Job{}, err
	}
	return s.GetJobBySlug(ctx, slug)
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
	err = db.New(s.DB).CreateRun(ctx, db.CreateRunParams{ID: id, JobID: jobID, StartedAt: timestamp(started), CreatedAt: timestamp(started)})
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
func (s *Store) ListRuns(ctx context.Context, limit int) ([]model.Run, error) {
	rows, err := db.New(s.DB).ListRuns(ctx, int64(limit))
	if err != nil {
		return nil, err
	}
	return convertRuns(rows)
}
func (s *Store) RecordMissedOccurrence(ctx context.Context, jobID string, expected time.Time) error {
	id, err := newID()
	if err != nil {
		return err
	}
	return db.New(s.DB).RecordMissedOccurrence(ctx, db.RecordMissedOccurrenceParams{ID: id, JobID: jobID, ExpectedAt: timestamp(expected), DetectedAt: timestamp(time.Now())})
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
	localNow := now.In(time.Local)
	next, err := schedule.Next(*j.Schedule, localNow)
	if err != nil {
		return v, err
	}
	v.NextExpectedAt = &next
	previous, err := schedule.Previous(*j.Schedule, localNow.Add(time.Nanosecond))
	if err != nil {
		return v, err
	}
	if !previous.After(j.CreatedAt) || now.Before(previous.Add(time.Duration(j.GraceSeconds)*time.Second)) || v.Status == "running" {
		return v, nil
	}
	if v.LastRun != nil && !v.LastRun.StartedAt.Before(previous) {
		return v, nil
	}
	if err := s.RecordMissedOccurrence(ctx, j.ID, previous); err != nil {
		return v, err
	}
	v.Status = "missed"
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
