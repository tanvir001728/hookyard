import { describe, expect, it } from "vitest";
import { InvalidStateError } from "../src/errors.js";
import { client, json, mockFetch, wireRequest } from "./helpers.js";

const retryWire = {
  preset: "quick" as const,
  max_attempts: 5,
  initial_interval: "1s",
  max_interval: "30s",
  multiplier: 2,
  max_age: "10m",
};

describe("requests", () => {
  it("list() builds the query and maps the page", async () => {
    const { fetch, calls } = mockFetch(json(200, { data: [wireRequest()], next_cursor: "abc" }));
    const page = await client(fetch).requests.list({
      upstream: "courier-x",
      status: ["dead", "failed"],
      tags: { app: "orders", tenant: "acme:eu" },
      dedupeKey: "order-1",
      createdAfter: new Date("2026-09-01T00:00:00Z"),
      createdBefore: "2026-09-02T00:00:00Z",
      limit: 10,
      cursor: "prev",
    });

    const url = calls[0]!.url;
    expect(url.pathname).toBe("/v1/requests");
    expect(url.searchParams.get("upstream")).toBe("courier-x");
    expect(url.searchParams.get("status")).toBe("dead,failed");
    expect(url.searchParams.getAll("tag")).toEqual(["app:orders", "tenant:acme:eu"]);
    expect(url.searchParams.get("dedupe_key")).toBe("order-1");
    expect(url.searchParams.get("created_after")).toBe("2026-09-01T00:00:00.000Z");
    expect(url.searchParams.get("created_before")).toBe("2026-09-02T00:00:00Z");
    expect(url.searchParams.get("limit")).toBe("10");
    expect(url.searchParams.get("cursor")).toBe("prev");
    expect(page.nextCursor).toBe("abc");
    expect(page.data[0]?.createdAt).toEqual(new Date("2026-09-29T10:00:00Z"));
  });

  it("list() accepts a single status and no filter", async () => {
    const { fetch, calls } = mockFetch(json(200, { data: [], next_cursor: null }));
    const hy = client(fetch);
    await hy.requests.list({ status: "dead" });
    const page = await hy.requests.list();
    expect(calls[0]?.url.search).toBe("?status=dead");
    expect(calls[1]?.url.search).toBe("");
    expect(page).toEqual({ data: [], nextCursor: null });
  });

  it("iterate() walks every page", async () => {
    const { fetch, calls } = mockFetch(
      json(200, { data: [wireRequest({ id: "req_3" }), wireRequest({ id: "req_2" })], next_cursor: "c1" }),
      json(200, { data: [wireRequest({ id: "req_1" })], next_cursor: null }),
    );
    const ids: string[] = [];
    for await (const req of client(fetch).requests.iterate({ upstream: "courier-x", limit: 2 })) ids.push(req.id);

    expect(ids).toEqual(["req_3", "req_2", "req_1"]);
    expect(calls.map((c) => c.url.searchParams.get("cursor"))).toEqual([null, "c1"]);
    expect(calls.every((c) => c.url.searchParams.get("upstream") === "courier-x")).toBe(true);
  });

  it("iterate() stops early when the loop breaks", async () => {
    const { fetch, calls } = mockFetch(json(200, { data: [wireRequest(), wireRequest()], next_cursor: "more" }));
    const iterator = client(fetch).requests.iterate();
    for await (const _ of iterator) break;
    expect(calls).toHaveLength(1);
  });

  it("get() encodes the id", async () => {
    const { fetch, calls } = mockFetch(json(200, wireRequest()));
    await client(fetch).requests.get("req_1/../x");
    expect(calls[0]?.url.pathname).toBe("/v1/requests/req_1%2F..%2Fx");
  });

  it("attempts() maps every attempt", async () => {
    const { fetch, calls } = mockFetch(
      json(200, {
        data: [
          {
            number: 1,
            started_at: "2026-09-29T10:00:00Z",
            duration_ms: 120,
            outcome: "retryable_failure",
            status_code: 503,
            error: { code: "http_status", message: "upstream responded with 503 Service Unavailable" },
            response: { headers: { "Retry-After": "1" }, body: "busy", body_truncated: false },
            retry_at: "2026-09-29T10:00:01Z",
            classified_by: "fake success",
          },
          {
            number: 2,
            started_at: "2026-09-29T10:00:01Z",
            duration_ms: 0,
            outcome: "retryable_failure",
            status_code: null,
            error: { code: "connection", message: "connection refused" },
            response: null,
            retry_at: null,
            classified_by: null,
          },
        ],
      }),
    );
    const attempts = await client(fetch).requests.attempts("req_1");
    expect(calls[0]?.url.pathname).toBe("/v1/requests/req_1/attempts");
    expect(attempts).toEqual([
      {
        number: 1,
        startedAt: new Date("2026-09-29T10:00:00Z"),
        durationMs: 120,
        outcome: "retryable_failure",
        statusCode: 503,
        error: { code: "http_status", message: "upstream responded with 503 Service Unavailable" },
        response: { headers: { "Retry-After": "1" }, body: "busy", bodyTruncated: false },
        retryAt: new Date("2026-09-29T10:00:01Z"),
        classifiedBy: "fake success",
      },
      {
        number: 2,
        startedAt: new Date("2026-09-29T10:00:01Z"),
        durationMs: 0,
        outcome: "retryable_failure",
        statusCode: null,
        error: { code: "connection", message: "connection refused" },
        response: null,
        retryAt: null,
        classifiedBy: null,
      },
    ]);
  });

  it("replay() and cancel() POST to the request", async () => {
    const { fetch, calls } = mockFetch(json(202, wireRequest({ status: "pending" })), json(200, wireRequest({ status: "canceled" })));
    const hy = client(fetch);
    expect((await hy.requests.replay("req_1")).status).toBe("pending");
    expect((await hy.requests.cancel("req_1")).status).toBe("canceled");
    expect(calls.map((c) => `${c.method} ${c.url.pathname}`)).toEqual([
      "POST /v1/requests/req_1/replay",
      "POST /v1/requests/req_1/cancel",
    ]);
    expect(calls[0]?.body).toBeUndefined();
  });
});

