-- name: RecordMissedOccurrence :execrows
INSERT OR IGNORE INTO missed_occurrences (id, job_id, expected_at, detected_at)
VALUES (?, ?, ?, ?);

-- name: LatestMissedOccurrence :one
SELECT expected_at FROM missed_occurrences WHERE job_id = ? ORDER BY expected_at DESC LIMIT 1;

-- name: LatestMissedOccurrenceBefore :one
SELECT expected_at FROM missed_occurrences WHERE job_id = ? AND expected_at < ?
ORDER BY expected_at DESC LIMIT 1;

-- name: CountMissedSince :many
SELECT job_id, count(*) AS missed
FROM missed_occurrences WHERE expected_at >= ? GROUP BY job_id;

-- name: DeleteMissedBefore :execrows
DELETE FROM missed_occurrences WHERE expected_at < ?;

-- name: ListMissedSince :many
SELECT job_id, expected_at FROM missed_occurrences WHERE expected_at >= ? ORDER BY expected_at;
