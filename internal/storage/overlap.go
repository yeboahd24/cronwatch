package storage

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/yeboahd24/cronwatch/internal/db"
	"github.com/yeboahd24/cronwatch/internal/model"
)

// SetRunOverlap records that run started while run previous was running.
func (s *Store) SetRunOverlap(ctx context.Context, runID, previous string) error {
	return db.New(s.DB).SetRunOverlap(ctx, db.SetRunOverlapParams{OverlappedRunID: sql.NullString{String: previous, Valid: previous != ""}, ID: runID})
}

// OtherRunningRun returns the job's newest running run other than runID, or
// sql.ErrNoRows.
func (s *Store) OtherRunningRun(ctx context.Context, jobID, runID string) (model.Run, error) {
	row, err := db.New(s.DB).LatestRunningRunBefore(ctx, db.LatestRunningRunBeforeParams{JobID: jobID, ID: runID})
	if err != nil {
		return model.Run{}, err
	}
	return convertRun(row)
}

// PreviousFinishedRun returns the job's newest finished, not skipped run that
// started before t, or sql.ErrNoRows.
func (s *Store) PreviousFinishedRun(ctx context.Context, jobID string, t time.Time) (model.Run, error) {
	row, err := db.New(s.DB).PreviousFinishedRun(ctx, db.PreviousFinishedRunParams{JobID: jobID, StartedAt: timestamp(t)})
	if err != nil {
		return model.Run{}, err
	}
	return convertRun(row)
}

// WasFailingBefore reports whether the job was failing or missed just before
// t: its newest finished run before t failed, or a scheduled run was missed
// after that run started. A job with no history was not failing.
func (s *Store) WasFailingBefore(ctx context.Context, jobID string, t time.Time) (bool, error) {
	var since time.Time
	run, err := s.PreviousFinishedRun(ctx, jobID, t)
	switch {
	case err == nil:
		if model.Failing(run.Status) {
			return true, nil
		}
		since = run.StartedAt
	case !errors.Is(err, sql.ErrNoRows):
		return false, err
	}
	value, err := db.New(s.DB).LatestMissedOccurrenceBefore(ctx, db.LatestMissedOccurrenceBeforeParams{JobID: jobID, ExpectedAt: timestamp(t)})
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	} else if err != nil {
		return false, err
	}
	missed, err := parseTime(value)
	return err == nil && !missed.Before(since), err
}

// JobActivity counts a job's runs by status and its missed occurrences.
type JobActivity struct {
	Runs   map[string]int64 // by status
	Missed int64
}

// ActivitySince counts each job's runs that started, and occurrences that
// were missed, at or after t. Jobs with no activity are absent.
func (s *Store) ActivitySince(ctx context.Context, t time.Time) (map[string]*JobActivity, error) {
	q := db.New(s.DB)
	out := map[string]*JobActivity{}
	get := func(jobID string) *JobActivity {
		if out[jobID] == nil {
			out[jobID] = &JobActivity{Runs: map[string]int64{}}
		}
		return out[jobID]
	}
	runs, err := q.CountRunsSince(ctx, timestamp(t))
	if err != nil {
		return nil, err
	}
	for _, r := range runs {
		get(r.JobID).Runs[r.Status] = r.Runs
	}
	missed, err := q.CountMissedSince(ctx, timestamp(t))
	if err != nil {
		return nil, err
	}
	for _, m := range missed {
		get(m.JobID).Missed = m.Missed
	}
	return out, nil
}
