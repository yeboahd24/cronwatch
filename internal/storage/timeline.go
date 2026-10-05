package storage

import (
	"context"
	"database/sql"
	"time"

	"github.com/yeboahd24/cronwatch/internal/db"
	"github.com/yeboahd24/cronwatch/internal/model"
)

// RunsOverlapping returns runs, without their output, that were running at
// some point between from and to, oldest first.
func (s *Store) RunsOverlapping(ctx context.Context, from, to time.Time) ([]model.Run, error) {
	rows, err := db.New(s.DB).ListRunsOverlapping(ctx, db.ListRunsOverlappingParams{FromTime: sql.NullString{String: timestamp(from), Valid: true}, ToTime: timestamp(to)})
	if err != nil {
		return nil, err
	}
	out := make([]model.Run, 0, len(rows))
	for _, row := range rows {
		r, err := convertRun(db.Run{ID: row.ID, JobID: row.JobID, StartedAt: row.StartedAt, CreatedAt: row.StartedAt,
			EndedAt: row.EndedAt, DurationMs: row.DurationMs, Status: row.Status, ExitCode: row.ExitCode, Reason: row.Reason,
			OverlappedRunID: row.OverlappedRunID})
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, nil
}

// MissedSince returns each job's missed occurrences expected at or after t.
func (s *Store) MissedSince(ctx context.Context, t time.Time) (map[string][]time.Time, error) {
	rows, err := db.New(s.DB).ListMissedSince(ctx, timestamp(t))
	if err != nil {
		return nil, err
	}
	out := map[string][]time.Time{}
	for _, row := range rows {
		at, err := parseTime(row.ExpectedAt)
		if err != nil {
			return nil, err
		}
		out[row.JobID] = append(out[row.JobID], at)
	}
	return out, nil
}
