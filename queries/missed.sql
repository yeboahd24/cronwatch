-- name: RecordMissedOccurrence :exec
INSERT OR IGNORE INTO missed_occurrences (id, job_id, expected_at, detected_at)
VALUES (?, ?, ?, ?);

-- name: LatestMissedOccurrence :one
SELECT expected_at FROM missed_occurrences WHERE job_id = ? ORDER BY expected_at DESC LIMIT 1;

-- name: DeleteMissedBefore :execrows
DELETE FROM missed_occurrences WHERE expected_at < ?;
