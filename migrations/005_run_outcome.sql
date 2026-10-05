-- +goose Up
-- Widen the status check for timeout and skipped runs. SQLite cannot alter a
-- CHECK constraint, so the table is rebuilt. Nothing references runs.
CREATE TABLE runs_new (
    id TEXT PRIMARY KEY,
    job_id TEXT NOT NULL REFERENCES jobs(id) ON DELETE CASCADE,
    started_at TEXT NOT NULL,
    ended_at TEXT,
    duration_ms INTEGER,
    status TEXT NOT NULL CHECK (status IN ('running', 'success', 'failed', 'cancelled', 'timeout', 'skipped')),
    exit_code INTEGER,
    stdout TEXT NOT NULL DEFAULT '',
    stderr TEXT NOT NULL DEFAULT '',
    combined_log TEXT NOT NULL DEFAULT '',
    truncated INTEGER NOT NULL DEFAULT 0 CHECK (truncated IN (0, 1)),
    created_at TEXT NOT NULL,
    pid INTEGER,
    host TEXT,
    env_hash TEXT,
    -- Why the status differs from what the exit code alone implies.
    reason TEXT,
    -- The run of the same job that was still running when this one started.
    overlapped_run_id TEXT,
    max_rss_kb INTEGER,
    user_cpu_ms INTEGER,
    sys_cpu_ms INTEGER
);
INSERT INTO runs_new (id, job_id, started_at, ended_at, duration_ms, status, exit_code, stdout, stderr,
    combined_log, truncated, created_at, pid, host, env_hash)
SELECT id, job_id, started_at, ended_at, duration_ms, status, exit_code, stdout, stderr,
    combined_log, truncated, created_at, pid, host, env_hash FROM runs;
DROP TABLE runs;
ALTER TABLE runs_new RENAME TO runs;
CREATE INDEX idx_runs_job_started ON runs(job_id, started_at DESC);
CREATE INDEX idx_runs_status ON runs(status);

-- +goose Down
DELETE FROM runs WHERE status IN ('timeout', 'skipped');
CREATE TABLE runs_old (
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
    created_at TEXT NOT NULL,
    pid INTEGER,
    host TEXT,
    env_hash TEXT
);
INSERT INTO runs_old SELECT id, job_id, started_at, ended_at, duration_ms, status, exit_code, stdout, stderr,
    combined_log, truncated, created_at, pid, host, env_hash FROM runs;
DROP TABLE runs;
ALTER TABLE runs_old RENAME TO runs;
CREATE INDEX idx_runs_job_started ON runs(job_id, started_at DESC);
CREATE INDEX idx_runs_status ON runs(status);
