-- +goose Up

-- The attempt count at the last replay. Attempts are numbered for the whole
-- life of a request, but a replay grants a fresh retry budget, so the retry
-- policy counts attempts from this base.
ALTER TABLE requests ADD COLUMN retry_attempt_base integer NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE requests DROP COLUMN retry_attempt_base;
