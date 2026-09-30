import { beforeEach, describe, expect, it } from "vitest";
import { ConnectionError, TimeoutError, ValidationError } from "../src/index.js";
import { apiError, client, json, mockFetch, networkError, noJitter, wireRequest } from "./helpers.js";

beforeEach(noJitter);

const dedupeKeys = (calls: { body: unknown }[]) => calls.map((c) => (c.body as { dedupe_key?: string }).dedupe_key);

describe("transport retries", () => {
  it("retries a network error with the same generated dedupe key", async () => {
    const { fetch, calls } = mockFetch(networkError(), json(202, wireRequest()));
    const job = await client(fetch).to("courier-x").post("/shipments", {});

    expect(calls).toHaveLength(2);
    const [first, second] = dedupeKeys(calls);
    expect(first).toMatch(/^sdk_/);
    expect(second).toBe(first);
    expect(job.deduplicated).toBe(false);
  });

  it("retries 5xx and 429 with the caller's dedupe key", async () => {
    const { fetch, calls } = mockFetch(
      apiError(503, "internal", "unavailable"),
      apiError(429, "internal", "slow down"),
      json(202, wireRequest()),
    );
    await client(fetch).to("courier-x").post("/shipments", {}, { dedupeKey: "order-1" });
    expect(dedupeKeys(calls)).toEqual(["order-1", "order-1", "order-1"]);
  });

  it("finds the request an earlier try created instead of duplicating it", async () => {
    // First try reached the server but the response was lost; the retry is deduplicated.
    const { fetch, calls } = mockFetch(
      networkError(),
      json(200, wireRequest({ id: "req_first" }), { "Hookyard-Deduplicated": "true" }),
    );
    const job = await client(fetch).to("courier-x").post("/shipments", {});
    expect(calls).toHaveLength(2);
    expect(job.id).toBe("req_first");
    expect(job.deduplicated).toBe(false);
  });

  it("honors Retry-After", async () => {
    const { fetch, calls } = mockFetch(
      new Response("{}", { status: 429, headers: { "Retry-After": "0" } }),
      json(200, { data: [] }),
    );
    await client(fetch).upstreams.list();
    expect(calls).toHaveLength(2);
  });

  it.each([
    [400, "bad_request"],
    [401, "unauthorized"],
    [404, "not_found"],
    [409, "invalid_state"],
    [413, "payload_too_large"],
    [422, "validation_failed"],
  ])("never retries %i", async (status, code) => {
    const { fetch, calls } = mockFetch(apiError(status, code, "no"), json(202, wireRequest()));
    await expect(client(fetch).to("courier-x").post("/shipments", {})).rejects.toMatchObject({ status, code });
    expect(calls).toHaveLength(1);
  });

  it("gives up after maxRetries and reports a ConnectionError", async () => {
    const { fetch, calls } = mockFetch(networkError());
    const err = await client(fetch, { maxRetries: 3 })
      .to("courier-x")
      .post("/shipments", {})
      .catch((e: unknown) => e);
    expect(calls).toHaveLength(4);
    expect(err).toBeInstanceOf(ConnectionError);
    expect(err).toMatchObject({ code: "connection_error", status: undefined });
    const message = (err as Error).message;
    expect(message).toContain("Could not reach Hookyard at http://hookyard.test for POST /v1/requests");
    expect(message).toContain("ECONNREFUSED");
    expect(message).toContain("(4 attempts)");
    expect(message).toContain("Check HOOKYARD_URL");
    expect((err as Error).cause).toBeInstanceOf(TypeError);
  });

  it("returns the last error response after maxRetries", async () => {
    const { fetch, calls } = mockFetch(apiError(500, "internal", "internal server error"));
    await expect(client(fetch, { maxRetries: 1 }).requests.get("req_1")).rejects.toMatchObject({
      status: 500,
      code: "internal",
    });
    expect(calls).toHaveLength(2);
  });

  it("does not retry at all with maxRetries: 0", async () => {
    const { fetch, calls } = mockFetch(networkError(), json(202, wireRequest()));
    await expect(client(fetch, { maxRetries: 0 }).to("courier-x").post("/a", {})).rejects.toBeInstanceOf(
      ConnectionError,
    );
    expect(calls).toHaveLength(1);
  });

  it("does not retry replay or cancel, which are not idempotent", async () => {
    const replay = mockFetch(networkError(), json(202, wireRequest()));
    await expect(client(replay.fetch).requests.replay("req_1")).rejects.toBeInstanceOf(ConnectionError);
    expect(replay.calls).toHaveLength(1);

    const cancel = mockFetch(apiError(503, "internal", "unavailable"), json(200, wireRequest()));
    await expect(client(cancel.fetch).requests.cancel("req_1")).rejects.toMatchObject({ status: 503 });
    expect(cancel.calls).toHaveLength(1);
  });

  it("retries reads and bulk DLQ replay", async () => {
    const get = mockFetch(networkError(), json(200, wireRequest()));
    await client(get.fetch).requests.get("req_1");
    expect(get.calls).toHaveLength(2);

    const dlq = mockFetch(networkError(), json(200, { matched: 1, replayed: 1, dry_run: false }));
    await client(dlq.fetch).dlq.replay({ upstream: "courier-x" });
    expect(dlq.calls).toHaveLength(2);
  });

  it("times out a call that takes too long and retries it", async () => {
    let n = 0;
    const { fetch, calls } = mockFetch((_call, signal) => {
      n++;
      if (n > 1) return json(200, wireRequest());
      return new Promise<Response>((_, reject) => {
        signal?.addEventListener("abort", () => reject(signal.reason));
      });
    });
    const req = await client(fetch, { timeout: 20 }).requests.get("req_1");
    expect(req.id).toBe("req_01J9ZK3Q4W5E6R7T8Y9U0I1O2P");
    expect(calls).toHaveLength(2);
  });

  it("reports a TimeoutError once retries are exhausted, even if fetch ignores the signal", async () => {
    const { fetch } = mockFetch(() => new Promise<Response>(() => undefined));
    const err = await client(fetch, { timeout: "20ms", maxRetries: 1 })
      .requests.get("req_1")
      .catch((e: unknown) => e);
    expect(err).toBeInstanceOf(TimeoutError);
    expect(err).toMatchObject({ code: "timeout" });
    expect((err as Error).message).toBe(
      "Hookyard at http://hookyard.test did not respond to GET /v1/requests/req_1 within 20ms (2 attempts). " +
        "Check that the server is healthy, or raise the timeout option.",
    );
  });

  it("does not retry a validation error from a retried enqueue", async () => {
    const { fetch, calls } = mockFetch(
      networkError(),
      apiError(422, "validation_failed", "1 field is invalid", [{ field: "path", message: "bad" }]),
    );
    await expect(client(fetch).to("courier-x").post("x", {})).rejects.toBeInstanceOf(ValidationError);
    expect(calls).toHaveLength(2);
  });
});
