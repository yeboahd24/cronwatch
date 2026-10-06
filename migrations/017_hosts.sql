-- +goose Up
-- Servers that report to this one as a hub. A host is known by the token the
-- hub issued it, stored only as a SHA-256 hash; the name is the hub's own.
-- report is the latest report, as JSON, replaced by each new one.
CREATE TABLE IF NOT EXISTS hosts (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL UNIQUE,
    token_hash TEXT NOT NULL UNIQUE,
    created_at TEXT NOT NULL,
    reported_at TEXT,
    report TEXT
);

-- +goose Down
DROP TABLE IF EXISTS hosts;