describe("dlq", () => {
  it("summary() maps groups", async () => {
    const { fetch, calls } = mockFetch(
      json(200, {
        total: 3,
        groups: [
          {
            upstream: "courier-x",
            error_code: "http_status",
            status_code: 500,
            count: 3,
            oldest_dead_at: "2026-09-29T08:00:00Z",
            newest_dead_at: "2026-09-29T09:00:00Z",
          },
        ],
      }),
    );
    const summary = await client(fetch).dlq.summary({ upstream: "courier-x" });
    expect(calls[0]?.url.search).toBe("?upstream=courier-x");
    expect(summary).toEqual({
      total: 3,
      groups: [
        {
          upstream: "courier-x",
          errorCode: "http_status",
          statusCode: 500,
          count: 3,
          oldestDeadAt: new Date("2026-09-29T08:00:00Z"),
          newestDeadAt: new Date("2026-09-29T09:00:00Z"),
        },
      ],
    });
  });

  it("replay() sends the filter in snake_case", async () => {
    const { fetch, calls } = mockFetch(json(200, { matched: 4, replayed: 0, dry_run: true }));
    const result = await client(fetch).dlq.replay({
      upstream: "courier-x",
      errorCode: "timeout",
      statusCode: 504,
      deadAfter: new Date("2026-09-29T08:00:00Z"),
      deadBefore: "2026-09-29T10:00:00Z",
      ids: ["req_1", "req_2"],
      dryRun: true,
    });
    expect(calls[0]?.method).toBe("POST");
    expect(calls[0]?.url.pathname).toBe("/v1/dlq/replay");
    expect(calls[0]?.body).toEqual({
      upstream: "courier-x",
      error_code: "timeout",
      status_code: 504,
      dead_after: "2026-09-29T08:00:00.000Z",
      dead_before: "2026-09-29T10:00:00Z",
      ids: ["req_1", "req_2"],
      dry_run: true,
    });
    expect(result).toEqual({ matched: 4, replayed: 0, dryRun: true });
  });

  it("replay() with no filter replays the whole DLQ", async () => {
    const { fetch, calls } = mockFetch(json(200, { matched: 0, replayed: 0, dry_run: false }));
    await client(fetch).dlq.replay();
    expect(calls[0]?.body).toEqual({ dry_run: false });
  });
});

