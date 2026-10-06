-- name: CreateHost :exec
INSERT INTO hosts (id, name, token_hash, created_at) VALUES (?, ?, ?, ?);

-- name: SetHostToken :execrows
UPDATE hosts SET token_hash = ? WHERE name = ?;

-- name: DeleteHost :execrows
DELETE FROM hosts WHERE name = ?;

-- name: GetHostByName :one
SELECT * FROM hosts WHERE name = ?;

-- name: GetHostByTokenHash :one
SELECT * FROM hosts WHERE token_hash = ?;

-- name: ListHosts :many
SELECT * FROM hosts ORDER BY name COLLATE NOCASE;

-- name: SaveHostReport :exec
UPDATE hosts SET reported_at = ?, report = ? WHERE id = ?;
