-- +goose Up

-- The classification rule that decided an attempt's outcome, if any.
ALTER TABLE attempts ADD COLUMN classified_by text;

-- +goose Down
ALTER TABLE attempts DROP COLUMN classified_by;
