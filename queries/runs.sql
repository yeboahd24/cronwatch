-- name: CreateRun :exec
INSERT INTO runs (id, job_id, started_at, status, created_at, pid, host)
VALUES (?, ?, ?, 'running', ?, ?, ?);

-- name: FinishRun :execrows
UPDATE runs SET ended_at = ?, duration_ms = ?, status = ?, exit_code = ?,
    stdout = ?, stderr = ?, combined_log = ?, truncated = ?, reason = ?,
    max_rss_kb = ?, user_cpu_ms = ?, sys_cpu_ms = ?, failure_signature = ?
WHERE id = ? AND status = 'running';

-- name: CountRunsSince :many
SELECT job_id, status, count(*) AS runs FROM runs WHERE started_at >= ? GROUP BY job_id, status;

-- name: SetRunOverlap :exec
UPDATE runs SET overlapped_run_id = ? WHERE id = ?;

-- name: LatestRunningRunBefore :one
SELECT * FROM runs WHERE job_id = ? AND status = 'running' AND id != ?
ORDER BY started_at DESC LIMIT 1;

-- name: PreviousFinishedRun :one
-- The job's newest finished run before the given start, ignoring skipped runs.
SELECT * FROM runs
WHERE job_id = ? AND status NOT IN ('running', 'skipped') AND started_at < ?
ORDER BY started_at DESC LIMIT 1;

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

-- name: SearchRunLogs :many
SELECT sqlc.embed(runs), jobs.name AS job_name
FROM runs JOIN jobs ON jobs.id = runs.job_id
WHERE runs.status != 'running'
  AND instr(lower(runs.combined_log), lower(sqlc.arg(query))) > 0
ORDER BY runs.started_at DESC LIMIT sqlc.arg(row_limit);

-- name: SetRunEnv :exec
UPDATE runs SET env_hash = ? WHERE id = ?;

-- name: LatestRunWithEnv :one
SELECT * FROM runs WHERE job_id = ? AND env_hash IS NOT NULL
ORDER BY started_at DESC LIMIT 1;

-- name: LastSuccessWithEnvBefore :one
SELECT * FROM runs
WHERE job_id = ? AND status = 'success' AND env_hash IS NOT NULL AND started_at < ?
ORDER BY started_at DESC LIMIT 1;

-- name: ListRunsOverlapping :many
-- Runs that were running at some point in [from, to): started before to and
-- not ended before from.
SELECT id, job_id, started_at, ended_at, duration_ms, status, exit_code, reason, overlapped_run_id
FROM runs WHERE started_at < sqlc.arg(to_time) AND (ended_at IS NULL OR ended_at >= sqlc.arg(from_time))
ORDER BY started_at;

-- name: LastSuccessBefore :one
SELECT * FROM runs WHERE job_id = ? AND status = 'success' AND started_at < ?
ORDER BY started_at DESC LIMIT 1;

-- name: FailureHistoryBefore :one
-- How many earlier runs of the job failed the same way, and when the first did.
SELECT count(*) AS runs, CAST(coalesce(min(started_at), '') AS TEXT) AS first_seen
FROM runs WHERE job_id = ? AND failure_signature = ? AND started_at < ?;

-- name: ListFailureGroups :many
-- The job's distinct failure signatures, most recent first, with the newest
-- run of each.
SELECT g.failure_signature, count(*) AS runs, CAST(min(g.started_at) AS TEXT) AS first_seen,
    CAST(max(g.started_at) AS TEXT) AS last_seen,
    CAST((SELECT latest.id FROM runs AS latest WHERE latest.job_id = g.job_id
        AND latest.failure_signature = g.failure_signature
        ORDER BY latest.started_at DESC LIMIT 1) AS TEXT) AS latest_run_id
FROM runs AS g WHERE g.job_id = ? AND g.failure_signature IS NOT NULL
GROUP BY g.failure_signature ORDER BY last_seen DESC LIMIT ?;

-- name: ListUnsignedFailures :many
SELECT id, status, exit_code, stdout, stderr, reason FROM runs
WHERE failure_signature IS NULL AND status IN ('failed', 'timeout')
LIMIT ?;

-- name: SetFailureSignature :exec
UPDATE runs SET failure_signature = ? WHERE id = ?;

-- name: SuccessDurationsBefore :many
SELECT duration_ms FROM runs
WHERE job_id = ? AND status = 'success' AND started_at < ? AND duration_ms IS NOT NULL
ORDER BY started_at DESC LIMIT ?;

-- name: SuccessDurationsSince :many
SELECT started_at, duration_ms FROM runs
WHERE job_id = ? AND status = 'success' AND started_at >= ? AND duration_ms IS NOT NULL
ORDER BY started_at;

-- name: ListRunDurations :many
-- The job's newest finished runs, without their output.
SELECT id, started_at, duration_ms, status FROM runs
WHERE job_id = ? AND status NOT IN ('running', 'skipped') AND duration_ms IS NOT NULL
ORDER BY started_at DESC LIMIT ?;

-- name: LatestOwnerlessRunningRun :one
-- The newest heartbeat run still waiting for its end ping.
SELECT * FROM runs WHERE job_id = ? AND status = 'running' AND pid IS NULL
ORDER BY started_at DESC LIMIT 1;

-- name: ListOpenHeartbeatRunsWithLimit :many
-- Heartbeat runs still waiting for their end ping, of jobs with a
-- --max-duration, oldest first.
SELECT sqlc.embed(runs), jobs.max_duration_seconds
FROM runs JOIN jobs ON jobs.id = runs.job_id
WHERE runs.status = 'running' AND runs.pid IS NULL AND jobs.max_duration_seconds IS NOT NULL
  AND (sqlc.narg(job_id) IS NULL OR runs.job_id = sqlc.narg(job_id))
ORDER BY runs.started_at;