describe("upstreams", () => {
  const upstream = {
    name: "courier-x",
    base_url: "https://api.courier-x.example",
    timeout: "15s",
    retry: retryWire,
    header_names: ["Authorization"],
    limits: { rate_limit: "10/s", burst: 10, max_concurrency: null },
    state: { status: "active", breaker: "closed", breaker_since: null, pause: null, in_flight: 1, available_tokens: 9, throttled_until: null },
    on_timeout: "unknown",
  };
  const mapped = {
    name: "courier-x",
    baseUrl: "https://api.courier-x.example",
    timeout: "15s",
    retry: { preset: "quick", maxAttempts: 5, initialInterval: "1s", maxInterval: "30s", multiplier: 2, maxAge: "10m" },
    headerNames: ["Authorization"],
    limits: { rateLimit: "10/s", burst: 10, maxConcurrency: null },
    state: { status: "active", breaker: "closed", breakerSince: null, pause: null, inFlight: 1, availableTokens: 9, throttledUntil: null },
    onTimeout: "unknown",
  };

  it("list() returns the upstreams", async () => {
    const { fetch, calls } = mockFetch(json(200, { data: [upstream] }));
    await expect(client(fetch).upstreams.list()).resolves.toEqual([mapped]);
    expect(calls[0]?.url.pathname).toBe("/v1/upstreams");
  });

  it("get() returns one upstream", async () => {
    const { fetch, calls } = mockFetch(json(200, upstream));
    await expect(client(fetch).upstreams.get("courier-x")).resolves.toEqual(mapped);
    expect(calls[0]?.url.pathname).toBe("/v1/upstreams/courier-x");
  });
});

describe("stats", () => {
  it("overview() maps per-upstream stats", async () => {
    const { fetch, calls } = mockFetch(
      json(200, {
        window: "2h",
        data: [
          {
            upstream: "courier-x",
            succeeded: 10,
            failed_attempts: 2,
            dead: 1,
            success_rate: 0.9091,
            throughput_per_min: 0.1,
            latency_ms: { p50: 120, p95: 300.5, p99: null },
            queue_depth: 3,
            oldest_pending_age_seconds: 12,
            dlq_size: 1,
          },
        ],
      }),
    );
    const overview = await client(fetch).stats.overview({ window: "2h" });
    expect(calls[0]?.url.search).toBe("?window=2h");
    expect(overview).toEqual({
      window: "2h",
      data: [
        {
          upstream: "courier-x",
          succeeded: 10,
          failedAttempts: 2,
          dead: 1,
          successRate: 0.9091,
          throughputPerMin: 0.1,
          latencyMs: { p50: 120, p95: 300.5, p99: null },
          queueDepth: 3,
          oldestPendingAgeSeconds: 12,
          dlqSize: 1,
        },
      ],
    });
  });

  it("overview() converts a window in milliseconds", async () => {
    const { fetch, calls } = mockFetch(json(200, { window: "1h", data: [] }));
    await client(fetch).stats.overview({ window: 3_600_000 });
    expect(calls[0]?.url.searchParams.get("window")).toBe("3600000ms");
  });

  it("timeseries() builds the query and maps buckets", async () => {
    const { fetch, calls } = mockFetch(
      json(200, {
        upstream: null,
        step: "5m",
        data: [
          {
            start: "2026-09-29T10:00:00Z",
            succeeded: 4,
            failed_attempts: 1,
            dead: 0,
            latency_ms: { p50: null, p95: null, p99: null },
          },
        ],
      }),
    );
    const series = await client(fetch).stats.timeseries({
      upstream: "courier-x",
      from: new Date("2026-09-29T10:00:00Z"),
      to: "2026-09-29T11:00:00Z",
      step: "5m",
    });
    const params = calls[0]!.url.searchParams;
    expect(calls[0]?.url.pathname).toBe("/v1/stats/timeseries");
    expect(Object.fromEntries(params)).toEqual({
      upstream: "courier-x",
      from: "2026-09-29T10:00:00.000Z",
      to: "2026-09-29T11:00:00Z",
      step: "5m",
    });
    expect(series).toEqual({
      upstream: null,
      step: "5m",
      data: [
        {
          start: new Date("2026-09-29T10:00:00Z"),
          succeeded: 4,
          failedAttempts: 1,
          dead: 0,
          latencyMs: { p50: null, p95: null, p99: null },
        },
      ],
    });
  });
});

