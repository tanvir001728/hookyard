<div align="center">

# 🪝 Hookyard

**Reliable delivery for outbound API calls: retries, backoff, dead-letter queues and circuit breakers, handled for you.**

Hand Hookyard a request to a third-party API and move on. It delivers the request, retries it, parks it
when the vendor is down, and shows you everything that happened.

[![CI](https://github.com/tanvir001728/hookyard/actions/workflows/ci.yml/badge.svg)](https://github.com/tanvir001728/hookyard/actions/workflows/ci.yml)
[![Go Report Card](https://goreportcard.com/badge/github.com/tanvir001728/hookyard)](https://goreportcard.com/report/github.com/tanvir001728/hookyard)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)
[![Release](https://img.shields.io/github/v/release/tanvir001728/hookyard?include_prereleases)](https://github.com/tanvir001728/hookyard/releases)
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
> Hookyard is **early**: [v0.1.0](https://github.com/tanvir001728/hookyard/releases/tag/v0.1.0) is the
> first release. Try it, and please [report issues](https://github.com/tanvir001728/hookyard/issues), but
> expect APIs to change before `v1.0`. Follow the
> [milestones](https://github.com/tanvir001728/hookyard/milestones) to see what's next.

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

- **Async by design.** Apps enqueue a request and get an id back immediately, then poll for the outcome
  (signed callbacks are coming in v0.2).
- **Nothing gets lost.** Requests survive restarts and crashes, and every request that can't be
  delivered lands in the dead-letter queue with its full history, ready to replay. In v0.2, a
  vendor's circuit breaker will *pause* its deliveries instead of failing them.
- **Built-in dashboard.** See success rates, latency, queue depth and the DLQ, and replay failed
  requests with one click.
- **Self-hosted.** A single Go binary; Postgres is the only dependency.

## Quickstart

You need Docker. This starts Hookyard, Postgres and a deliberately unreliable demo API:

```sh
git clone https://github.com/tanvir001728/hookyard.git
cd hookyard
docker compose -f deploy/docker-compose.yml up --build
```

Open **http://localhost:8080** and sign in with the demo token `hookyard-demo-token`. Then send a
request to the demo upstream. The demo API fails the first attempt, so you'll see Hookyard retry it:

```sh
curl -X POST localhost:8080/v1/requests \
  -H "Authorization: Bearer hookyard-demo-token" \
  -d '{"upstream":"demo","method":"POST","path":"/orders?fail_first=1","body":{"order_id":1}}'
```

The dashboard shows the request, both attempts and the delivery stats. Try `/orders?status=500` to
fill the dead-letter queue, then replay it from the **Dead letters** page.

To run Hookyard against your own APIs, see [docs/configuration.md](docs/configuration.md) and
[`hookyard.example.yaml`](hookyard.example.yaml).

## A quick look

These examples use the TypeScript SDK, [`@hookyard/sdk`](sdk/typescript#readme). It isn't published to
npm yet; until it is, build it from [`sdk/typescript`](sdk/typescript#readme) or call the HTTP API
directly.

**Start with three lines.** Retries, backoff and the dead-letter queue are on by default:

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
  tags: { app: "orders" },
});

const outcome = await job.result({ timeout: "30s" }); // optional: wait for the outcome
```

**Manage everything from code.** Anything you can do in the dashboard is also available in the API
and the SDK:

```ts
const dead = await hy.dlq.summary({ upstream: "courier-x" });
await hy.dlq.replay({ upstream: "courier-x", errorCode: "timeout" });
```

The HTTP API is described in [`api/openapi.yaml`](api/openapi.yaml) (OpenAPI 3.1), and all settings in
[docs/configuration.md](docs/configuration.md).

## Roadmap

| Milestone | Focus |
| --- | --- |
| **v0.1 — Async core** ✅ | Queue, workers, retry presets, DLQ, dedupe, management API, TypeScript SDK, dashboard |
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
