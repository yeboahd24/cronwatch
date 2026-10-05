package storage

import (
	"context"
	"database/sql"
	"time"

	"github.com/yeboahd24/cronwatch/internal/db"
	"github.com/yeboahd24/cronwatch/internal/logs"
	"github.com/yeboahd24/cronwatch/internal/model"
)

// FailureHistory is how often a job failed the same way before a run.
type FailureHistory struct {
	Earlier   int        // earlier runs with the same failure signature
	FirstSeen *time.Time // the first of them; nil when Earlier is 0
}

// FailureHistoryBefore counts the job's runs that failed the way run did and
// started before it.
func (s *Store) FailureHistoryBefore(ctx context.Context, run model.Run) (FailureHistory, error) {
	var h FailureHistory
	row, err := db.New(s.DB).FailureHistoryBefore(ctx, db.FailureHistoryBeforeParams{JobID: run.JobID,
		FailureSignature: sql.NullString{String: run.FailureSignature, Valid: true}, StartedAt: timestamp(run.StartedAt)})
	if err != nil {
		return h, err
	}
	h.Earlier = int(row.Runs)
	if row.FirstSeen != "" {
		first, err := parseTime(row.FirstSeen)
		if err != nil {
			return h, err
		}
		h.FirstSeen = &first
	}
	return h, nil
}

// FailureGroup is one way a job has failed.
type FailureGroup struct {
	Signature           string
	Runs                int
	FirstSeen, LastSeen time.Time
	Latest              model.Run // the newest run that failed this way
}

// FailureGroups returns up to limit ways the job has failed, most recently
// seen first.
func (s *Store) FailureGroups(ctx context.Context, jobID string, limit int) ([]FailureGroup, error) {
	rows, err := db.New(s.DB).ListFailureGroups(ctx, db.ListFailureGroupsParams{JobID: jobID, Limit: int64(limit)})
	if err != nil {
		return nil, err
	}
	groups := make([]FailureGroup, 0, len(rows))
	for _, row := range rows {
		g := FailureGroup{Signature: row.FailureSignature.String, Runs: int(row.Runs)}
		if g.FirstSeen, err = parseTime(row.FirstSeen); err != nil {
			return nil, err
		}
		if g.LastSeen, err = parseTime(row.LastSeen); err != nil {
			return nil, err
		}
		if g.Latest, err = s.GetRun(ctx, row.LatestRunID); err != nil {
			return nil, err
		}
		groups = append(groups, g)
	}
	return groups, nil
}

// BackfillFailureSignatures signs up to limit failed runs recorded before
// failure signatures existed, and returns how many it signed.
func (s *Store) BackfillFailureSignatures(ctx context.Context, limit int) (int, error) {
	q := db.New(s.DB)
	rows, err := q.ListUnsignedFailures(ctx, int64(limit))
	if err != nil {
		return 0, err
	}
	for i, row := range rows {
		var code *int
		if row.ExitCode.Valid {
			c := int(row.ExitCode.Int64)
			code = &c
		}
		sig := logs.FailureSignature(row.Status, row.Stderr, row.Stdout, code)
		if err := q.SetFailureSignature(ctx, db.SetFailureSignatureParams{FailureSignature: sql.NullString{String: sig, Valid: true}, ID: row.ID}); err != nil {
			return i, err
		}
	}
	return len(rows), nil
}
