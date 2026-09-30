# @hookyard/sdk

The official TypeScript SDK for [Hookyard](https://github.com/tanvir001728/hookyard), a self-hosted
service that reliably delivers your outbound HTTP calls to third-party APIs, with retries, backoff and a
dead-letter queue.

[![CI](https://github.com/tanvir001728/hookyard/actions/workflows/ci.yml/badge.svg)](https://github.com/tanvir001728/hookyard/actions/workflows/ci.yml)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](https://github.com/tanvir001728/hookyard/blob/main/LICENSE)

- **Zero runtime dependencies.** Uses the global `fetch` in Node.js 18+ (and other modern runtimes).
- **Fully typed.** Strict TypeScript, camelCase types, `Date` objects for timestamps.
- **Safe to retry.** The SDK retries its own calls to Hookyard without ever creating duplicate requests.
- **Easy to test.** An in-memory fake ships in `@hookyard/sdk/testing`.

> [!WARNING]
> Hookyard is in early development. The API may change before `v1.0`.

## Contents

- [Install](#install)
- [Quickstart](#quickstart)
- [Configuration](#configuration)
- [Sending requests](#sending-requests)
- [Waiting for the outcome](#waiting-for-the-outcome)
- [Managing requests, the DLQ, upstreams and stats](#management-api)
- [Error handling](#error-handling)
- [Transport retries and deduplication](#transport-retries-and-deduplication)
- [Testing your code](#testing-your-code)

## Install

```sh
npm install @hookyard/sdk
# or: pnpm add @hookyard/sdk / yarn add @hookyard/sdk
```

You need a running Hookyard server with at least one upstream configured in `hookyard.yaml`. See the
[main README](https://github.com/tanvir001728/hookyard#readme) and the
[configuration reference](https://github.com/tanvir001728/hookyard/blob/main/docs/configuration.md).

## Quickstart

```ts
import { Hookyard } from "@hookyard/sdk";

const hy = new Hookyard(); // reads HOOKYARD_URL and HOOKYARD_TOKEN
await hy.to("courier-x").post("/shipments", { orderId: 123 });
```

That's it: the call returns as soon as Hookyard has stored the request. Hookyard delivers it to the
`courier-x` upstream in the background, retries it with backoff if the vendor fails, and parks it in the
dead-letter queue if it never succeeds.

## Configuration

```ts
const hy = new Hookyard({
  url: "http://localhost:8080", // default: process.env.HOOKYARD_URL
  token: "…",                   // default: process.env.HOOKYARD_TOKEN
  timeout: "10s",               // per HTTP call to Hookyard (not to the upstream)
  maxRetries: 2,                // SDK → Hookyard retries; 0 disables them
  fetch: customFetch,           // default: the global fetch
});
```

| Option | Default | Description |
| --- | --- | --- |
| `url` | `HOOKYARD_URL` | Base URL of the Hookyard server. A path prefix (for a reverse proxy) is kept. |
| `token` | `HOOKYARD_TOKEN` | An API token from the server's `HOOKYARD_API_TOKENS`. |
| `timeout` | `"10s"` | Timeout of each HTTP call to Hookyard. A duration string or milliseconds. |
| `maxRetries` | `2` | How often a failed call to Hookyard is retried (network errors, timeouts, `429`, `5xx`). |
| `fetch` | global `fetch` | A custom `fetch`, for example to add tracing or a proxy agent. |

The constructor throws a `TypeError` with a clear message if the URL or token is missing or invalid.

## Sending requests

`hy.to(upstream)` returns a client for one upstream, with a method per HTTP verb. Each returns a
[`Job`](#waiting-for-the-outcome).

```ts
const courier = hy.to("courier-x");

await courier.get("/shipments/42");
await courier.delete("/shipments/42");
await courier.post("/shipments", { orderId: 123 });
await courier.put("/shipments/42", { weightKg: 2.5 });
await courier.patch("/shipments/42", { status: "ready" });
```

`hy.send()` is the low-level equivalent:

```ts
await hy.send({ upstream: "courier-x", method: "POST", path: "/shipments", body: { orderId: 123 } });
```

### Options

Every method takes an optional, fully typed options object:

```ts
const job = await hy.to("courier-x").post("/shipments", body, {
  dedupeKey: `order-${id}-shipment`,
  retry: "patient",
  deliverAt: new Date(Date.now() + 60 * 60 * 1000),
  timeout: "15s",
  headers: { "X-Correlation-Id": correlationId },
  tags: { app: "orders", tenant: "acme" },
});
```

| Option | Type | Description |
| --- | --- | --- |
| `dedupeKey` | `string` | Makes enqueueing idempotent. While a request with the same key exists for the same upstream (24 hours by default), sending again returns that request instead of creating a new one, and `job.deduplicated` is `true`. |
| `retry` | `"none" \| "quick" \| "standard" \| "patient"` or an object | Retry policy for this request. Defaults to the upstream's policy. See below. |
| `deliverAt` | `Date \| string` | Deliver no earlier than this time (at most 30 days ahead). The request stays `scheduled` until then. |
| `timeout` | `string \| number` | Per-attempt timeout for the call to the upstream, overriding the upstream's. |
| `headers` | `Record<string, string>` | Headers for this request. Headers configured on the upstream (usually credentials) take precedence. |
| `tags` | `Record<string, string>` | Labels for filtering and grouping, such as the calling app or tenant (up to 20). |

**Durations** are strings with a unit (`"500ms"`, `"30s"`, `"5m"`, `"1h30m"`) or a number of
milliseconds (`1500` is sent as `"1500ms"`). Invalid values throw a `TypeError` before anything is sent.

**Retry policies.** Pick a preset, or start from one and override individual fields:

| Preset | Attempts | Initial interval | Max interval | Max age |
| --- | --- | --- | --- | --- |
| `none` | 1 | – | – | – |
| `quick` | 5 | 1s | 30s | 10m |
| `standard` | 10 | 5s | 10m | 24h |
| `patient` | 25 | 30s | 1h | 72h |

```ts
await courier.post("/shipments", body, {
  retry: { preset: "patient", maxAttempts: 5, maxInterval: "2m" },
});
```

The object form accepts `preset`, `maxAttempts` (1–100), `initialInterval`, `maxInterval`,
`multiplier` (1–10) and `maxAge`. Delays grow exponentially with full jitter, and a `Retry-After`
header from the upstream always wins.

### Request bodies

- Objects, arrays, numbers and booleans are sent to the upstream as JSON with
  `Content-Type: application/json`.
- A **string is sent verbatim**, so set a `Content-Type` header for other formats:

  ```ts
  await hy.to("erp").post("/orders", "<order id='7'/>", { headers: { "Content-Type": "application/xml" } });
  await hy.to("legacy").post("/form", "a=1&b=2", {
    headers: { "Content-Type": "application/x-www-form-urlencoded" },
  });
  ```

- Omit the body (or pass `null`) to send none.

## Waiting for the outcome

Enqueueing is asynchronous by design. A `Job` holds the request as it was when enqueued:

```ts
const job = await hy.to("courier-x").post("/shipments", body);

job.id;            // "req_01J9ZK3Q4W5E6R7T8Y9U0I1O2P"
job.status;        // "pending"
job.deduplicated;  // false
job.request;       // the full request: upstream, path, retry policy, timestamps…
```

When you need the outcome, wait for it:

```ts
const outcome = await job.result({ timeout: "30s" });

if (outcome.status === "succeeded") {
  console.log(`delivered after ${outcome.attemptCount} attempt(s)`);
} else {
  console.warn(`gave up: ${outcome.lastError?.message}`); // "dead", "canceled" or "unknown"
}
```

- `job.result({ timeout?, pollInterval? })` polls until the request reaches a final status
  (`succeeded`, `dead`, `canceled` or `unknown`) and returns it. A `dead` request is **returned, not
  thrown**. If the request isn't final within `timeout` (default `30s`), it throws a `TimeoutError`
  whose `request` property holds the last known state; delivery carries on in the background.
- `job.refresh()` fetches the current state once and updates `job.request`.

Statuses: `scheduled` → `pending` → `in_flight` → `succeeded`, or `failed` (a retry is scheduled) and
eventually `dead`. `isFinalStatus(status)` tells you whether a status is final.

## Management API

Everything you can do in the dashboard is available from the SDK. All responses use camelCase fields
and `Date` objects.

### Requests

```ts
// One page, newest first. Filters are combined with AND.
const { data, nextCursor } = await hy.requests.list({
  upstream: "courier-x",
  status: ["dead", "failed"],
  tags: { app: "orders" },
  createdAfter: new Date(Date.now() - 24 * 60 * 60 * 1000),
  limit: 100,
});

// Every matching request, fetching pages as needed.
for await (const request of hy.requests.iterate({ status: "dead" })) {
  console.log(request.id, request.lastError?.message);
}

const request = await hy.requests.get("req_…");
const attempts = await hy.requests.attempts("req_…"); // status codes, response bodies, timings
await hy.requests.replay("req_…"); // a finished request, with a fresh retry budget
await hy.requests.cancel("req_…"); // a scheduled, pending or failed request
```

### Dead-letter queue

```ts
const { total, groups } = await hy.dlq.summary({ upstream: "courier-x" });
// groups: [{ upstream, errorCode: "timeout", statusCode: null, count: 12, oldestDeadAt, newestDeadAt }]

// Always try a dry run first: an empty filter matches the whole DLQ.
const preview = await hy.dlq.replay({ upstream: "courier-x", errorCode: "timeout", dryRun: true });
console.log(`${preview.matched} requests would be replayed`);

await hy.dlq.replay({
  upstream: "courier-x",
  errorCode: "timeout",
  deadAfter: new Date(Date.now() - 2 * 60 * 60 * 1000),
});
```

`dlq.replay()` also accepts `statusCode`, `deadBefore` and `ids` (up to 1000).

### Upstreams and stats

```ts
const upstreams = await hy.upstreams.list(); // header names only, never values
const courier = await hy.upstreams.get("courier-x");

const overview = await hy.stats.overview({ window: "1h" });
// [{ upstream, succeeded, dead, successRate, throughputPerMin, latencyMs: { p50, p95, p99 }, queueDepth, dlqSize, … }]

const series = await hy.stats.timeseries({ upstream: "courier-x", from: yesterday, step: "15m" });
```

## Error handling

Every error the SDK throws extends `HookyardError`, which has:

| Property | Description |
| --- | --- |
| `code` | A stable, machine-readable code, safe to branch on. |
| `status` | The HTTP status from Hookyard, or `undefined` if there was no response. |
| `message` | A human-readable explanation, usually with a hint on how to fix it. |
| `details` | Per-field problems for validation errors, `[{ field, message }]`. Empty otherwise. |

| Class | `code` | When |
| --- | --- | --- |
| `BadRequestError` | `bad_request` | Malformed request (400). |
| `AuthError` | `unauthorized` | Missing or invalid token (401). |
| `NotFoundError` | `not_found` | Unknown request or upstream (404). |
| `InvalidStateError` | `invalid_state` | For example, canceling an `in_flight` request (409). |
| `PayloadTooLargeError` | `payload_too_large` | Body above the server's limit (413). |
| `ValidationError` | `validation_failed` | One or more invalid fields (422); see `details`. |
| `UnknownUpstreamError` | `unknown_upstream` | Upstream not configured (422); the message suggests the closest name. |
| `ConnectionError` | `connection_error` | Hookyard could not be reached, even after retries. |
| `TimeoutError` | `timeout` | Hookyard did not respond in time, or `job.result()` timed out. |
| `HookyardError` | `internal`, `unexpected_response` | Server errors, or a response that isn't from Hookyard. |

```ts
import { ConnectionError, UnknownUpstreamError, ValidationError } from "@hookyard/sdk";

try {
  await hy.to("courier-y").post("shipments", body);
} catch (err) {
  if (err instanceof UnknownUpstreamError) {
    console.error(err.message); // Upstream "courier-y" is not configured (did you mean "courier-x"?)
  } else if (err instanceof ValidationError) {
    for (const { field, message } of err.details) console.error(`${field}: ${message}`);
    // path: must start with "/"
  } else if (err instanceof ConnectionError) {
    // Hookyard is down or unreachable: the request was not stored.
  }
  throw err;
}
```

## Transport retries and deduplication

Hookyard retries deliveries to *upstreams*. The SDK separately retries its own calls to *Hookyard*
when they fail with a network error, a timeout, `429` or `5xx`, up to `maxRetries` times (default 2),
with exponential backoff and full jitter, honoring `Retry-After`. Other `4xx` responses are never
retried.

Retrying an enqueue call is only safe if it can't create a duplicate, because a call that seemed to fail
may have reached the server. So the SDK **always retries enqueue calls with the same dedupe key**:

- If you pass a `dedupeKey`, every retry reuses it.
- If you don't, the SDK generates one (`sdk_` followed by a random UUID) for that `send()` call. It only
  protects that call's own retries: two separate `send()` calls always create two requests. The key is
  stored with the request, so you will see it as `request.dedupeKey`. Finding the request created by an
  earlier try of the same call is not reported as `deduplicated`.
- With `maxRetries: 0`, no key is generated.

For end-to-end idempotency, for example when your own job runner may retry the code that calls
`send()`, pass a `dedupeKey` derived from your data, such as `` `order-${orderId}-shipment` ``.

Reads and `dlq.replay()` are safe to repeat and are retried. `requests.replay()` and
`requests.cancel()` are not retried, because repeating one that already succeeded would fail with a
confusing `InvalidStateError`.

## Testing your code

`@hookyard/sdk/testing` provides an in-memory Hookyard with the same interface as the real client. It
makes no network calls, records everything you send and "delivers" requests instantly.

Write your code against the `HookyardClient` interface:

```ts
// orders.ts
import type { HookyardClient } from "@hookyard/sdk";

export async function shipOrder(hy: HookyardClient, orderId: number) {
  await hy.to("courier-x").post("/shipments", { orderId }, { dedupeKey: `order-${orderId}-shipment` });
}
```

Then pass the fake in tests:

```ts
// orders.test.ts
import { createFakeHookyard } from "@hookyard/sdk/testing";
import { expect, it } from "vitest";
import { shipOrder } from "./orders";

it("ships the order", async () => {
  const fake = createFakeHookyard();

  await shipOrder(fake, 123);

  expect(fake.sent).toContainEqual(expect.objectContaining({ upstream: "courier-x", path: "/shipments" }));
});
```

- `fake.sent` lists every `send()` call in camelCase (`upstream`, `method`, `path`, `body` and the
  options you set), including deduplicated ones.
- `fake.reset()` forgets everything, for example in `beforeEach`.
- Requests start `pending` and resolve to `succeeded` by default. Configure other outcomes:

  ```ts
  createFakeHookyard({ outcome: "dead" });
  createFakeHookyard({ outcomes: { "payments-y": { status: "dead", statusCode: 503 } } });
  createFakeHookyard({ outcome: (req) => (req.path.startsWith("/refunds") ? "dead" : "succeeded") });
  createFakeHookyard({ outcome: "pending" }); // never finishes: result() times out, cancel() works
  ```

- `createFakeHookyard({ upstreams: ["courier-x"] })` rejects other upstreams with an
  `UnknownUpstreamError`, like a real server. The fake also rejects paths that don't start with `/`.
- The management API works too: `requests.list/get/attempts/replay/cancel`, `dlq.summary/replay`,
  `upstreams` and basic `stats`.

## Links

- [Hookyard README](https://github.com/tanvir001728/hookyard#readme)
- [HTTP API reference (OpenAPI)](https://github.com/tanvir001728/hookyard/blob/main/api/openapi.yaml)
- [Server configuration](https://github.com/tanvir001728/hookyard/blob/main/docs/configuration.md)
- [Design](https://github.com/tanvir001728/hookyard/blob/main/docs/design.md)
- [Contributing](https://github.com/tanvir001728/hookyard/blob/main/CONTRIBUTING.md): SDK development
  commands are `make sdk-install`, `make sdk-test` and `make sdk-build`.

## License

[MIT](https://github.com/tanvir001728/hookyard/blob/main/LICENSE)
