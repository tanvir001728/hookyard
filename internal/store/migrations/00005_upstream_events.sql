-- +goose Up

-- What happened to an upstream over time: circuit breaker transitions now,
-- manual pauses and resumes later. Shown in the dashboard's upstream history.
CREATE TABLE upstream_events (
    id       bigint      GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    upstream text        NOT NULL,
    at       timestamptz NOT NULL DEFAULT now(),
    kind     text        NOT NULL,
    reason   text        NOT NULL DEFAULT '',
    actor    text        NOT NULL DEFAULT 'hookyard',
    details  jsonb       NOT NULL DEFAULT '{}'
);

CREATE INDEX upstream_events_upstream_idx ON upstream_events (upstream, id DESC);

-- +goose Down
DROP TABLE upstream_events;
