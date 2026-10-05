-- name: LatestCrontabSnapshot :one
SELECT * FROM crontab_snapshots ORDER BY taken_at DESC LIMIT 1;

-- name: InsertCrontabSnapshot :exec
INSERT INTO crontab_snapshots (id, taken_at, content_hash, content) VALUES (?, ?, ?, ?);

-- name: InsertCrontabChange :exec
INSERT INTO crontab_changes (id, snapshot_id, taken_at, job_slug, kind, before_text, after_text)
VALUES (?, ?, ?, ?, ?, ?, ?);

-- name: ListCrontabChanges :many
SELECT * FROM crontab_changes ORDER BY taken_at DESC, rowid LIMIT ?;

-- name: ListJobCrontabChanges :many
SELECT * FROM crontab_changes WHERE job_slug = ? ORDER BY taken_at DESC, rowid LIMIT ?;

-- name: ListCrontabChangesSince :many
SELECT * FROM crontab_changes WHERE job_slug IS NOT NULL AND taken_at >= ? ORDER BY taken_at;

-- name: DeleteCrontabSnapshotsBefore :execrows
-- Keeps the newest snapshot, which later changes are compared with.
DELETE FROM crontab_snapshots WHERE crontab_snapshots.taken_at < ?
  AND crontab_snapshots.id != (SELECT newest.id FROM crontab_snapshots AS newest ORDER BY newest.taken_at DESC LIMIT 1);
