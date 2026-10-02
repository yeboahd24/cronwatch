-- name: CreateRun :exec
INSERT INTO runs (id, job_id, started_at, status, created_at, pid, host)
VALUES (?, ?, ?, 'running', ?, ?, ?);

-- name: FinishRun :execrows
UPDATE runs SET ended_at = ?, duration_ms = ?, status = ?, exit_code = ?,
    stdout = ?, stderr = ?, combined_log = ?, truncated = ?
WHERE id = ? AND status = 'running';

-- name: GetRun :one
SELECT * FROM runs WHERE id = ?;

-- name: ListRunsForJob :many
SELECT * FROM runs WHERE job_id = ? ORDER BY started_at DESC LIMIT ?;

-- name: ListRunningRuns :many
SELECT * FROM runs WHERE status = 'running';

-- name: ListRunsWithJob :many
SELECT sqlc.embed(runs), jobs.name AS job_name
FROM runs JOIN jobs ON jobs.id = runs.job_id
ORDER BY runs.started_at DESC LIMIT ?;

-- name: ListRunStartsSince :many
SELECT started_at FROM runs WHERE job_id = ? AND started_at >= ? ORDER BY started_at;

-- name: DeleteRunsBefore :execrows
DELETE FROM runs WHERE status != 'running' AND started_at < ?;

-- name: DeleteRunsBeyondKeep :execrows
-- The parameter is keep-1: the offset of the oldest run to keep for each job.
DELETE FROM runs WHERE status != 'running' AND started_at < COALESCE((
    SELECT kept.started_at FROM runs AS kept
    WHERE kept.job_id = runs.job_id
    ORDER BY kept.started_at DESC LIMIT 1 OFFSET ?
), '');
