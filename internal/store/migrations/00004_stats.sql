-- +goose Up

-- Per-upstream delivery metrics in one-minute buckets. Rows are upserted
-- additively, so several Hookyard instances can write to the same bucket.
CREATE TABLE stats_1m (
    bucket          timestamptz NOT NULL,
    upstream        text        NOT NULL,
    attempts        bigint      NOT NULL DEFAULT 0,
    failed_attempts bigint      NOT NULL DEFAULT 0,
    succeeded       bigint      NOT NULL DEFAULT 0,
    dead            bigint      NOT NULL DEFAULT 0,
    latency_sum_ms  bigint      NOT NULL DEFAULT 0,
    PRIMARY KEY (bucket, upstream)
);

-- Attempt latency histogram: how many attempts in the bucket took at most
-- le_ms milliseconds (and more than the previous boundary).
CREATE TABLE stats_1m_latency (
    bucket   timestamptz NOT NULL,
    upstream text        NOT NULL,
    le_ms    integer     NOT NULL,
    count    bigint      NOT NULL,
    PRIMARY KEY (bucket, upstream, le_ms)
);

-- Retention deletes finished requests by age.
CREATE INDEX requests_completed_at_idx ON requests (completed_at) WHERE completed_at IS NOT NULL;

-- +goose Down
DROP INDEX requests_completed_at_idx;
DROP TABLE stats_1m_latency;
DROP TABLE stats_1m;
