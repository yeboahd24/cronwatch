package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"

	"github.com/yeboahd24/cronwatch/internal/db"
	"github.com/yeboahd24/cronwatch/internal/model"
	"github.com/yeboahd24/cronwatch/internal/runenv"
)

// SetRunEnv records the environment a run started in. Identical environments
// are stored once.
func (s *Store) SetRunEnv(ctx context.Context, runID string, env runenv.Env) error {
	data, err := json.Marshal(env)
	if err != nil {
		return err
	}
	hash := env.Hash()
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	q := db.New(tx)
	if err := q.InsertEnvironment(ctx, db.InsertEnvironmentParams{Hash: hash, Data: string(data), CreatedAt: timestamp(time.Now())}); err != nil {
		return err
	}
	if err := q.SetRunEnv(ctx, db.SetRunEnvParams{EnvHash: sql.NullString{String: hash, Valid: true}, ID: runID}); err != nil {
		return err
	}
	return tx.Commit()
}

// GetEnvironment returns a recorded environment by hash.
func (s *Store) GetEnvironment(ctx context.Context, hash string) (runenv.Env, error) {
	var env runenv.Env
	data, err := db.New(s.DB).GetEnvironment(ctx, hash)
	if err != nil {
		return env, err
	}
	err = json.Unmarshal([]byte(data), &env)
	return env, err
}

// LatestRunWithEnv returns the job's newest run that has a recorded
// environment, or sql.ErrNoRows.
func (s *Store) LatestRunWithEnv(ctx context.Context, jobID string) (model.Run, error) {
	row, err := db.New(s.DB).LatestRunWithEnv(ctx, jobID)
	if err != nil {
		return model.Run{}, err
	}
	return convertRun(row)
}

// LastSuccessWithEnvBefore returns the job's newest successful run with a
// recorded environment that started before t, or sql.ErrNoRows.
func (s *Store) LastSuccessWithEnvBefore(ctx context.Context, jobID string, t time.Time) (model.Run, error) {
	row, err := db.New(s.DB).LastSuccessWithEnvBefore(ctx, db.LastSuccessWithEnvBeforeParams{JobID: jobID, StartedAt: timestamp(t)})
	if err != nil {
		return model.Run{}, err
	}
	return convertRun(row)
}
