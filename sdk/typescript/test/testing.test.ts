import { describe, expect, it } from "vitest";
import { Hookyard, InvalidStateError, Job, NotFoundError, TimeoutError, UnknownUpstreamError, ValidationError } from "../src/index.js";
import type { HookyardClient } from "../src/index.js";
import { createFakeHookyard } from "../src/testing.js";
import { BASE_URL, TOKEN } from "./helpers.js";

/** Example application code that depends on the client interface. */
async function shipOrder(hy: HookyardClient, orderId: number) {
  const job = await hy.to("courier-x").post("/shipments", { orderId }, { dedupeKey: `order-${orderId}-shipment` });
  return job.result();
}

describe("createFakeHookyard", () => {
  it("records sent requests and delivers them successfully", async () => {
    const fake = createFakeHookyard();

    const result = await shipOrder(fake, 123);

    expect(result.status).toBe("succeeded");
    expect(result.lastStatusCode).toBe(200);
    expect(fake.sent).toContainEqual(expect.objectContaining({ upstream: "courier-x", path: "/shipments" }));
    expect(fake.sent).toEqual([
      {
        upstream: "courier-x",
        method: "POST",
        path: "/shipments",
        body: { orderId: 123 },
        dedupeKey: "order-123-shipment",
      },
    ]);
  });

  it("returns real Jobs that start pending, like the server", async () => {
    const fake = createFakeHookyard();
    const job = await fake.to("erp").put("/items/1", { qty: 2 }, { tags: { app: "orders" }, retry: "quick", timeout: 5000 });
    expect(job).toBeInstanceOf(Job);
    expect(job.status).toBe("pending");
    expect(job.deduplicated).toBe(false);
    expect(job.request).toMatchObject({
      upstream: "erp",
      method: "PUT",
      tags: { app: "orders" },
      timeout: "5000ms",
      retry: { preset: "quick", maxAttempts: 5 },
    });
    await expect(job.refresh()).resolves.toMatchObject({ status: "succeeded", attemptCount: 1 });
  });

  it("records only the options that were set", async () => {
    const fake = createFakeHookyard();
    await fake.to("courier-x").get("/status");
    expect(fake.sent).toEqual([{ upstream: "courier-x", method: "GET", path: "/status" }]);
  });

  it("deduplicates by upstream and dedupe key", async () => {
    const fake = createFakeHookyard();
    const first = await fake.to("courier-x").post("/a", {}, { dedupeKey: "k" });
    const second = await fake.to("courier-x").post("/a", {}, { dedupeKey: "k" });
    const other = await fake.to("erp").post("/a", {}, { dedupeKey: "k" });
    expect(second.deduplicated).toBe(true);
    expect(second.id).toBe(first.id);
    expect(other.id).not.toBe(first.id);
    expect(fake.sent).toHaveLength(3);
  });

  it("uses a fixed outcome, per-upstream outcomes or a function", async () => {
    const fixed = createFakeHookyard({ outcome: "dead" });
    const dead = await (await fixed.to("x").post("/a")).result();
    expect(dead).toMatchObject({
      status: "dead",
      lastStatusCode: 500,
      lastError: { code: "http_status" },
    });

    const perUpstream = createFakeHookyard({ outcomes: { flaky: { status: "dead", statusCode: 503 } } });
    await expect((await perUpstream.to("flaky").post("/a")).result()).resolves.toMatchObject({
      status: "dead",
      lastStatusCode: 503,
    });
    await expect((await perUpstream.to("stable").post("/a")).result()).resolves.toMatchObject({ status: "succeeded" });

    const byFunction = createFakeHookyard({ outcome: (req) => (req.path.startsWith("/refunds") ? "dead" : "succeeded") });
    await expect((await byFunction.to("pay").post("/refunds")).result()).resolves.toMatchObject({ status: "dead" });
    await expect((await byFunction.to("pay").post("/charges")).result()).resolves.toMatchObject({ status: "succeeded" });
  });

  it("can leave requests unfinished, so result() times out and cancel() works", async () => {
    const fake = createFakeHookyard({ outcome: "pending" });
    const job = await fake.to("slow").post("/a", {});
    await expect(job.result({ timeout: 20, pollInterval: 5 })).rejects.toBeInstanceOf(TimeoutError);

    const canceled = await fake.requests.cancel(job.id);
    expect(canceled.status).toBe("canceled");
    await expect(fake.requests.cancel(job.id)).rejects.toBeInstanceOf(InvalidStateError);
  });

  it("validates like the server", async () => {
    const fake = createFakeHookyard({ upstreams: ["courier-x"] });
    await expect(fake.to("courier-y").post("/a")).rejects.toBeInstanceOf(UnknownUpstreamError);
    const err = await fake
      .to("courier-x")
      .post("shipments")
      .catch((e: unknown) => e);
    expect(err).toBeInstanceOf(ValidationError);
    expect(err).toMatchObject({ details: [{ field: "path", message: 'must start with "/"' }] });
    await expect(fake.to("courier-x").post("/a", {}, { timeout: "soon" })).rejects.toThrow(TypeError);
  });

  it("supports the management API", async () => {
    const fake = createFakeHookyard({ outcomes: { flaky: "dead" }, upstreams: ["courier-x", "flaky"] });
    const ok = await fake.to("courier-x").post("/a", {}, { tags: { app: "orders" } });
    const bad = await fake.to("flaky").post("/b", {});
    await fake.to("flaky").post("/c", {});

    expect((await fake.requests.get(ok.id)).status).toBe("succeeded");
    await expect(fake.requests.get("req_missing")).rejects.toBeInstanceOf(NotFoundError);
    expect(await fake.requests.attempts(bad.id)).toMatchObject([{ number: 1, outcome: "permanent_failure" }]);

    const page = await fake.requests.list({ status: "dead", limit: 1 });
    expect(page.data).toHaveLength(1);
    expect(page.nextCursor).not.toBeNull();
    const all = [];
    for await (const r of fake.requests.iterate({ status: ["dead"], limit: 1 })) all.push(r.path);
    expect(all).toEqual(["/c", "/b"]); // newest first
    expect((await fake.requests.list({ tags: { app: "orders" } })).data.map((r) => r.id)).toEqual([ok.id]);

    expect(await fake.dlq.summary()).toMatchObject({ total: 2, groups: [{ upstream: "flaky", count: 2 }] });
    expect(await fake.dlq.replay({ upstream: "flaky", dryRun: true })).toEqual({ matched: 2, replayed: 0, dryRun: true });
    expect(await fake.dlq.replay({ ids: [bad.id] })).toEqual({ matched: 1, replayed: 1, dryRun: false });
    expect((await fake.requests.attempts(bad.id)).length).toBe(2);

    await expect(fake.requests.replay(ok.id)).resolves.toMatchObject({ status: "pending" });
    expect((await fake.requests.get(ok.id)).attemptCount).toBe(2);

    expect((await fake.upstreams.list()).map((u) => u.name)).toEqual(["courier-x", "flaky"]);
    await expect(fake.upstreams.get("nope")).rejects.toBeInstanceOf(NotFoundError);
    const overview = await fake.stats.overview();
    expect(overview.data.find((s) => s.upstream === "flaky")).toMatchObject({ dead: 2, dlqSize: 2, successRate: 0 });
    expect(await fake.stats.timeseries({ upstream: "flaky" })).toEqual({ upstream: "flaky", step: "1m", data: [] });
  });

  it("reset() forgets everything", async () => {
    const fake = createFakeHookyard();
    const job = await fake.to("x").post("/a");
    fake.reset();
    expect(fake.sent).toEqual([]);
    await expect(fake.requests.get(job.id)).rejects.toBeInstanceOf(NotFoundError);
  });
});

