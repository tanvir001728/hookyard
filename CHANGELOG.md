# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- TypeScript SDK `@hookyard/sdk` (`sdk/typescript`): `hy.to(upstream).post(...)` with typed options, `job.result()`, the full management API, typed errors, transport retries that never create duplicates, and an in-memory fake in `@hookyard/sdk/testing`. Zero runtime dependencies; wire types are generated from the OpenAPI spec and contract-tested against a live server in CI.
- Project documentation, community guidelines and contribution templates.
- `hookyard serve` with `/healthz` and `/readyz` endpoints, structured logging and graceful shutdown.
- End-to-end tests: the real binaries are tested over HTTP (retries, `Retry-After`, the dead-letter queue and replay, dedupe, a SIGKILL crash with recovery, graceful shutdown), plus dashboard browser tests on desktop and phone, all in CI. New `HOOKYARD_LEASE_MARGIN` setting.
- Docker image (multi-stage, distroless, non-root, about 20 MB) with the dashboard and flakyvendor, a `hookyard healthcheck` command for container health checks, and a one-command quickstart: `docker compose -f deploy/docker-compose.yml up --build`.
- Dashboard dead-letter queue: dead requests grouped by upstream and failure reason, inspect a group, and replay a group or everything after a confirmation that shows the exact count (from a server-side dry run); a live dead-letter count in the navigation.
- Dashboard requests explorer and request detail: filters (status, upstream, time, tag, dedupe key) kept in the URL, jump to a request by ID, load more; an attempt timeline with responses and backoff, live updates until the request finishes, replay and cancel, copy as curl or SDK call, and redaction of sensitive header values.
- Dashboard overview: per-upstream health (healthy, degraded, failing, idle), success rate, attempts per minute, p50/p95/p99 latency, queue backlog and dead letters; a deliveries chart with a table view; selectable time range kept in the URL; auto-refresh; and a "send your first request" guide when there is no traffic yet.
- Web dashboard embedded in the binary (`HOOKYARD_DASHBOARD`): sign-in with an API token exchanged for a signed session cookie, CSRF protection for cookie sessions, security headers, light and dark themes, and a responsive layout.
- Delivery metrics: per-upstream per-minute rollups in Postgres (safe with several instances), `GET /v1/stats/overview` (success rate, throughput, p50/p95/p99 latency, queue depth, oldest pending age, DLQ size) and `GET /v1/stats/timeseries`; optional Prometheus endpoint (`HOOKYARD_METRICS`); retention of finished requests (`HOOKYARD_REQUEST_RETENTION`), old rollups and expired dedupe keys.
- Management API: list requests with filters and cursor pagination, get a request and its attempts, replay and cancel, a dead-letter queue summary grouped by upstream and failure reason, bulk replay by filter with `dry_run`, and upstream listing (header values are never returned). Replays get a fresh retry budget, and replay and cancel are recorded in an audit log with the token name.
- Retry policies: exponential backoff with full jitter, `Retry-After` on `429`/`503`, permanent failures for other `4xx` and `3xx`, and `max_age` measured from when a request becomes due. Requests that give up move to the dead-letter queue (`dead`) with the reason recorded. The engine wakes exactly when retries are due.
- Delivery engine: a bounded worker pool claims due requests with `FOR UPDATE SKIP LOCKED`, delivers them over HTTP and records every attempt. Leases with fencing recover requests from crashed workers without letting stale workers overwrite results; in-flight deliveries drain on shutdown. Requests carry `Hookyard-Request-Id` and `Hookyard-Attempt` headers, and redirects are never followed.
- `flakyvendor`, a deliberately unreliable test API (failures, `Retry-After`, fake `200` errors, latency, hangs) for tests and demos.
- `POST /v1/requests`: enqueue outbound requests, with bearer-token authentication (`HOOKYARD_API_TOKENS`), per-field validation errors, explicit `dedupe_key` deduplication, scheduled delivery (`deliver_at`) and per-request timeout and retry overrides.
- Upstream registry in `hookyard.yaml`: defaults, retry presets (`none`, `quick`, `standard`, `patient`) with per-field overrides, static headers, `${ENV_VAR}` interpolation, strict validation with line numbers, and `hookyard validate`.
- Postgres storage with embedded, lock-protected migrations; `hookyard migrate [up|status]`; `HOOKYARD_DATABASE_URL` and `HOOKYARD_AUTO_MIGRATE` settings; `/readyz` checks the database.
- OpenAPI 3.1 specification for the v1 API (`api/openapi.yaml`), linted in CI.
