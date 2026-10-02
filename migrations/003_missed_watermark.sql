-- +goose Up
-- Occurrences at or before this time have been checked for missed runs.
ALTER TABLE jobs ADD COLUMN missed_checked_until TEXT;

-- +goose Down
ALTER TABLE jobs DROP COLUMN missed_checked_until;
