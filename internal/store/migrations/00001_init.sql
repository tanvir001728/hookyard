-- +goose Up

-- One outbound call an application asked Hookyard to deliver.
CREATE TABLE requests (
    id                 text        PRIMARY KEY,
    upstream           text        NOT NULL,
    method             text        NOT NULL,
    path               text        NOT NULL,
    headers            jsonb       NOT NULL DEFAULT '{}',
    -- The body exactly as enqueued; json (not jsonb) preserves the original text.
    body               json,
    dedupe_key         text,
    status             text        NOT NULL,
    attempt_count      integer     NOT NULL DEFAULT 0,
    -- Fully resolved retry policy (durations in milliseconds).
    retry              jsonb       NOT NULL,
    timeout_ms         integer     NOT NULL,
    tags               jsonb       NOT NULL DEFAULT '{}',
    deliver_at         timestamptz,
    next_attempt_at    timestamptz,
    -- Set while a worker owns the request; an expired lease means the worker died.
    lease_expires_at   timestamptz,
    last_error_code    text,
    last_error_message text,
    last_status_code   integer,
    created_at         timestamptz NOT NULL DEFAULT now(),
    updated_at         timestamptz NOT NULL DEFAULT now(),
    completed_at       timestamptz,

    CONSTRAINT requests_status_check CHECK (status IN (
        'scheduled', 'pending', 'in_flight', 'failed', 'succeeded', 'dead', 'unknown', 'canceled'
    )),
    CONSTRAINT requests_method_check CHECK (method IN ('GET', 'POST', 'PUT', 'PATCH', 'DELETE')),
    CONSTRAINT requests_attempt_count_check CHECK (attempt_count >= 0)
);

-- Work queue: the scheduler claims due requests in next_attempt_at order.
CREATE INDEX requests_due_idx ON requests (next_attempt_at)
    WHERE status IN ('scheduled', 'pending', 'failed');

-- Crash recovery: find in-flight requests whose lease expired.
CREATE INDEX requests_lease_idx ON requests (lease_expires_at)
    WHERE status = 'in_flight';

-- Explorer filters (IDs are time-sortable, so id DESC is newest first).
CREATE INDEX requests_upstream_id_idx ON requests (upstream, id DESC);
CREATE INDEX requests_status_id_idx ON requests (status, id DESC);
CREATE INDEX requests_dedupe_key_idx ON requests (dedupe_key) WHERE dedupe_key IS NOT NULL;
CREATE INDEX requests_tags_idx ON requests USING gin (tags jsonb_path_ops);

-- Dead-letter queue by upstream and time.
CREATE INDEX requests_dead_idx ON requests (upstream, completed_at) WHERE status = 'dead';

-- Live dedupe keys. A key can be reused once it expires, which a plain unique
-- index on requests could not express.
CREATE TABLE dedupe_keys (
    upstream   text        NOT NULL,
    dedupe_key text        NOT NULL,
    request_id text        NOT NULL REFERENCES requests (id) ON DELETE CASCADE,
    expires_at timestamptz NOT NULL,
    PRIMARY KEY (upstream, dedupe_key)
);

CREATE INDEX dedupe_keys_expires_at_idx ON dedupe_keys (expires_at);
CREATE INDEX dedupe_keys_request_id_idx ON dedupe_keys (request_id);

-- One HTTP try of a request.
CREATE TABLE attempts (
    request_id              text        NOT NULL REFERENCES requests (id) ON DELETE CASCADE,
    number                  integer     NOT NULL,
    started_at              timestamptz NOT NULL,
    duration_ms             integer     NOT NULL,
    outcome                 text        NOT NULL,
    status_code             integer,
    error_code              text,
    error_message           text,
    response_headers        jsonb,
    response_body           text,
    response_body_truncated boolean     NOT NULL DEFAULT false,
    retry_at                timestamptz,

    PRIMARY KEY (request_id, number),
    CONSTRAINT attempts_outcome_check CHECK (outcome IN ('success', 'retryable_failure', 'permanent_failure')),
    CONSTRAINT attempts_number_check CHECK (number >= 1)
);

-- Who did what, for operator actions such as replay and cancel.
CREATE TABLE audit_log (
    id          bigint      GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    at          timestamptz NOT NULL DEFAULT now(),
    actor       text        NOT NULL,
    action      text        NOT NULL,
    target_type text        NOT NULL,
    target_id   text,
    details     jsonb       NOT NULL DEFAULT '{}'
);

CREATE INDEX audit_log_target_idx ON audit_log (target_type, target_id);

-- +goose Down
DROP TABLE audit_log;
DROP TABLE attempts;
DROP TABLE dedupe_keys;
DROP TABLE requests;
