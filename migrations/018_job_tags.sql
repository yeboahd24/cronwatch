-- +goose Up
-- A job's tags, sorted and separated by single spaces; '' for none. Tags
-- are lowercase letters, digits and hyphens, like slugs, so they never hold
-- a space.
ALTER TABLE jobs ADD COLUMN tags TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE jobs DROP COLUMN tags;