describe("parity with the real client", () => {
  const real = new Hookyard({ url: BASE_URL, token: TOKEN, fetch: async () => new Response() });
  const fake = createFakeHookyard();

  /** Own methods plus class methods, without the ones every object inherits. */
  const methods = (value: object): string[] => {
    const proto: unknown = Object.getPrototypeOf(value);
    const inherited = proto === Object.prototype ? [] : Object.getOwnPropertyNames(proto);
    return [...Object.keys(value), ...inherited]
      .filter((k) => k !== "constructor" && typeof (value as Record<string, unknown>)[k] === "function")
      .sort();
  };

  it("exposes the same top-level methods", () => {
    // The fake adds test helpers on top of the client.
    const helpers = ["reset", "callbackEvent", "signedCallback"];
    expect(methods(fake).filter((m) => !helpers.includes(m))).toEqual(methods(real));
  });

  it.each(["requests", "dlq", "upstreams", "stats"] as const)("exposes the same %s methods", (group) => {
    expect(methods(fake[group])).toEqual(methods(real[group]));
  });

  it("exposes the same upstream client methods", () => {
    expect(methods(fake.to("x"))).toEqual(methods(real.to("x")));
    expect(methods(real.to("x"))).toEqual(["delete", "get", "patch", "post", "put"]);
  });
});

describe("fake upstream pauses", () => {
  it("pauses, resumes and records events like the server", async () => {
    const fake = createFakeHookyard({ upstreams: ["courier-x"] });
    const paused = await fake.upstreams.pause("courier-x", { reason: "maintenance", duration: "1h" });
    expect(paused.state.status).toBe("paused");
    expect(paused.state.pause?.reason).toBe("maintenance");
    expect(paused.state.pause?.until).toBeInstanceOf(Date);

    await expect(fake.upstreams.resume("courier-x")).resolves.toMatchObject({ state: { status: "active", pause: null } });
    await expect(fake.upstreams.resume("courier-x")).rejects.toMatchObject({ code: "invalid_state" });
    await expect(fake.upstreams.events("courier-x")).resolves.toMatchObject([{ kind: "resumed" }, { kind: "paused" }]);
    await expect(fake.upstreams.pause("nope")).rejects.toMatchObject({ code: "not_found" });

    fake.reset();
    await expect(fake.upstreams.events("courier-x")).resolves.toEqual([]);
  });
});

describe("fake unknown outcomes", () => {
  it("can produce unknown requests and resolve them", async () => {
    const fake = createFakeHookyard({ outcomes: { "payments-y": "unknown" } });
    const job = await fake.to("payments-y").post("/charge", { amount: 5 });
    const req = await job.result();
    expect(req.status).toBe("unknown");

    await expect(fake.requests.resolve(req.id, "succeeded")).resolves.toMatchObject({ status: "succeeded", lastError: null });
    await expect(fake.requests.resolve(req.id, "dead")).rejects.toMatchObject({ code: "invalid_state" });
  });
});
