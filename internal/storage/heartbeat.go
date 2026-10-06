package storage

import (
	"context"
	"database/sql"
	"os"
	"time"

	"github.com/yeboahd24/cronwatch/internal/db"
	"github.com/yeboahd24/cronwatch/internal/model"
)

// CreateHeartbeatRun starts a run reported by "cronwatch ping". It has no
// owner process, so abandoned-run reaping leaves it alone; it ends with the
// job's next ping, or times out after the job's MaxDurationSeconds.
func (s *Store) CreateHeartbeatRun(ctx context.Context, jobID string, started time.Time) (model.Run, error) {
	id, err := newID()
	if err != nil {
		return model.Run{}, err
	}
	host, _ := os.Hostname()
	err = db.New(s.DB).CreateRun(ctx, db.CreateRunParams{ID: id, JobID: jobID, StartedAt: timestamp(started), CreatedAt: timestamp(started),
		Host: sql.NullString{String: host, Valid: host != ""}})
	if err != nil {
		return model.Run{}, err
	}
	return s.GetRun(ctx, id)
}

// OpenHeartbeatRun returns the job's newest heartbeat run still waiting for
// its end ping, or sql.ErrNoRows.
func (s *Store) OpenHeartbeatRun(ctx context.Context, jobID string) (model.Run, error) {
	row, err := db.New(s.DB).LatestOwnerlessRunningRun(ctx, jobID)
	if err != nil {
		return model.Run{}, err
	}
	return convertRun(row)
}

// OverdueRun is a heartbeat run that has waited longer than its job's
// --max-duration for an end ping.
type OverdueRun struct {
	Run   model.Run
	Limit time.Duration
}

// OverdueHeartbeatRuns returns the overdue heartbeat runs of the job with
// jobID, or of every job if jobID is "".
func (s *Store) OverdueHeartbeatRuns(ctx context.Context, jobID string, now time.Time) ([]OverdueRun, error) {
	var filter any
	if jobID != "" {
		filter = jobID
	}
	rows, err := db.New(s.DB).ListOpenHeartbeatRunsWithLimit(ctx, filter)
	if err != nil {
		return nil, err
	}
	var out []OverdueRun
	for _, row := range rows {
		run, err := convertRun(row.Run)
		if err != nil {
			return out, err
		}
		limit := time.Duration(row.MaxDurationSeconds.Int64) * time.Second
		if now.Sub(run.StartedAt) > limit {
			out = append(out, OverdueRun{Run: run, Limit: limit})
		}
	}
	return out, nil
}
