-- +goose Up
-- Run environments, stored once per distinct environment.
CREATE TABLE IF NOT EXISTS environments (
    hash TEXT PRIMARY KEY,
    data TEXT NOT NULL,
    created_at TEXT NOT NULL
);
ALTER TABLE runs ADD COLUMN env_hash TEXT;

-- +goose Down
ALTER TABLE runs DROP COLUMN env_hash;
DROP TABLE IF EXISTS environments;
