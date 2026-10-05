import { describe, expect, it, vi } from "vitest";
import { Hookyard, Job, VERSION } from "../src/index.js";
import { BASE_URL, TOKEN, client, json, mockFetch, wireRequest } from "./helpers.js";

describe("configuration", () => {
  it("reads HOOKYARD_URL and HOOKYARD_TOKEN from the environment", async () => {
    vi.stubEnv("HOOKYARD_URL", "http://env.test:8080/");
    vi.stubEnv("HOOKYARD_TOKEN", "env-token");
    const { fetch, calls } = mockFetch(json(202, wireRequest()));

    const hy = new Hookyard({ fetch });
    await hy.to("courier-x").post("/shipments", { orderId: 123 });

    expect(hy.url).toBe("http://env.test:8080");
    expect(calls[0]?.url.href).toBe("http://env.test:8080/v1/requests");
    expect(calls[0]?.headers["authorization"]).toBe("Bearer env-token");
  });

  it("prefers options over the environment", () => {
    vi.stubEnv("HOOKYARD_URL", "http://env.test");
    vi.stubEnv("HOOKYARD_TOKEN", "env-token");
    expect(new Hookyard({ url: "https://opt.test/" }).url).toBe("https://opt.test");
  });

  it("keeps a path prefix in the URL", async () => {
    const { fetch, calls } = mockFetch(json(200, { data: [] }));
    await client(fetch, { url: "https://gw.test/hookyard/" }).upstreams.list();
    expect(calls[0]?.url.href).toBe("https://gw.test/hookyard/v1/upstreams");
  });

  it("explains a missing URL", () => {
    vi.stubEnv("HOOKYARD_URL", "");
    vi.stubEnv("HOOKYARD_TOKEN", "t");
    expect(() => new Hookyard()).toThrow("Set HOOKYARD_URL or pass { url }");
  });

  it("explains a missing token", () => {
    vi.stubEnv("HOOKYARD_URL", "http://env.test");
    vi.stubEnv("HOOKYARD_TOKEN", "");
    expect(() => new Hookyard()).toThrow("Set HOOKYARD_TOKEN or pass { token }");
  });

  it("rejects invalid URLs and options", () => {
    expect(() => new Hookyard({ url: "localhost:8080", token: TOKEN })).toThrow(/scheme must be http or https/);
    expect(() => new Hookyard({ url: "not a url", token: TOKEN })).toThrow(/Invalid Hookyard URL "not a url"/);
    expect(() => new Hookyard({ url: BASE_URL, token: TOKEN, maxRetries: -1 })).toThrow(/maxRetries/);
    expect(() => new Hookyard({ url: BASE_URL, token: TOKEN, maxRetries: 1.5 })).toThrow(/maxRetries/);
    expect(() => new Hookyard({ url: BASE_URL, token: TOKEN, timeout: "soon" })).toThrow(/Invalid duration "soon"/);
    expect(() => new Hookyard({ url: BASE_URL, token: TOKEN, timeout: 0 })).toThrow(/greater than 0/);
  });
});

