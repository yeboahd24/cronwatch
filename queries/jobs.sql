-- name: UpsertJob :exec
INSERT INTO jobs (id, slug, name, command, schedule, grace_seconds, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(slug) DO UPDATE SET
    name = excluded.name,
    command = excluded.command,
    schedule = excluded.schedule,
    grace_seconds = excluded.grace_seconds,
    updated_at = excluded.updated_at;

-- name: GetJob :one
SELECT * FROM jobs WHERE id = ?;

-- name: GetJobBySlug :one
SELECT * FROM jobs WHERE slug = ?;

-- name: ListJobs :many
SELECT * FROM jobs ORDER BY name COLLATE NOCASE;
