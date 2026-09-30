import { describe, expect, it } from "vitest";
import { TimeoutError } from "../src/index.js";
import { client, json, mockFetch, wireRequest } from "./helpers.js";

describe("Job", () => {
  it("result() polls until the request is final", async () => {
    const { fetch, calls } = mockFetch(
      json(202, wireRequest({ status: "pending" })),
      json(200, wireRequest({ status: "in_flight" })),
      json(200, wireRequest({ status: "failed", attempt_count: 1, last_status_code: 503 })),
      json(200, wireRequest({ status: "succeeded", attempt_count: 2, last_status_code: 200 })),
    );
    const job = await client(fetch).to("courier-x").post("/shipments", {});

    const result = await job.result({ pollInterval: 1 });

    expect(result.status).toBe("succeeded");
    expect(result.attemptCount).toBe(2);
    expect(job.status).toBe("succeeded");
    expect(job.request).toBe(result);
    expect(calls.slice(1).map((c) => `${c.method} ${c.url.pathname}`)).toEqual([
      "GET /v1/requests/req_01J9ZK3Q4W5E6R7T8Y9U0I1O2P",
      "GET /v1/requests/req_01J9ZK3Q4W5E6R7T8Y9U0I1O2P",
      "GET /v1/requests/req_01J9ZK3Q4W5E6R7T8Y9U0I1O2P",
    ]);
  });

  it.each(["dead", "canceled", "unknown"] as const)("result() resolves with a %s request", async (status) => {
    const { fetch } = mockFetch(json(202, wireRequest()), json(200, wireRequest({ status })));
    const job = await client(fetch).to("courier-x").post("/shipments", {});
    await expect(job.result({ pollInterval: 1 })).resolves.toMatchObject({ status });
  });

  it("result() returns at once when the request is already final", async () => {
    const { fetch, calls } = mockFetch(
      json(200, wireRequest({ status: "succeeded", dedupe_key: "k" }), { "Hookyard-Deduplicated": "true" }),
    );
    const job = await client(fetch).to("courier-x").post("/shipments", {}, { dedupeKey: "k" });
    await expect(job.result()).resolves.toMatchObject({ status: "succeeded" });
    expect(calls).toHaveLength(1);
  });

  it("result() throws a TimeoutError with the last known state", async () => {
    const { fetch } = mockFetch(json(202, wireRequest()), json(200, wireRequest({ status: "failed" })));
    const job = await client(fetch).to("courier-x").post("/shipments", {});

    const err = await job.result({ timeout: 30, pollInterval: 5 }).catch((e: unknown) => e);

    expect(err).toBeInstanceOf(TimeoutError);
    expect(err).toMatchObject({ code: "timeout", request: { status: "failed" } });
    expect((err as Error).message).toBe(
      "Request req_01J9ZK3Q4W5E6R7T8Y9U0I1O2P did not reach a final status within 30ms (last status: failed). " +
        'It is still being delivered: call result() again to keep waiting, or check it later with requests.get("req_01J9ZK3Q4W5E6R7T8Y9U0I1O2P").',
    );
  });

  it("result() accepts duration strings", async () => {
    const { fetch } = mockFetch(json(202, wireRequest()), json(200, wireRequest({ status: "succeeded" })));
    const job = await client(fetch).to("courier-x").post("/shipments", {});
    await expect(job.result({ timeout: "5s", pollInterval: "1ms" })).resolves.toMatchObject({ status: "succeeded" });
    await expect(job.result({ timeout: "forever" })).rejects.toThrow(/Invalid duration "forever" for timeout/);
  });

  it("refresh() fetches and stores the current state", async () => {
    const { fetch } = mockFetch(json(202, wireRequest()), json(200, wireRequest({ status: "in_flight" })));
    const job = await client(fetch).to("courier-x").post("/shipments", {});
    const req = await job.refresh();
    expect(req.status).toBe("in_flight");
    expect(job.status).toBe("in_flight");
  });

  it("serializes to JSON", async () => {
    const { fetch } = mockFetch(json(202, wireRequest()));
    const job = await client(fetch).to("courier-x").post("/shipments", {});
    expect(JSON.parse(JSON.stringify(job))).toMatchObject({
      id: job.id,
      status: "pending",
      deduplicated: false,
      request: { id: job.id, createdAt: "2026-09-29T10:00:00.000Z" },
    });
  });
});
