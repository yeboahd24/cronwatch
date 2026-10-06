-- +goose Up
-- A paused job is not checked for missed runs and raises no alerts, until
-- paused_until if set, or until it is resumed. An archived job is also left
-- off job lists. Both keep their runs.
ALTER TABLE jobs ADD COLUMN paused_at TEXT;
ALTER TABLE jobs ADD COLUMN paused_until TEXT;
ALTER TABLE jobs ADD COLUMN archived_at TEXT;

-- +goose Down
ALTER TABLE jobs DROP COLUMN archived_at;
ALTER TABLE jobs DROP COLUMN paused_until;
ALTER TABLE jobs DROP COLUMN paused_at;
