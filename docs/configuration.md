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
| `HOOKYARD_LEASE_MARGIN` | | `30s` | Advanced: added to a request's timeout to form a worker's lease. If a Hookyard process dies mid-delivery, the request is delivered again after its lease expires. |
| `HOOKYARD_REQUEST_RETENTION` | | `720h` | How long finished requests and their attempts are kept (`0` keeps them forever) |
| `HOOKYARD_METRICS` | | `false` | Serve Prometheus metrics at `/metrics` |
| `HOOKYARD_DASHBOARD` | | `true` | Serve the web dashboard at `/` |
| `HOOKYARD_CALLBACK_SECRETS` | | none | Comma-separated `whsec_…` secrets that sign completion callbacks, newest first. Callbacks are off without one (see below). |

Hookyard needs **PostgreSQL 14 or newer**.

### Dashboard sign-in

The dashboard at `/` asks for one of the API tokens. Signing in exchanges it for an HTTP-only,
`SameSite=Strict` session cookie valid for 12 hours, so the token is never stored in the browser.
Pasting a whole `name:secret` entry also works. Removing or rotating a token signs out its sessions.

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
| `hookyard_upstream_breaker_open` | gauge (1 open, 0.5 half-open, 0 closed) | `upstream` |
| `hookyard_upstream_paused` | gauge (1 while paused) | `upstream` |

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

## Running with Docker

The image runs `hookyard serve` as a non-root user and listens on port 8080. Mount your config file
and pass settings as environment variables:

```sh
docker run -p 8080:8080 \
  -v $PWD/hookyard.yaml:/etc/hookyard/hookyard.yaml:ro \
  -e HOOKYARD_CONFIG=/etc/hookyard/hookyard.yaml \
  -e HOOKYARD_DATABASE_URL=postgres://... \
  -e HOOKYARD_API_TOKENS="orders:$(openssl rand -hex 32)" \
  hookyard:local
```

Other commands run the same way, for example `docker run hookyard:local migrate status` or
`docker run hookyard:local validate -config /etc/hookyard/hookyard.yaml`. The image's health check
calls `hookyard healthcheck`, which exits non-zero unless `/readyz` reports ready.

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
| `rate_limit` | none | Default rate limit for every upstream (see below) |
| `max_concurrency` | none | Default concurrency cap for every upstream |
| `breaker` | on | Default circuit breaker settings, or `off` (see below) |
| `callback_url` | none | Default URL for completion callbacks (see below) |

### `upstreams.<name>`

Names use lowercase letters, digits, `-` and `_`. Applications refer to upstreams by name.

