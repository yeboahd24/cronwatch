-- +goose Up
-- Each time a job's --on-failure or --on-recover hook should run, and how
-- delivering it went. Pending alerts are retried until they are delivered or
-- run out of attempts. run_id has no foreign key because runs are pruned
-- separately; environ is the event's CRONWATCH_* variables as a JSON array,
-- so a retry passes what the first attempt did.
CREATE TABLE IF NOT EXISTS alerts (
    id TEXT PRIMARY KEY,
    job_id TEXT NOT NULL REFERENCES jobs(id) ON DELETE CASCADE,
    run_id TEXT,
    event TEXT NOT NULL CHECK (event IN ('failed', 'timeout', 'missed', 'recovered')),
    hook TEXT NOT NULL CHECK (hook IN ('on_failure', 'on_recover')),
    environ TEXT NOT NULL,
    created_at TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'delivered', 'undelivered', 'cancelled')),
    attempts INTEGER NOT NULL DEFAULT 0,
    next_attempt_at TEXT,
    last_attempt_at TEXT,
    last_error TEXT NOT NULL DEFAULT '',
    last_output TEXT NOT NULL DEFAULT ''
);
CREATE INDEX idx_alerts_pending ON alerts(status, job_id, created_at);
CREATE INDEX idx_alerts_job ON alerts(job_id, created_at DESC);
CREATE INDEX idx_alerts_run ON alerts(run_id);

-- +goose Down
DROP TABLE IF EXISTS alerts;
