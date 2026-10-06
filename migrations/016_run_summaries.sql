-- +goose Up
-- Runs without their output, for lists: output can be megabytes per run.
CREATE VIEW run_summaries AS
SELECT id, job_id, started_at, ended_at, duration_ms, status, exit_code, truncated, created_at,
       pid, host, env_hash, reason, overlapped_run_id, max_rss_kb, user_cpu_ms, sys_cpu_ms,
       failure_signature, output_at
FROM runs;

-- +goose Down
DROP VIEW IF EXISTS run_summaries;
