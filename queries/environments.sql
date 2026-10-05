-- name: InsertEnvironment :exec
INSERT INTO environments (hash, data, created_at) VALUES (?, ?, ?)
ON CONFLICT(hash) DO NOTHING;

-- name: GetEnvironment :one
SELECT data FROM environments WHERE hash = ?;

-- name: DeleteUnusedEnvironments :execrows
DELETE FROM environments
WHERE hash NOT IN (SELECT env_hash FROM runs WHERE env_hash IS NOT NULL);
