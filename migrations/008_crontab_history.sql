-- +goose Up
-- The crontab as it was each time it changed, with values of secret-looking
-- variables replaced by a short hash.
CREATE TABLE IF NOT EXISTS crontab_snapshots (
    id TEXT PRIMARY KEY,
    taken_at TEXT NOT NULL,
    content_hash TEXT NOT NULL,
    content TEXT NOT NULL
);
CREATE INDEX idx_crontab_snapshots_taken ON crontab_snapshots(taken_at DESC);

-- What changed between a snapshot and the one before it. job_slug is NULL
-- for lines that do not run a job through cronwatch.
CREATE TABLE IF NOT EXISTS crontab_changes (
    id TEXT PRIMARY KEY,
    snapshot_id TEXT NOT NULL REFERENCES crontab_snapshots(id) ON DELETE CASCADE,
    taken_at TEXT NOT NULL,
    job_slug TEXT,
    kind TEXT NOT NULL CHECK (kind IN ('added', 'removed', 'schedule', 'changed', 'line_added', 'line_removed')),
    before_text TEXT NOT NULL DEFAULT '',
    after_text TEXT NOT NULL DEFAULT ''
);
CREATE INDEX idx_crontab_changes_job ON crontab_changes(job_slug, taken_at DESC);
CREATE INDEX idx_crontab_changes_taken ON crontab_changes(taken_at DESC);

-- +goose Down
DROP TABLE IF EXISTS crontab_changes;
DROP TABLE IF EXISTS crontab_snapshots;
