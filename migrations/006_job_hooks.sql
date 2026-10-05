-- +goose Up
-- Commands run when a job starts failing and when it recovers.
ALTER TABLE jobs ADD COLUMN on_failure TEXT;
ALTER TABLE jobs ADD COLUMN on_recover TEXT;

-- +goose Down
ALTER TABLE jobs DROP COLUMN on_recover;
ALTER TABLE jobs DROP COLUMN on_failure;
