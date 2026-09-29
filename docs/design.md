# Hookyard design

> Status: **accepted**. This document describes the target design. Sections are implemented
> milestone by milestone; see the [roadmap](../README.md#roadmap).

## Goals

1. **Reliability by default.** An application hands Hookyard an outbound API call and never has to
   write retry, backoff, dead-letter or circuit-breaker code again.
2. **Great developer experience.** Integration takes a few lines of code, with full control available
   when you need it. Everything visible in the dashboard is also available from the API, SDK and CLI.
3. **Built for real-world vendor APIs.** Handle flaky partners, non-standard auth, `200 OK` responses
   that are really errors, missing idempotency support and IP allowlisting.
4. **Easy to operate.** A single binary with Postgres as the only dependency.

### Non-goals (for now)

- **Synchronous proxying.** Hookyard is async-first. A sync proxy mode may come later.
- **Inbound webhooks** (receiving webhooks from vendors).
- **Workflow orchestration.** Hookyard delivers individual requests; it is not a workflow engine.

## Concepts

| Term | Meaning |
| --- | --- |
| **Upstream** | A configured third-party API (base URL, auth, retry policy, limits), for example `courier-x` |
| **Request** | One outbound call that an application asks Hookyard to deliver |
| **Attempt** | One HTTP try of a request; a request has one or more attempts |
| **DLQ** | Dead-letter queue: requests that exhausted retries or failed permanently |
| **Callback** | A signed HTTP notification to the application when a request reaches a final state |

## API shape

Applications enqueue requests:

```http
POST /v1/requests
Authorization: Bearer <token>

{
  "upstream": "courier-x",
  "method": "POST",
  "path": "/shipments",
  "headers": { "X-Correlation-Id": "abc" },
  "body": { "orderId": 123 },
  "dedupe_key": "order-123-create-shipment",
  "callback_url": "http://orders-svc/hooks/hookyard",
  "deliver_at": "2026-10-01T10:00:00Z",
  "ordering_key": "order-123",
  "retry_policy": { "max_attempts": 10 },
  "tags": { "app": "orders" }
}
```

```http
202 Accepted
{ "id": "req_01J…", "status": "pending" }
```

The outcome is reported through a signed callback, or by polling `GET /v1/requests/{id}`. The full
contract lives in [`api/openapi.yaml`](../api/openapi.yaml).

**Configuration precedence:** request options > upstream configuration > global defaults. The one
exception is headers: headers configured on the upstream (usually credentials) win over request
headers, so applications can't override them.

## Request lifecycle

```
            ┌───────────┐ deliver_at reached
 enqueue ─▶ │ scheduled │ ─────────────┐
    │       └───────────┘              ▼
    └──────────────────────────▶ ┌─────────┐  claimed   ┌───────────┐
                                 │ pending │ ─────────▶ │ in_flight │
                                 └─────────┘            └───────────┘
                                   ▲    ▲                  │  │  │  │
                    retry after    │    │ replay           │  │  │  └─ success ─────────▶ succeeded
                    backoff        │    │                  │  │  └──── retryable error ─▶ failed (retrying) ─┘
                                   │    │                  │  └─────── retries exhausted
                                   │    │                  │           or permanent error ▶ dead (DLQ)
                                   │    │                  └────────── timeout on non-idempotent call ▶ unknown
                                   └────┴── dead / unknown (manual replay)
```

`canceled` can be reached from `scheduled`, `pending` or `failed`.

## Delivery engine

- **Durable queue in Postgres.** Workers claim due requests with `SELECT … FOR UPDATE SKIP LOCKED`, so
  delivery survives restarts and needs no extra infrastructure.
- **Per-upstream isolation.** Each upstream has its own concurrency cap and token-bucket rate limit, so
  one slow vendor can't starve deliveries to others.
- **Retries.** Exponential backoff with full jitter, honoring `Retry-After` on `429`/`503`. Named
  presets (`none`, `quick`, `standard`, `patient`) or a custom policy. Network errors, timeouts,
  `408`, `425`, `429` and `5xx` are retried; other `4xx` and `3xx` are permanent. `max_age` is
  measured from when a request becomes due and restarts on replay.
- **Circuit breaker with pause semantics.** When an upstream's breaker opens, its requests stay queued
  instead of failing or burning retries. Half-open probes resume delivery once the vendor recovers.
- **Response classification.** Rules match on status code and response body (JSON path) to decide
  whether a response is a success, a retryable failure or a permanent failure.
- **Auth plugins.** API key, basic, HMAC signing and OAuth2 client credentials (cached tokens, refresh on
  `401`). Credentials come from environment variables or secret files; applications never see them.

### Delivery semantics

- **At-least-once** delivery. A `dedupe_key` makes the application's own enqueue retries safe: the same
  key for the same upstream returns the existing request instead of creating a new one.
- A **timeout on a non-idempotent call** (for example a `POST` without an idempotency key) moves the
  request to `unknown`, not `failed`, because the vendor may have processed it. These requests need a
  human decision in the dashboard or API.
- **Ordering** is only guaranteed for requests that share an `ordering_key`.

## Observability

- Every attempt is recorded (timing, status, error, classification decision, redacted
  headers and body snippets). This gives a full audit trail for vendor disputes.
- In-memory counters and latency histograms per upstream are flushed to Postgres rollup tables every
  ~10 seconds. They power the dashboard without requiring Prometheus. A `/metrics` endpoint is also
  available.
- A **built-in dashboard** (React, embedded into the binary) shows overview health, upstream details,
  a requests explorer, request timelines, the DLQ, the unknown-outcome queue and a live tail.

## Components

```
cmd/hookyard/        server binary: API + scheduler + workers + dashboard
cmd/flakyvendor/     configurable misbehaving vendor for tests and demos
internal/config/     upstream registry (YAML)
internal/api/        REST API and server-sent events
internal/queue/      Postgres queue, scheduling, DLQ
internal/worker/     delivery workers and callbacks
internal/policy/     retry/backoff and response classification
internal/breaker/    per-upstream circuit breaker
internal/ratelimit/  per-upstream rate and concurrency limits
internal/auth/       vendor auth plugins
internal/stats/      metrics and rollups
internal/store/      database access and migrations
web/                 dashboard
sdk/typescript/      @hookyard/sdk
```

## Open questions

- Payload retention defaults and PII masking rules.
- Coordinating breaker and rate-limit state across multiple Hookyard instances.
- A shared catalog of per-vendor configuration "recipes".
