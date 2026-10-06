package storage

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/yeboahd24/cronwatch/internal/db"
	"github.com/yeboahd24/cronwatch/internal/logs"
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

// ExpireHeartbeatRuns records as timed out the heartbeat runs, of the job
// with jobID or of every job if jobID is "", that have waited longer than
// their job's --max-duration for an end ping. It returns the runs it ended.
func (s *Store) ExpireHeartbeatRuns(ctx context.Context, jobID string, now time.Time) ([]model.Run, error) {
	var filter any
	if jobID != "" {
		filter = jobID
	}
	rows, err := db.New(s.DB).ListOpenHeartbeatRunsWithLimit(ctx, filter)
	if err != nil {
		return nil, err
	}
	var expired []model.Run
	for _, row := range rows {
		run, err := convertRun(row.Run)
		if err != nil {
			return expired, err
		}
		limit := time.Duration(row.MaxDurationSeconds.Int64) * time.Second
		if now.Sub(run.StartedAt) <= limit {
			continue
		}
		// "2h" rather than "2h0m0s", as it would be passed to the flag.
		within := strings.TrimSuffix(strings.TrimSuffix(limit.String(), "0s"), "0m")
		note := fmt.Sprintf("cronwatch: no end ping within %s (--max-duration)\n", within)
		err = s.CompleteRun(ctx, run.ID, Completion{Ended: now, Duration: now.Sub(run.StartedAt), Status: "timeout",
			Stderr: note, Combined: string(logs.StderrMark) + note, Reason: fmt.Sprintf("no end ping within %s (--max-duration)", within)})
		if err != nil {
			continue // ended concurrently by its end ping or another process
		}
		if run, err = s.GetRun(ctx, run.ID); err != nil {
			return expired, err
		}
		expired = append(expired, run)
	}
	return expired, nil
}
