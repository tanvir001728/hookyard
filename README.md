<div align="center">

# 🪝 Hookyard

**Reliable delivery for outbound API calls: retries, backoff, dead-letter queues and circuit breakers, handled for you.**

Hand Hookyard a request to a third-party API and move on. It delivers the request, retries it, parks it
when the vendor is down, and shows you everything that happened.

[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)
[![Status: early development](https://img.shields.io/badge/status-early%20development-orange.svg)](#project-status)
[![PRs Welcome](https://img.shields.io/badge/PRs-welcome-brightgreen.svg)](CONTRIBUTING.md)

[Why Hookyard](#why-hookyard) •
[How it works](#how-it-works) •
[A quick look](#a-quick-look) •
[Roadmap](#roadmap) •
[Contributing](#contributing)

</div>

---

## Project status

> [!WARNING]
> Hookyard is in **early development** and is **not ready for production use**.
> The APIs shown below are the planned design and may change before `v1.0`.
> Follow the [milestones](https://github.com/tanvir001728/hookyard/milestones) to track progress.

## Why Hookyard

Every service that talks to third-party APIs ends up rebuilding the same reliability code:

- **Retries with exponential backoff and jitter**, and remembering to honor `Retry-After`
- **Dead-letter queues** for requests that never succeed, plus a way to replay them
- **Circuit breakers** so a struggling vendor doesn't take your workers down with it
- **Rate limiting** to stay under each vendor's quota
- **Delivery tracking** to answer "did we actually send it, and what did they reply?"

Partner and vendor APIs make this worse. They time out, return `200 OK` with an error in the body,
use unusual auth schemes, and rarely support idempotency keys.

Hookyard moves all of that into a single self-hosted service. Your application code shrinks to
*"deliver this to vendor X"*.

## How it works

```
 Your services                           Hookyard                                 Third-party APIs
┌─────────────┐   POST /v1/requests   ┌──────────────────────────────────────┐
│ orders-svc  │ ────────────────────▶ │  durable queue (Postgres)            │
│ billing-svc │ ◀──── 202 + id ────── │   └─▶ per-vendor workers             │ ───▶  courier-x
│ ...         │                       │        ├─ rate limit & concurrency   │ ───▶  payments-y
└─────────────┘                       │        ├─ retries, backoff, jitter   │ ───▶  erp-z
       ▲                              │        ├─ circuit breaker (pause)    │
       │   signed callback / poll     │        └─ dead-letter queue          │
       └───────────────────────────── │  dashboard · metrics · audit log     │
                                      └──────────────────────────────────────┘
```

- **Async by design.** Apps enqueue a request and get an id back immediately. The outcome arrives by
  signed callback, or you poll for it.
- **Nothing gets lost.** When a vendor's circuit breaker opens, deliveries to it are *paused* instead of
  failed, and the queue drains once the vendor recovers.
- **Built-in dashboard.** See success rates, latency, queue depth, breaker state and the DLQ, and
  replay failed requests with one click.
- **Self-hosted.** A single Go binary; Postgres is the only dependency.

## A quick look

> [!NOTE]
> This is the **planned** TypeScript SDK API, shown here to explain the developer experience we're
> building toward.

**Start with three lines.** Retries, dead-lettering and circuit breaking are on by default:

```ts
import { Hookyard } from "@hookyard/sdk";

const hy = new Hookyard(); // reads HOOKYARD_URL and HOOKYARD_TOKEN
await hy.to("courier-x").post("/shipments", { orderId: 123 });
```

**Take control when you need it.** Every option is typed:

```ts
const job = await hy.to("courier-x").post("/shipments", body, {
  dedupeKey: `order-${id}-shipment`,
  retry: "patient",            // "none" | "quick" | "standard" | "patient" | custom policy
  deliverAt: inOneHour,
  orderingKey: `order-${id}`,
  tags: { app: "orders" },
});

const outcome = await job.result({ timeout: "30s" }); // optional: wait for the outcome
```

**Manage everything from code.** Anything you can do in the dashboard is also available in the API,
the SDK and the CLI:

```ts
await hy.dlq.replay({ upstream: "courier-x", since: "2h" });
await hy.upstreams.pause("courier-x");
```

## Roadmap

| Milestone | Focus |
| --- | --- |
| **v0.1 — Async core** | Queue, workers, retry presets, DLQ, dedupe, management API, TypeScript SDK, dashboard |
| **v0.2 — Resilience** | Rate limits, circuit breaker, response classification, signed callbacks, live tail |
| **v0.3 — Vendor realism** | Auth plugins (OAuth2, HMAC), scheduled and ordered delivery, CLI, alerting |
| **v0.4 — Production ready** | Docs site, releases, SSO, multi-instance deployments |

Progress is tracked in [milestones](https://github.com/tanvir001728/hookyard/milestones) and
[issues](https://github.com/tanvir001728/hookyard/issues).

## Contributing

Contributions are very welcome, whether it's a bug report, a feature idea, docs or code.
Please read the [contributing guide](CONTRIBUTING.md) and our [code of conduct](CODE_OF_CONDUCT.md)
before you start.

## Security

Please **do not** open public issues for security vulnerabilities. See [SECURITY.md](SECURITY.md) for
how to report them privately.

## License

Hookyard is licensed under the [MIT License](LICENSE).