| Key | Required | Description |
| --- | --- | --- |
| `base_url` | yes | Absolute `http`/`https` URL. Request paths are appended to it. No query string or credentials. |
| `timeout` | no | Overrides `defaults.timeout` |
| `retry` | no | Overrides `defaults.retry` |
| `dedupe_window` | no | Overrides `defaults.dedupe_window` |
| `rate_limit` | no | Sustained rate such as `10/s`, `600/m` or `3600/h` |
| `burst` | no | Requests sent at once after a quiet period (default: one second's worth, at least 1) |
| `max_concurrency` | no | Maximum deliveries in flight to this upstream (1–1024) |
| `breaker` | no | Circuit breaker settings, or `off` |
| `classify` | no | Rules that decide how responses count (see below) |
| `on_timeout` | no | `unknown` (default) or `retry`: what happens when a POST or PATCH was sent but no response arrived (see below) |
| `callback_url` | no | Overrides `defaults.callback_url`; `""` turns callbacks off for this upstream |
| `headers` | no | Headers added to every request, typically credentials. Values are never returned by the API. `Host`, `Content-Length` and hop-by-hop headers can't be set. |

### Rate limits and concurrency

Respect a vendor's published limits, and keep one slow vendor from occupying every worker:

```yaml
upstreams:
  courier-x:
    base_url: https://api.courier-x.example
    rate_limit: 600/m        # a token bucket: sustained 10 per second...
    burst: 20                # ...with up to 20 at once after a quiet period
    max_concurrency: 4       # never more than 4 deliveries in flight
```

Requests over the limits **wait in the queue** (they stay `pending` and don't use up attempts); other
upstreams are unaffected. When an upstream answers `429` with a `Retry-After` header, Hookyard pauses
**all** deliveries to it until then, not just the request that got the `429`.

Limits apply per Hookyard instance. If you run several instances, divide the vendor's limit between
them.

### Circuit breaker

When a vendor is down, retrying every request against it wastes attempts and floods it as it recovers.
Each upstream has a circuit breaker, **on by default**:

- **Closed** (normal): outcomes are counted over a sliding `window`. The breaker **opens** when at
  least `min_calls` calls were seen and the failure rate reaches `failure_rate`, or after
  `consecutive_failures` failures in a row.
- **Open**: deliveries to that upstream **pause**. Requests wait in the queue: they don't use up
  attempts, and the paused time doesn't count against their `max_age`.
- **Half-open**: after `cooldown`, up to `probes` requests are sent. If they all succeed the breaker
  closes and the queue drains; if one fails it opens again.

Only retryable failures count (timeouts, connection errors, `429`, `5xx`). A vendor rejecting a bad
request with `400` is healthy, so it doesn't trip the breaker.

```yaml
upstreams:
  courier-x:
    base_url: https://api.courier-x.example
    breaker:                  # all fields optional; defaults shown
      failure_rate: 0.5
      min_calls: 20
      window: 1m
      consecutive_failures: 5
      cooldown: 30s
      probes: 3
  internal-api:
    base_url: http://internal.example
    breaker: off
```

Every transition is logged and kept in the upstream's history. Breaker state is per Hookyard
instance.

### Classifying responses

By default `2xx` is a success, network errors, timeouts, `408`, `425`, `429` and `5xx` are retried,
and other responses are permanent failures. Many partner APIs don't follow that: they answer `200 OK`
with an error in the body, or use `4xx` for temporary problems. Add `classify` rules to an upstream;
**the first rule that matches decides**, and responses no rule matches use the defaults.

```yaml
upstreams:
  courier-x:
    base_url: https://api.courier-x.example
    classify:
      - name: fake success          # optional; shown on attempts in the API and dashboard
        status: 200
        body: status                # dot path into the JSON body, e.g. error.code or items.0.state
        equals: FAILED
        then: retry                 # success | retry | fail
      - status: 409                 # "already exists" means the earlier attempt went through
        then: success
      - status: 4xx
        body: error.code
        in: [busy, try_later]
        then: retry
```

| Key | Meaning |
| --- | --- |
| `status` | A code (`200`), a class (`4xx`), a range (`500-599`) or a list of those |
| `body` | A dot path into the JSON response body |
| `equals` / `not_equals` | The value at `body` must (not) equal this string, number, boolean or null |
| `in` | The value at `body` must be one of these |
| `exists` | `true` if `body` must exist, `false` if it must not |
| `then` | `success`, `retry` (retryable failure) or `fail` (permanent failure) |

A rule needs `status`, `body` or both. With `body`, it needs exactly one of `equals`, `not_equals`,
`in` or `exists`, and it only matches JSON responses. When a rule turns a `2xx` into a failure, the
attempt's error code is `classified_failure`. The rule's outcome also counts for the circuit breaker.

### When Hookyard can't tell whether a request went through

If a request was **fully sent** but no response came back (a timeout, or the connection dropping
after sending), the upstream may or may not have processed it. Repeating a POST could then charge a
customer twice. So for **POST and PATCH**, Hookyard marks such a request **`unknown`** instead of
retrying it, and leaves the decision to a person or your application:

- `POST /v1/requests/{id}/resolve` with `{"outcome": "succeeded"}` after confirming with the vendor,
  or `{"outcome": "dead"}` if it didn't go through (it then moves to the dead-letter queue);
- or replay it if you're sure a duplicate is harmless.

The dashboard's request page shows a "Mark as delivered / Mark as failed" choice for these. Failures
**before** the request was fully sent (DNS errors, refused connections) are always retried, and so
are **GET, PUT and DELETE**, which are idempotent by definition.

To retry ambiguous POST/PATCH requests anyway:

- send an `Idempotency-Key` header (in the request or the upstream's `headers`) if the vendor
  deduplicates by it, or
- set `on_timeout: retry` on the upstream.

### Completion callbacks

Instead of polling, your app can be told when a request finishes. Set `callback_url` when you enqueue
(or as a default in the config file), and Hookyard POSTs an event to it when the request becomes
`succeeded`, `dead`, `unknown` or `canceled`:

```json
{
  "type": "request.succeeded",
  "timestamp": "2026-10-04T09:12:03Z",
  "data": {
    "request_id": "req_01J9…", "upstream": "courier-x", "method": "POST", "path": "/shipments",
    "status": "succeeded", "on_result": "order.shipment", "tags": {"app": "orders"},
    "dedupe_key": "order-123-create-shipment", "attempt_count": 2, "last_error": null,
    "response": {"status_code": 201, "headers": {"Content-Type": "application/json"}, "body": "{…}", "body_truncated": false},
    "completed_at": "2026-10-04T09:12:03Z"
  }
}
```

`on_result` is any key you choose when enqueueing, to route the event to the right handler.

**Signatures.** Callbacks are signed following [Standard Webhooks](https://www.standardwebhooks.com/),
so any of its libraries can verify them: the `webhook-id`, `webhook-timestamp` and `webhook-signature`
headers carry an HMAC-SHA256 over `{id}.{timestamp}.{body}`. Generate a secret and give it to both
Hookyard and your app:

```sh
echo "whsec_$(openssl rand -base64 32)"
HOOKYARD_CALLBACK_SECRETS=whsec_new,whsec_old   # rotation: sign with both until every app has the new one
```

Callbacks are off until a secret is set: Hookyard rejects a `callback_url` (and refuses to start
with one in the config file) without it.

**Delivery.** Answer with any `2xx`. Anything else, or no answer within the timeout, is retried with
backoff (5s doubling up to an hour, twelve attempts by default, about six hours). Callbacks are queued
in the same transaction that finishes the request, so they survive restarts, and are delivered **at
least once**: deduplicate on `webhook-id`, which is the same on every attempt. Redirects aren't
followed. `GET /v1/requests/{id}/callbacks` and the dashboard's request page show each callback's
status; a `failed` one can be sent again with `POST /v1/requests/{id}/callbacks/{callback_id}/retry`.

**Restricting where callbacks go.** An API client could otherwise make Hookyard POST to any address
it can reach. List the allowed URLs to prevent that:

```yaml
callbacks:
  allow:
    - http://orders-svc:3000/hooks/          # this host and port, paths under /hooks/
    - https://*.internal.example.com         # any subdomain, any path
  timeout: 10s                               # per attempt, at most 1m
  max_attempts: 12                           # 1–50
defaults:
  callback_url: http://orders-svc:3000/hooks/hookyard
```

### Pausing an upstream

During a vendor's maintenance window, or while you investigate an incident, pause deliveries to an
upstream. Requests wait in the queue with the same guarantees as an open breaker. A pause is stored in
the database, so it survives restarts and applies to every Hookyard instance:

```sh
curl -X POST localhost:8080/v1/upstreams/courier-x/pause \
  -H "Authorization: Bearer $HOOKYARD_TOKEN" \
  -d '{"reason":"vendor maintenance","duration":"2h"}'      # or "until": "2026-10-05T03:00:00Z"

curl -X POST localhost:8080/v1/upstreams/courier-x/resume -H "Authorization: Bearer $HOOKYARD_TOKEN"
```

With the SDK: `hy.upstreams.pause("courier-x", { reason, duration: "2h" })` and
`hy.upstreams.resume("courier-x")`. A pause with an end time resumes on its own. `GET /v1/upstreams`
shows each upstream's live state (breaker, pause, in-flight deliveries, rate-limit tokens), and
`GET /v1/upstreams/{name}/events` its history. Pauses and resumes are recorded in the audit log.

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
