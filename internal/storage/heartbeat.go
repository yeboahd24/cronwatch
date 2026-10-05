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
// job's next ping.
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
