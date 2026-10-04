# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Changed

- **A POST or PATCH that was sent but got no response is no longer retried by default.** It becomes
  `unknown`, because the vendor may have processed it and a retry could duplicate it (for example a
  payment). Settle it with `POST /v1/requests/{id}/resolve` (or in the dashboard), or replay it.
  Requests with an `Idempotency-Key` header, idempotent methods (GET, PUT, DELETE), failures before
  the request was sent, and upstreams with `on_timeout: retry` are still retried.

### Added

- Signed completion callbacks: set `callback_url` (per request, or a default per upstream) and
  Hookyard POSTs an event to your app when the request succeeds, dies, becomes unknown or is
  canceled. Events are signed following Standard Webhooks with `HOOKYARD_CALLBACK_SECRETS` (rotation
  supported), retried with backoff until your app answers `2xx`, queued durably with the request, and
  shown on the dashboard's request page. `on_result` carries a routing key, `callbacks.allow`
  restricts where callbacks may go, and `GET /v1/requests/{id}/callbacks` and
  `POST …/callbacks/{callback_id}/retry` inspect and resend them.
- `POST /v1/requests/{id}/resolve` to settle `unknown` requests, the `on_timeout` upstream setting,
  and "Mark as delivered / Mark as failed" on the dashboard's request page.

- Response classification rules per upstream (`classify`): match on status code and a value in the
  JSON body to decide whether a response is a success, a retryable failure or a permanent failure,
  for vendors that report errors inside `200 OK`. Attempts show which rule decided, and such failures
  use the new `classified_failure` error code.
- Pause and resume upstreams (`POST /v1/upstreams/{name}/pause` and `/resume`, or the SDK), with a
  reason and an optional end time. Pauses survive restarts and are audit-logged. `GET /v1/upstreams`
  now includes each upstream's live state, `GET /v1/upstreams/{name}/events` its history, and
  Prometheus gauges for breaker and pause state.
- A circuit breaker per upstream, on by default (`breaker`). When a vendor keeps failing, deliveries
  to it pause instead of burning retries; after a cooldown a few probe requests decide whether to
  resume. Paused time doesn't count against a request's `max_age`, and every transition is recorded
  in the upstream's history.
- Per-upstream rate limits (`rate_limit`, `burst`) and concurrency caps (`max_concurrency`). Requests
  over the limits wait in the queue without using up attempts, other upstreams are unaffected, and a
  `429` with `Retry-After` pauses all deliveries to that upstream until then. Limits appear in
  `GET /v1/upstreams` and the SDK.

## [0.1.0] - 2026-10-04

The first release: Hookyard delivers outbound API calls reliably, and you can watch and manage
every delivery from a built-in dashboard, the API or the TypeScript SDK.

### Delivery

- Async delivery engine: a bounded worker pool claims due requests with `FOR UPDATE SKIP LOCKED`,
  delivers them over HTTP and records every attempt. Leases with fencing recover requests from
  crashed processes without letting stale workers overwrite results; in-flight deliveries finish on
  graceful shutdown.
- Retry policies: exponential backoff with full jitter, `Retry-After` on `429`/`503`, permanent
  failures for other `4xx` and `3xx`, `max_attempts` and `max_age`. Presets `none`, `quick`,
  `standard`, `patient`, or a custom policy per upstream or per request.
- Dead-letter queue: requests that give up are kept with the reason, and can be replayed one at a
  time or in bulk with a fresh retry budget.
- Explicit deduplication with `dedupe_key`, scheduled delivery with `deliver_at`, and
  `Hookyard-Request-Id` / `Hookyard-Attempt` headers on every delivery. Redirects are never followed.

### API

- `POST /v1/requests` with bearer-token authentication (`HOOKYARD_API_TOKENS`) and per-field
  validation errors, including "did you mean" suggestions for unknown upstreams.
- Management API: list and filter requests, attempts, replay, cancel, dead-letter summary and bulk
  replay with `dry_run`, upstreams, and delivery stats (`/v1/stats/overview`, `/v1/stats/timeseries`).
  Replay and cancel are recorded in an audit log.
- OpenAPI 3.1 specification (`api/openapi.yaml`), linted in CI.

### Dashboard

- Embedded in the binary and served at `/`: sign-in with an API token (exchanged for a signed,
  HTTP-only session cookie), CSRF protection, a strict content security policy, light and dark
  themes, and a layout that works on phones.
- Overview with per-upstream health, success rate, latency percentiles, backlog and dead letters,
  plus a deliveries chart with a table view.
- Requests explorer and request detail with the attempt timeline, live updates, replay and cancel,
  copy as curl or SDK call, and redaction of sensitive header values.
- Dead-letter page with grouped failures and bulk replay that confirms the exact count first.

### TypeScript SDK

- `@hookyard/sdk`: `hy.to(upstream).post(...)` with typed options, `job.result()`, the full
  management API, typed errors, transport retries that never create duplicates, and an in-memory
  fake in `@hookyard/sdk/testing`. Zero runtime dependencies.

### Operations

- Configuration in `hookyard.yaml` with `${ENV_VAR}` interpolation, strict validation with line
  numbers, and `hookyard validate`.
- Postgres storage with embedded, lock-protected migrations (`hookyard migrate [up|status]`).
- Per-upstream metrics stored in Postgres, an optional Prometheus endpoint (`HOOKYARD_METRICS`), and
  retention of finished requests (`HOOKYARD_REQUEST_RETENTION`).
- Docker image (distroless, non-root, about 20 MB) with a `hookyard healthcheck` command, and a
  one-command quickstart with Docker Compose.
- `flakyvendor`, a deliberately unreliable API for tests and demos.

### Quality

- Unit and integration tests against real Postgres, SDK contract tests, a Docker quickstart check,
  end-to-end tests of the real binaries (including a SIGKILL crash and recovery), and dashboard
  browser tests on desktop and phone, all required on every pull request.

[Unreleased]: https://github.com/tanvir001728/hookyard/compare/v0.1.0...HEAD
[0.1.0]: https://github.com/tanvir001728/hookyard/releases/tag/v0.1.0
