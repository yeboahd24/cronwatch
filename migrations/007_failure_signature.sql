-- +goose Up
-- Groups failed runs that failed the same way; NULL for runs that did not
-- fail and for failures not yet backfilled.
ALTER TABLE runs ADD COLUMN failure_signature TEXT;
CREATE INDEX idx_runs_job_signature ON runs(job_id, failure_signature);

-- +goose Down
DROP INDEX IF EXISTS idx_runs_job_signature;
ALTER TABLE runs DROP COLUMN failure_signature;
