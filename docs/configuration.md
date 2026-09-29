# Configuration

Hookyard reads two kinds of settings:

- **Process settings** (listen address, database, logging) come from `HOOKYARD_*` environment
  variables or command-line flags.
- **Upstreams** (the third-party APIs Hookyard delivers to) come from a YAML file, `hookyard.yaml`.

## Environment variables

| Variable | Flag | Default | Description |
| --- | --- | --- | --- |
| `HOOKYARD_DATABASE_URL` | `-database-url` | *(required)* | Postgres connection string |
| `HOOKYARD_API_TOKENS` | | *(required)* | Comma-separated API tokens, each `secret` or `name:secret` (see below) |
| `HOOKYARD_CONFIG` | `-config` | `hookyard.yaml` if present | Path to the config file |
| `HOOKYARD_ADDR` | `-addr` | `:8080` | HTTP listen address |
| `HOOKYARD_LOG_LEVEL` | `-log-level` | `info` | `debug`, `info`, `warn` or `error` |
| `HOOKYARD_LOG_FORMAT` | `-log-format` | `text` | `text` or `json` |
| `HOOKYARD_AUTO_MIGRATE` | `-auto-migrate` | `true` | Apply database migrations on startup |
| `HOOKYARD_SHUTDOWN_TIMEOUT` | `-shutdown-timeout` | `30s` | Graceful shutdown limit |
| `HOOKYARD_MAX_BODY_BYTES` | | `1048576` | Maximum API request body size (1 KiB to 64 MiB) |
| `HOOKYARD_WORKERS` | | `32` | Maximum concurrent deliveries (1 to 1024) |
| `HOOKYARD_POLL_INTERVAL` | | `1s` | How often the queue is checked when idle. New requests are picked up immediately. |
| `HOOKYARD_REQUEST_RETENTION` | | `720h` | How long finished requests and their attempts are kept (`0` keeps them forever) |
| `HOOKYARD_METRICS` | | `false` | Serve Prometheus metrics at `/metrics` |

Hookyard needs **PostgreSQL 14 or newer**.

### Data retention

A background job runs every 10 minutes and deletes:

- finished requests (succeeded, dead, canceled) older than `HOOKYARD_REQUEST_RETENTION`, with their
  attempts; waiting and in-flight requests are never deleted
- metric rollups older than 30 days
- expired dedupe keys

### Prometheus metrics

With `HOOKYARD_METRICS=true`, `/metrics` serves (unauthenticated, so keep it on a private network):

| Metric | Type | Labels |
| --- | --- | --- |
| `hookyard_attempts_total` | counter | `upstream` |
| `hookyard_attempts_failed_total` | counter | `upstream` |
| `hookyard_requests_succeeded_total` | counter | `upstream` |
| `hookyard_requests_dead_total` | counter | `upstream` |
| `hookyard_attempt_duration_seconds` | histogram | `upstream` |
| `hookyard_queue_waiting` | gauge | `upstream` |
| `hookyard_dlq_size` | gauge | `upstream` |

The dashboard and `/v1/stats` don't need Prometheus: they read per-minute rollups that Hookyard keeps in
Postgres.

### API tokens

Every `/v1` call needs `Authorization: Bearer <token>`. Hookyard refuses to start without at least
one token, because an open API would let anyone send requests with your vendor credentials.

```sh
HOOKYARD_API_TOKENS="orders:$(openssl rand -hex 32),billing:$(openssl rand -hex 32)"
```

The name before `:` identifies the caller in logs and the audit log. Unnamed tokens are called
`token-1`, `token-2`, and so on. Tokens must be at least 16 characters. To rotate a token, add the new
one, deploy, move clients over, then remove the old one.

## The config file

See [`hookyard.example.yaml`](../hookyard.example.yaml) for an annotated example. Check a file
before deploying it:

```sh
hookyard validate -config hookyard.yaml
```

Unknown keys, invalid values and unset environment variables are all reported with their line
numbers. Hookyard refuses to start with an invalid file.

### `defaults`

| Key | Default | Description |
| --- | --- | --- |
| `timeout` | `30s` | Per-attempt timeout, at most `10m` |
| `retry` | `standard` | Retry policy (see below) |
| `dedupe_window` | `24h` | How long a `dedupe_key` is remembered, at most `720h` |

### `upstreams.<name>`

Names use lowercase letters, digits, `-` and `_`. Applications refer to upstreams by name.

| Key | Required | Description |
| --- | --- | --- |
| `base_url` | yes | Absolute `http`/`https` URL. Request paths are appended to it. No query string or credentials. |
| `timeout` | no | Overrides `defaults.timeout` |
| `retry` | no | Overrides `defaults.retry` |
| `dedupe_window` | no | Overrides `defaults.dedupe_window` |
| `headers` | no | Headers added to every request, typically credentials. Values are never returned by the API. `Host`, `Content-Length` and hop-by-hop headers can't be set. |

### Retry policies

`retry` is either a preset name or an object:

| Preset | Attempts | Initial interval | Max interval | Max age |
| --- | --- | --- | --- | --- |
| `none` | 1 | – | – | – |
| `quick` | 5 | 1s | 30s | 10m |
| `standard` | 10 | 5s | 10m | 24h |
| `patient` | 25 | 30s | 1h | 72h |

```yaml
retry:
  preset: patient        # optional starting point
  max_attempts: 20       # 1–100, including the first attempt
  initial_interval: 10s
  max_interval: 30m
  multiplier: 2          # 1–10
  max_age: 48h           # give up after this long since the request was created
```

Fields you don't set come from `preset` if it's given, and otherwise from the defaults. The same
format is accepted per request in the API, so a request can override its upstream's policy.

#### How retries behave

- **What is retried:** network errors, timeouts, `408`, `425`, `429` and `5xx`. Other `4xx` responses
  and `3xx` redirects (which are never followed) move the request straight to the dead-letter queue,
  because retrying the same request can't succeed.
- **When:** after attempt *n* the delay is random between 0 and
  `min(max_interval, initial_interval × multiplier^(n-1))` ("full jitter"), so many failed requests
  don't all retry at the same moment. A `Retry-After` header on a `429` or `503` replaces the
  computed delay.
- **Until when:** a request goes to the dead-letter queue after `max_attempts`, or as soon as its next
  attempt would fall outside `max_age`. `max_age` starts when the request becomes due (its creation,
  or `deliver_at` for scheduled requests) and starts over when a request is replayed.

### Environment variable references

String values can reference environment variables, which keeps secrets out of the file:

| Syntax | Meaning |
| --- | --- |
| `${NAME}` | Value of `NAME`; startup fails if it is unset or empty |
| `${NAME:-fallback}` | Value of `NAME`, or `fallback` if it is unset or empty |
| `$$` | A literal `$` |
