-- +goose Up
-- When a running run's saved output last changed. cronwatch run saves the
-- output so far while the command runs, so a long or stuck job can be
-- looked at before it ends.
ALTER TABLE runs ADD COLUMN output_at TEXT;

-- +goose Down
ALTER TABLE runs DROP COLUMN output_at;
