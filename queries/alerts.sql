-- name: InsertAlert :exec
INSERT INTO alerts (id, job_id, run_id, event, hook, environ, created_at, next_attempt_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?);

-- name: ListPendingAlerts :many
-- Oldest first within each job, so a job's alerts are delivered in order.
SELECT sqlc.embed(alerts), jobs.name AS job_name, jobs.on_failure, jobs.on_recover
FROM alerts JOIN jobs ON jobs.id = alerts.job_id
WHERE alerts.status = 'pending' AND (sqlc.narg(job_id) IS NULL OR alerts.job_id = sqlc.narg(job_id))
ORDER BY alerts.job_id, alerts.created_at, alerts.rowid;

-- name: ClaimAlert :execrows
-- Takes a due alert for one attempt. next_attempt_at becomes a lease, so
-- another process does not attempt it too, and an attempt cut short by a
-- crash is retried once the lease runs out.
UPDATE alerts SET attempts = attempts + 1, last_attempt_at = sqlc.arg(now), next_attempt_at = sqlc.arg(lease_until)
WHERE id = sqlc.arg(id) AND status = 'pending' AND next_attempt_at <= sqlc.arg(now);

-- name: FinishAlertAttempt :exec
UPDATE alerts SET status = ?, next_attempt_at = ?, last_error = ?, last_output = ? WHERE id = ?;

-- name: CancelAlert :exec
UPDATE alerts SET status = 'cancelled', next_attempt_at = NULL, last_error = ? WHERE id = ? AND status = 'pending';

-- name: ListJobAlerts :many
SELECT * FROM alerts WHERE job_id = ? ORDER BY created_at DESC, rowid DESC LIMIT ?;

-- name: ListRunAlerts :many
SELECT * FROM alerts WHERE run_id = ? ORDER BY created_at, rowid;

-- name: CountUndeliveredAlerts :many
-- Alerts that failed at least once and are not yet delivered, by job.
SELECT job_id, count(*) AS alerts FROM alerts
WHERE status = 'undelivered' OR (status = 'pending' AND last_error != '')
GROUP BY job_id;

-- name: DeleteAlertsBefore :execrows
DELETE FROM alerts WHERE created_at < ? AND status != 'pending';
