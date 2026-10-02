-- name: CreateRun :exec
INSERT INTO runs (id, job_id, started_at, status, created_at)
VALUES (?, ?, ?, 'running', ?);

-- name: FinishRun :execrows
UPDATE runs SET ended_at = ?, duration_ms = ?, status = ?, exit_code = ?,
    stdout = ?, stderr = ?, combined_log = ?, truncated = ?
WHERE id = ? AND status = 'running';

-- name: GetRun :one
SELECT * FROM runs WHERE id = ?;

-- name: ListRunsForJob :many
SELECT * FROM runs WHERE job_id = ? ORDER BY started_at DESC LIMIT ?;

-- name: ListRuns :many
SELECT * FROM runs ORDER BY started_at DESC LIMIT ?;
