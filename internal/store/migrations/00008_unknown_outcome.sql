-- +goose Up

-- An attempt whose outcome is unknown: the request was sent, but no response
-- came back.
ALTER TABLE attempts DROP CONSTRAINT attempts_outcome_check;
ALTER TABLE attempts ADD CONSTRAINT attempts_outcome_check
    CHECK (outcome IN ('success', 'retryable_failure', 'permanent_failure', 'unknown'));

-- +goose Down
ALTER TABLE attempts DROP CONSTRAINT attempts_outcome_check;
ALTER TABLE attempts ADD CONSTRAINT attempts_outcome_check
    CHECK (outcome IN ('success', 'retryable_failure', 'permanent_failure'));