describe("sending requests", () => {
  it("enqueues with three lines", async () => {
    const { fetch, calls } = mockFetch(json(202, wireRequest()));
    const hy = client(fetch);

    const job = await hy.to("courier-x").post("/shipments", { orderId: 123 });

    expect(job).toBeInstanceOf(Job);
    expect(job.id).toBe("req_01J9ZK3Q4W5E6R7T8Y9U0I1O2P");
    expect(job.status).toBe("pending");
    expect(job.deduplicated).toBe(false);
    expect(calls).toHaveLength(1);
    const call = calls[0]!;
    expect(call.method).toBe("POST");
    expect(call.url.pathname).toBe("/v1/requests");
    expect(call.headers).toMatchObject({
      authorization: `Bearer ${TOKEN}`,
      accept: "application/json",
      "content-type": "application/json",
      "user-agent": `hookyard-sdk-typescript/${VERSION}`,
    });
    expect(call.body).toEqual({
      upstream: "courier-x",
      method: "POST",
      path: "/shipments",
      body: { orderId: 123 },
      dedupe_key: expect.stringMatching(/^sdk_[0-9a-f-]{36}$/),
    });
  });

  it.each([
    ["get", "GET"],
    ["delete", "DELETE"],
  ] as const)("%s sends no body", async (fn, method) => {
    const { fetch, calls } = mockFetch(json(202, wireRequest()));
    await client(fetch).to("courier-x")[fn]("/shipments/1", { tags: { app: "orders" } });
    expect(calls[0]?.body).toEqual({
      upstream: "courier-x",
      method,
      path: "/shipments/1",
      tags: { app: "orders" },
      dedupe_key: expect.any(String),
    });
  });

  it.each([
    ["post", "POST"],
    ["put", "PUT"],
    ["patch", "PATCH"],
  ] as const)("%s sends the body", async (fn, method) => {
    const { fetch, calls } = mockFetch(json(202, wireRequest()));
    await client(fetch).to("erp")[fn]("/items/1", [1, 2, 3], { dedupeKey: "k" });
    expect(calls[0]?.body).toEqual({ upstream: "erp", method, path: "/items/1", body: [1, 2, 3], dedupe_key: "k" });
  });

  it("maps every option to the wire format", async () => {
    const { fetch, calls } = mockFetch(json(202, wireRequest()));
    await client(fetch).send({
      upstream: "courier-x",
      method: "POST",
      path: "/shipments?notify=true",
      body: "<shipment/>",
      headers: { "Content-Type": "application/xml" },
      dedupeKey: "order-123-shipment",
      deliverAt: new Date("2026-10-01T10:00:00Z"),
      timeout: 1500,
      retry: { preset: "patient", maxAttempts: 5, initialInterval: "2s", maxInterval: 60_000, multiplier: 3, maxAge: "1h30m" },
      tags: { app: "orders", tenant: "acme" },
    });
    expect(calls[0]?.body).toEqual({
      upstream: "courier-x",
      method: "POST",
      path: "/shipments?notify=true",
      body: "<shipment/>",
      headers: { "Content-Type": "application/xml" },
      dedupe_key: "order-123-shipment",
      deliver_at: "2026-10-01T10:00:00.000Z",
      timeout: "1500ms",
      retry: {
        preset: "patient",
        max_attempts: 5,
        initial_interval: "2s",
        max_interval: "60000ms",
        multiplier: 3,
        max_age: "1h30m",
      },
      tags: { app: "orders", tenant: "acme" },
    });
  });

  it("sends a retry preset name as a string and passes ISO strings through", async () => {
    const { fetch, calls } = mockFetch(json(202, wireRequest()));
    await client(fetch).to("courier-x").post("/x", { a: 1 }, { retry: "none", deliverAt: "2026-10-01T10:00:00Z" });
    expect(calls[0]?.body).toMatchObject({ retry: "none", deliver_at: "2026-10-01T10:00:00Z" });
  });

  it("sends JSON values of every kind", async () => {
    const { fetch, calls } = mockFetch(json(202, wireRequest()));
    const up = client(fetch).to("courier-x");
    await up.post("/a", 42);
    await up.post("/a", false);
    await up.post("/a", null);
    await up.post("/a");
    expect(calls.map((c) => (c.body as { body?: unknown }).body)).toEqual([42, false, null, undefined]);
    expect(calls[3]?.body).not.toHaveProperty("body");
  });

  it("rejects invalid durations and dates before calling the server", async () => {
    const { fetch, calls } = mockFetch(json(202, wireRequest()));
    const up = client(fetch).to("courier-x");
    await expect(up.get("/a", { timeout: "30 seconds" })).rejects.toThrow(
      'Invalid duration "30 seconds" for timeout: use a number of milliseconds or a string with a unit',
    );
    await expect(up.get("/a", { retry: { maxAge: -1 } })).rejects.toThrow(/retry\.maxAge/);
    await expect(up.get("/a", { deliverAt: new Date("nope") })).rejects.toThrow(/Invalid date for deliverAt/);
    expect(calls).toHaveLength(0);
  });

  it("maps the response to a camelCase request with dates", async () => {
    const wire = wireRequest({
      status: "dead",
      attempt_count: 3,
      dedupe_key: "k",
      tags: { app: "orders" },
      deliver_at: "2026-09-29T09:00:00Z",
      next_attempt_at: null,
      last_error: { code: "http_status", message: "upstream responded with 500" },
      last_status_code: 500,
      completed_at: "2026-09-29T10:05:00Z",
    });
    const { fetch } = mockFetch(json(202, wire));
    const job = await client(fetch).to("courier-x").post("/shipments", {}, { dedupeKey: "k" });
    expect(job.request).toEqual({
      id: wire.id,
      upstream: "courier-x",
      method: "POST",
      path: "/shipments",
      headers: {},
      body: { order_id: 123 },
      dedupeKey: "k",
      status: "dead",
      attemptCount: 3,
      retry: {
        preset: "standard",
        maxAttempts: 10,
        initialInterval: "5s",
        maxInterval: "10m",
        multiplier: 2,
        maxAge: "24h",
      },
      timeout: "30s",
      tags: { app: "orders" },
      deliverAt: new Date("2026-09-29T09:00:00Z"),
      nextAttemptAt: null,
      lastError: { code: "http_status", message: "upstream responded with 500" },
      lastStatusCode: 500,
      createdAt: new Date("2026-09-29T10:00:00Z"),
      updatedAt: new Date("2026-09-29T10:00:00Z"),
      completedAt: new Date("2026-09-29T10:05:00Z"),
      callbackUrl: null,
      onResult: null,
    });
  });
});

describe("deduplication", () => {
  it("reports a dedupe hit for a caller-provided key", async () => {
    const { fetch } = mockFetch(json(200, wireRequest({ dedupe_key: "order-1" }), { "Hookyard-Deduplicated": "true" }));
    const job = await client(fetch).to("courier-x").post("/shipments", {}, { dedupeKey: "order-1" });
    expect(job.deduplicated).toBe(true);
    expect(job.request.dedupeKey).toBe("order-1");
  });

  it("does not report a dedupe hit on its own generated key", async () => {
    // A 200 for a generated key means an earlier try of this same call created the request.
    const { fetch } = mockFetch(json(200, wireRequest({ dedupe_key: "sdk_x" }), { "Hookyard-Deduplicated": "true" }));
    const job = await client(fetch).to("courier-x").post("/shipments", {});
    expect(job.deduplicated).toBe(false);
  });

  it("generates a fresh key for every send()", async () => {
    const { fetch, calls } = mockFetch(json(202, wireRequest()));
    const up = client(fetch).to("courier-x");
    await up.post("/a", {});
    await up.post("/a", {});
    const keys = calls.map((c) => (c.body as { dedupe_key: string }).dedupe_key);
    expect(keys[0]).not.toBe(keys[1]);
  });

  it("does not generate a key when retries are disabled", async () => {
    const { fetch, calls } = mockFetch(json(202, wireRequest()));
    await client(fetch, { maxRetries: 0 }).to("courier-x").post("/a", {});
    expect(calls[0]?.body).not.toHaveProperty("dedupe_key");
  });
});
