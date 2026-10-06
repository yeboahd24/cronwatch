package storage

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/yeboahd24/cronwatch/internal/db"
)

// CrontabChange is one difference between a crontab snapshot and the one
// before it.
type CrontabChange struct {
	TakenAt time.Time // when CronWatch noticed the change
	JobSlug string    // "" for lines that do not run a job through cronwatch
	// Kind is added, removed, schedule or changed for a job's line, and
	// line_added or line_removed for other lines.
	Kind          string
	Before, After string
}

// CrontabSnapshot is the crontab as it was at some time.
type CrontabSnapshot struct {
	TakenAt time.Time
	Hash    string
	Content string
}

// LatestCrontabSnapshot returns the newest snapshot, or nil if none exists.
func (s *Store) LatestCrontabSnapshot(ctx context.Context) (*CrontabSnapshot, error) {
	row, err := db.New(s.DB).LatestCrontabSnapshot(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	taken, err := parseTime(row.TakenAt)
	if err != nil {
		return nil, err
	}
	return &CrontabSnapshot{TakenAt: taken, Hash: row.ContentHash, Content: row.Content}, nil
}

// RecordCrontabSnapshot stores a snapshot and the changes since the previous
// one in one transaction.
func (s *Store) RecordCrontabSnapshot(ctx context.Context, snap CrontabSnapshot, changes []CrontabChange) error {
	id, err := newID()
	if err != nil {
		return err
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	q := db.New(tx)
	taken := timestamp(snap.TakenAt)
	if err := q.InsertCrontabSnapshot(ctx, db.InsertCrontabSnapshotParams{ID: id, TakenAt: taken, ContentHash: snap.Hash, Content: snap.Content}); err != nil {
		return err
	}
	for _, c := range changes {
		cid, err := newID()
		if err != nil {
			return err
		}
		if err := q.InsertCrontabChange(ctx, db.InsertCrontabChangeParams{ID: cid, SnapshotID: id, TakenAt: taken,
			JobSlug: sql.NullString{String: c.JobSlug, Valid: c.JobSlug != ""}, Kind: c.Kind, BeforeText: c.Before, AfterText: c.After}); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func convertChanges(rows []db.CrontabChange) ([]CrontabChange, error) {
	out := make([]CrontabChange, 0, len(rows))
	for _, r := range rows {
		taken, err := parseTime(r.TakenAt)
		if err != nil {
			return nil, err
		}
		out = append(out, CrontabChange{TakenAt: taken, JobSlug: r.JobSlug.String, Kind: r.Kind, Before: r.BeforeText, After: r.AfterText})
	}
	return out, nil
}

// CrontabChanges returns up to limit changes, newest first.
func (s *Store) CrontabChanges(ctx context.Context, limit int) ([]CrontabChange, error) {
	rows, err := db.New(s.DB).ListCrontabChanges(ctx, int64(limit))
	if err != nil {
		return nil, err
	}
	return convertChanges(rows)
}

// JobCrontabChanges returns up to limit changes to the job's crontab line,
// newest first.
func (s *Store) JobCrontabChanges(ctx context.Context, slug string, limit int) ([]CrontabChange, error) {
	rows, err := db.New(s.DB).ListJobCrontabChanges(ctx, db.ListJobCrontabChangesParams{JobSlug: sql.NullString{String: slug, Valid: true}, Limit: int64(limit)})
	if err != nil {
		return nil, err
	}
	return convertChanges(rows)
}

// JobCrontabChangesSince returns changes to jobs' crontab lines noticed at or
// after t, oldest first, by job slug.
func (s *Store) JobCrontabChangesSince(ctx context.Context, t time.Time) (map[string][]CrontabChange, error) {
	rows, err := db.New(s.DB).ListCrontabChangesSince(ctx, timestamp(t))
	if err != nil {
		return nil, err
	}
	changes, err := convertChanges(rows)
	if err != nil {
		return nil, err
	}
	out := map[string][]CrontabChange{}
	for _, c := range changes {
		out[c.JobSlug] = append(out[c.JobSlug], c)
	}
	return out, nil
}

// SyncJobsInCrontab records that the user's crontab runs the jobs with the
// given slugs, and removes the schedule of every job it ran at its last sync
// but no longer does, so those jobs are not reported as missed. It returns
// the names of the jobs whose schedule it removed.
func (s *Store) SyncJobsInCrontab(ctx context.Context, slugs []string, now time.Time) ([]string, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	q := db.New(tx)
	present := map[string]bool{}
	for _, slug := range slugs {
		present[slug] = true
		if err := q.MarkJobInCrontab(ctx, slug); err != nil {
			return nil, err
		}
	}
	jobs, err := q.ListJobsInCrontab(ctx)
	if err != nil {
		return nil, err
	}
	var unscheduled []string
	for _, j := range jobs {
		if present[j.Slug] {
			continue
		}
		at := timestamp(now)
		if err := q.RemoveJobFromCrontab(ctx, db.RemoveJobFromCrontabParams{ID: j.ID, UpdatedAt: at, MissedCheckedUntil: sql.NullString{String: at, Valid: true}}); err != nil {
			return nil, err
		}
		if j.Schedule.Valid {
			unscheduled = append(unscheduled, j.Name)
		}
	}
	return unscheduled, tx.Commit()
}