describe("upstream pause, resume and events", () => {
  const stateWire = {
    status: "paused",
    breaker: "closed",
    breaker_since: "2026-10-04T10:00:00Z",
    pause: { since: "2026-10-04T10:00:00Z", until: null, reason: "maintenance", by: "ops" },
    in_flight: 0,
    available_tokens: 3,
    throttled_until: null,
  };
  const upstreamWire = {
    name: "courier-x",
    base_url: "https://api.courier-x.example",
    timeout: "15s",
    retry: { preset: "quick", max_attempts: 5, initial_interval: "1s", max_interval: "30s", multiplier: 2, max_age: "10m" },
    header_names: [],
    limits: { rate_limit: null, burst: null, max_concurrency: null },
    state: stateWire,
    on_timeout: "retry",
  };

  it("pause() sends the reason and duration and maps the state", async () => {
    const { fetch, calls } = mockFetch(json(200, upstreamWire));
    const up = await client(fetch).upstreams.pause("courier-x", { reason: "maintenance", duration: "2h" });
    expect(calls[0]?.url.pathname).toBe("/v1/upstreams/courier-x/pause");
    expect(calls[0]?.body).toEqual({ reason: "maintenance", duration: "2h" });
    expect(up.state).toEqual({
      status: "paused",
      breaker: "closed",
      breakerSince: new Date("2026-10-04T10:00:00Z"),
      pause: { since: new Date("2026-10-04T10:00:00Z"), until: null, reason: "maintenance", by: "ops" },
      inFlight: 0,
      availableTokens: 3,
      throttledUntil: null,
    });
  });

  it("resume() is not retried, so a 409 surfaces as InvalidStateError", async () => {
    const { fetch, calls } = mockFetch(json(409, { error: { code: "invalid_state", message: 'upstream "courier-x" is not paused' } }));
    await expect(client(fetch).upstreams.resume("courier-x")).rejects.toBeInstanceOf(InvalidStateError);
    expect(calls).toHaveLength(1);
  });

  it("events() maps timestamps", async () => {
    const { fetch, calls } = mockFetch(
      json(200, { data: [{ id: 2, at: "2026-10-04T10:05:00Z", kind: "breaker_open", reason: "5 consecutive failures", actor: "hookyard", details: {} }] }),
    );
    const events = await client(fetch).upstreams.events("courier-x", { limit: 5 });
    expect(calls[0]?.url.search).toBe("?limit=5");
    expect(events[0]).toEqual({ id: 2, at: new Date("2026-10-04T10:05:00Z"), kind: "breaker_open", reason: "5 consecutive failures", actor: "hookyard", details: {} });
  });
});

describe("requests.resolve", () => {
  it("posts the outcome and reason, and is not retried", async () => {
    const { fetch, calls } = mockFetch(json(409, { error: { code: "invalid_state", message: "cannot resolve a request that is succeeded (allowed: unknown)" } }));
    await expect(client(fetch).requests.resolve("req_01", "succeeded", { reason: "vendor confirmed" })).rejects.toBeInstanceOf(InvalidStateError);
    expect(calls).toHaveLength(1);
    expect(calls[0]?.url.pathname).toBe("/v1/requests/req_01/resolve");
    expect(calls[0]?.body).toEqual({ outcome: "succeeded", reason: "vendor confirmed" });
  });
});
