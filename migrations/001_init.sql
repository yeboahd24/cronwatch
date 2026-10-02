-- +goose Up
CREATE TABLE IF NOT EXISTS jobs (
    id TEXT PRIMARY KEY,
    slug TEXT NOT NULL UNIQUE,
    name TEXT NOT NULL,
    command TEXT NOT NULL,
    schedule TEXT,
    grace_seconds INTEGER NOT NULL DEFAULT 300,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS runs (
    id TEXT PRIMARY KEY,
    job_id TEXT NOT NULL REFERENCES jobs(id) ON DELETE CASCADE,
    started_at TEXT NOT NULL,
    ended_at TEXT,
    duration_ms INTEGER,
    status TEXT NOT NULL CHECK (status IN ('running', 'success', 'failed', 'cancelled')),
    exit_code INTEGER,
    stdout TEXT NOT NULL DEFAULT '',
    stderr TEXT NOT NULL DEFAULT '',
    combined_log TEXT NOT NULL DEFAULT '',
    truncated INTEGER NOT NULL DEFAULT 0 CHECK (truncated IN (0, 1)),
    created_at TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_runs_job_started ON runs(job_id, started_at DESC);
CREATE INDEX IF NOT EXISTS idx_runs_status ON runs(status);

CREATE TABLE IF NOT EXISTS missed_occurrences (
    id TEXT PRIMARY KEY,
    job_id TEXT NOT NULL REFERENCES jobs(id) ON DELETE CASCADE,
    expected_at TEXT NOT NULL,
    detected_at TEXT NOT NULL,
    UNIQUE(job_id, expected_at)
);

CREATE INDEX IF NOT EXISTS idx_missed_job_expected ON missed_occurrences(job_id, expected_at DESC);

-- +goose Down
DROP TABLE IF EXISTS missed_occurrences;
DROP TABLE IF EXISTS runs;
DROP TABLE IF EXISTS jobs;
