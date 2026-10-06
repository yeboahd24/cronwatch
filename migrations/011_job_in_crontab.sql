-- +goose Up
-- Whether the user's crontab ran the job through cronwatch run when it was
-- last synced. A job that leaves the crontab has its schedule removed, so it
-- is not reported as missed. Jobs in the crontab history were in it once.
ALTER TABLE jobs ADD COLUMN in_crontab INTEGER NOT NULL DEFAULT 0 CHECK (in_crontab IN (0, 1));
UPDATE jobs SET in_crontab = 1
WHERE slug IN (SELECT job_slug FROM crontab_changes WHERE job_slug IS NOT NULL);

-- +goose Down
ALTER TABLE jobs DROP COLUMN in_crontab;
