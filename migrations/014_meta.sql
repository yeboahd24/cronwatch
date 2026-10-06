-- +goose Up
-- Facts about the installation as a whole, such as when missed runs were
-- last checked.
CREATE TABLE IF NOT EXISTS meta (
    key TEXT PRIMARY KEY,
    value TEXT NOT NULL
);

-- +goose Down
DROP TABLE IF EXISTS meta;
