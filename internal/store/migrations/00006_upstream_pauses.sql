-- +goose Up

-- Upstreams paused by an operator. Deliveries to a paused upstream wait in
-- the queue; the pause survives restarts and can end on its own at "until".
CREATE TABLE upstream_pauses (
    upstream  text        PRIMARY KEY,
    paused_at timestamptz NOT NULL DEFAULT now(),
    until     timestamptz,
    reason    text        NOT NULL DEFAULT '',
    actor     text        NOT NULL
);

-- +goose Down
DROP TABLE upstream_pauses;
