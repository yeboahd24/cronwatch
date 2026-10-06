-- +goose Up
-- How long a heartbeat run may wait for its end ping before it is recorded
-- as timed out. NULL means no limit.
ALTER TABLE jobs ADD COLUMN max_duration_seconds INTEGER;

-- +goose Down
ALTER TABLE jobs DROP COLUMN max_duration_seconds;
