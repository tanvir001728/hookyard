# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- Project documentation, community guidelines and contribution templates.
- `hookyard serve` with `/healthz` and `/readyz` endpoints, structured logging and graceful shutdown.
- Retry policies: exponential backoff with full jitter, `Retry-After` on `429`/`503`, permanent failures for other `4xx` and `3xx`, and `max_age` measured from when a request becomes due. Requests that give up move to the dead-letter queue (`dead`) with the reason recorded. The engine wakes exactly when retries are due.
- Delivery engine: a bounded worker pool claims due requests with `FOR UPDATE SKIP LOCKED`, delivers them over HTTP and records every attempt. Leases with fencing recover requests from crashed workers without letting stale workers overwrite results; in-flight deliveries drain on shutdown. Requests carry `Hookyard-Request-Id` and `Hookyard-Attempt` headers, and redirects are never followed.
- `flakyvendor`, a deliberately unreliable test API (failures, `Retry-After`, fake `200` errors, latency, hangs) for tests and demos.
- `POST /v1/requests`: enqueue outbound requests, with bearer-token authentication (`HOOKYARD_API_TOKENS`), per-field validation errors, explicit `dedupe_key` deduplication, scheduled delivery (`deliver_at`) and per-request timeout and retry overrides.
- Upstream registry in `hookyard.yaml`: defaults, retry presets (`none`, `quick`, `standard`, `patient`) with per-field overrides, static headers, `${ENV_VAR}` interpolation, strict validation with line numbers, and `hookyard validate`.
- Postgres storage with embedded, lock-protected migrations; `hookyard migrate [up|status]`; `HOOKYARD_DATABASE_URL` and `HOOKYARD_AUTO_MIGRATE` settings; `/readyz` checks the database.
- OpenAPI 3.1 specification for the v1 API (`api/openapi.yaml`), linted in CI.
