-- +goose Up
-- Run history across all jobs is listed newest first, a page at a time.
CREATE INDEX IF NOT EXISTS idx_runs_started ON runs(started_at DESC, id DESC);

-- +goose Down
DROP INDEX IF EXISTS idx_runs_started;
