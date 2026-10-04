-- +goose Up

-- Where to report a request's outcome, and the key the app routes it by.
ALTER TABLE requests ADD COLUMN callback_url text, ADD COLUMN on_result text;

-- One completion event for an application, delivered with its own retries.
CREATE TABLE callbacks (
    id                  text        PRIMARY KEY,
    request_id          text        NOT NULL REFERENCES requests (id) ON DELETE CASCADE,
    url                 text        NOT NULL,
    event_type          text        NOT NULL,
    -- The request's status and attempt count when it finished. The request
    -- can change later (a replay), but the event describes this moment.
    request_status      text        NOT NULL,
    request_attempts    integer     NOT NULL,
    status              text        NOT NULL DEFAULT 'pending',
    -- The signed body, fixed on the first attempt so retries send the same
    -- event.
    payload             text,
    attempt_count       integer     NOT NULL DEFAULT 0,
    next_attempt_at     timestamptz NOT NULL DEFAULT now(),
    lease_expires_at    timestamptz,
    last_status_code    integer,
    last_error          text,
    last_attempt_at     timestamptz,
    created_at          timestamptz NOT NULL DEFAULT now(),
    delivered_at        timestamptz,

    CONSTRAINT callbacks_status_check CHECK (status IN ('pending', 'delivering', 'delivered', 'failed'))
);

CREATE INDEX callbacks_due_idx ON callbacks (next_attempt_at) WHERE status = 'pending';
CREATE INDEX callbacks_lease_idx ON callbacks (lease_expires_at) WHERE status = 'delivering';
CREATE INDEX callbacks_request_id_idx ON callbacks (request_id, created_at);

-- Queue a callback in the same transaction that finishes a request, whatever
-- finished it (a delivery, cancel, resolve or the DLQ).
-- +goose StatementBegin
CREATE FUNCTION hookyard_enqueue_callback() RETURNS trigger AS $$
BEGIN
    INSERT INTO callbacks (id, request_id, url, event_type, request_status, request_attempts)
    VALUES ('evt_' || replace(gen_random_uuid()::text, '-', ''), NEW.id, NEW.callback_url,
            'request.' || NEW.status, NEW.status, NEW.attempt_count);
    RETURN NULL;
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd

CREATE TRIGGER requests_enqueue_callback
    AFTER UPDATE OF status ON requests
    FOR EACH ROW
    WHEN (NEW.callback_url IS NOT NULL
          AND NEW.status IN ('succeeded', 'dead', 'unknown', 'canceled')
          AND OLD.status IS DISTINCT FROM NEW.status)
    EXECUTE FUNCTION hookyard_enqueue_callback();

-- +goose Down
DROP TRIGGER requests_enqueue_callback ON requests;
DROP FUNCTION hookyard_enqueue_callback();
DROP TABLE callbacks;
ALTER TABLE requests DROP COLUMN callback_url, DROP COLUMN on_result;
