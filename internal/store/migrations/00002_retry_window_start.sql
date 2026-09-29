-- +goose Up

-- Anchors a request's max_age. It starts when the request first becomes due
-- (creation, or deliver_at if later) and is reset when a request is replayed,
-- so replaying an old request gets a fresh retry budget.
ALTER TABLE requests ADD COLUMN retry_window_start timestamptz;
UPDATE requests SET retry_window_start = GREATEST(created_at, COALESCE(deliver_at, created_at));
ALTER TABLE requests ALTER COLUMN retry_window_start SET NOT NULL;

-- +goose Down
ALTER TABLE requests DROP COLUMN retry_window_start;
