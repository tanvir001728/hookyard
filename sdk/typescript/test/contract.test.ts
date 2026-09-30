/**
 * Contract tests against a live Hookyard server. Skipped unless HOOKYARD_URL and HOOKYARD_TOKEN
 * are set. The server needs an upstream named `flaky` that points at flakyvendor
 * (cmd/flakyvendor), for example:
 *
 *   upstreams:
 *     flaky:
 *       base_url: http://127.0.0.1:19090
 *       retry: { preset: quick, max_attempts: 2, initial_interval: 100ms }
 *
 * Run with `pnpm test:contract`. Set HOOKYARD_CONTRACT_REQUIRED=1 (as CI does) to fail instead of
 * skipping when the server settings are missing.
 */
import { describe, expect, it } from "vitest";
import { AuthError, Hookyard, NotFoundError, UnknownUpstreamError, ValidationError } from "../src/index.js";
import type { UpstreamStats } from "../src/index.js";

const url = process.env["HOOKYARD_URL"];
const token = process.env["HOOKYARD_TOKEN"];
const live = Boolean(url && token);

if (!live && process.env["HOOKYARD_CONTRACT_REQUIRED"]) {
  throw new Error("Contract tests are required but HOOKYARD_URL and HOOKYARD_TOKEN are not set.");
}

// Unique per run, so dedupe keys and tags never collide with earlier runs against the same database.
const run = `sdk-contract-${Date.now().toString(36)}-${Math.random().toString(36).slice(2, 8)}`;

describe.skipIf(!live)("contract: live Hookyard server", () => {
  const hy = live ? new Hookyard() : (undefined as unknown as Hookyard);
  const flaky = () => hy.to("flaky");

  it("delivers a request and reports success", async () => {
    const job = await flaky().post("/orders", { orderId: 1 }, { tags: { run } });
    expect(job.id).toMatch(/^req_/);
    expect(job.deduplicated).toBe(false);
    expect(["pending", "in_flight", "succeeded"]).toContain(job.status);

    const result = await job.result({ timeout: "20s" });
    expect(result).toMatchObject({ status: "succeeded", attemptCount: 1, lastStatusCode: 200, tags: { run } });
    expect(result.completedAt).toBeInstanceOf(Date);
  });

  it("deduplicates by dedupe key", async () => {
    const dedupeKey = `${run}-dedupe`;
    const first = await flaky().post("/orders", { orderId: 2 }, { dedupeKey });
    const second = await flaky().post("/orders", { orderId: 2 }, { dedupeKey });
    expect(first.deduplicated).toBe(false);
    expect(second.deduplicated).toBe(true);
    expect(second.id).toBe(first.id);
    expect(second.request.dedupeKey).toBe(dedupeKey);
  });

  it("uses a generated dedupe key that does not collide across calls", async () => {
    const a = await flaky().get("/ping");
    const b = await flaky().get("/ping");
    expect(a.id).not.toBe(b.id);
    expect(a.request.dedupeKey).toMatch(/^sdk_/);
  });

  it("maps every option to the server", async () => {
    const deliverAt = new Date(Date.now() + 60 * 60 * 1000);
    const job = await hy.send({
      upstream: "flaky",
      method: "PUT",
      path: "/orders/3",
      body: "<order/>",
      headers: { "Content-Type": "application/xml" },
      deliverAt,
      timeout: 2500,
      retry: { preset: "quick", maxAttempts: 3, maxInterval: "5s" },
      tags: { run, kind: "scheduled" },
    });
    expect(job.request).toMatchObject({
      method: "PUT",
      body: "<order/>",
      status: "scheduled",
      timeout: "2s500ms", // 2500 ms, in the server's canonical format
      retry: { preset: "quick", maxAttempts: 3, maxInterval: "5s" },
      tags: { run, kind: "scheduled" },
    });
    expect(job.request.headers["Content-Type"]).toBe("application/xml");
    expect(job.request.deliverAt?.toISOString()).toBe(deliverAt.toISOString());

    const canceled = await hy.requests.cancel(job.id);
    expect(canceled.status).toBe("canceled");
  });

  it("reports validation errors per field", async () => {
    const err = await flaky()
      .post("orders", {}, { retry: { maxAttempts: 1000 } })
      .catch((e: unknown) => e);
    expect(err).toBeInstanceOf(ValidationError);
    const fields = (err as ValidationError).details.map((d) => d.field);
    expect(fields).toEqual(expect.arrayContaining(["path", "retry.max_attempts"]));
    expect((err as ValidationError).status).toBe(422);
  });

  it("suggests the closest upstream for a typo", async () => {
    const err = await hy
      .to("flakey")
      .post("/orders", {})
      .catch((e: unknown) => e);
    expect(err).toBeInstanceOf(UnknownUpstreamError);
    expect((err as Error).message).toContain('did you mean "flaky"?');
  });

  it("rejects a bad token", async () => {
    const bad = new Hookyard({ token: "not-a-real-token" });
    await expect(bad.upstreams.list()).rejects.toBeInstanceOf(AuthError);
  });

  it("lists, gets and inspects attempts", async () => {
    // A 400 is a permanent failure: the request goes straight to the DLQ.
    const job = await flaky().post("/orders?status=400", { orderId: 4 }, { tags: { run, kind: "dead" } });
    const dead = await job.result({ timeout: "20s" });
    expect(dead).toMatchObject({ status: "dead", lastStatusCode: 400, lastError: { code: "http_status" } });

    const got = await hy.requests.get(job.id);
    expect(got.id).toBe(job.id);

    const attempts = await hy.requests.attempts(job.id);
    expect(attempts).toHaveLength(1);
    expect(attempts[0]).toMatchObject({ number: 1, outcome: "permanent_failure", statusCode: 400 });
    expect(attempts[0]?.startedAt).toBeInstanceOf(Date);
    expect(attempts[0]?.response?.body).toContain("/orders");

    const page = await hy.requests.list({ upstream: "flaky", status: ["dead"], tags: { run }, limit: 1 });
    expect(page.data.map((r) => r.id)).toEqual([job.id]);

    await expect(hy.requests.get("req_doesnotexist")).rejects.toBeInstanceOf(NotFoundError);
  });

  it("iterates over every page", async () => {
    const created: string[] = [];
    for (let i = 0; i < 3; i++) {
      created.push((await flaky().get(`/ping?i=${i}`, { tags: { run, kind: "page" } })).id);
    }
    const seen: string[] = [];
    for await (const r of hy.requests.iterate({ tags: { run, kind: "page" }, limit: 2 })) seen.push(r.id);
    expect(seen).toEqual(created.reverse()); // newest first, across two pages
  });

  it("summarizes the DLQ and replays it with a dry run", async () => {
    const job = await flaky().post("/orders?status=500", {}, { retry: "none", tags: { run } });
    await job.result({ timeout: "20s" });

    const summary = await hy.dlq.summary({ upstream: "flaky" });
    expect(summary.total).toBeGreaterThanOrEqual(1);
    expect(summary.groups).toContainEqual(
      expect.objectContaining({ upstream: "flaky", errorCode: "http_status", statusCode: 500 }),
    );

    const dry = await hy.dlq.replay({ ids: [job.id], dryRun: true });
    expect(dry).toEqual({ matched: 1, replayed: 0, dryRun: true });

    const replayed = await hy.requests.replay(job.id);
    expect(replayed.status).toBe("pending");
    expect(replayed.completedAt).toBeNull();
  });

  it("lists and gets upstreams", async () => {
    const upstreams = await hy.upstreams.list();
    expect(upstreams.map((u) => u.name)).toContain("flaky");
    const upstream = await hy.upstreams.get("flaky");
    expect(upstream).toMatchObject({ name: "flaky", retry: { preset: "quick", maxAttempts: 2 } });
    expect(upstream.baseUrl).toMatch(/^http/);
    await expect(hy.upstreams.get("nope")).rejects.toBeInstanceOf(NotFoundError);
  });

  it("returns stats", async () => {
    // The server flushes metrics every 10 seconds, so wait for this run's deliveries to show up.
    const deadline = Date.now() + 30_000;
    let flakyStats: UpstreamStats | undefined;
    while (Date.now() < deadline) {
      const overview = await hy.stats.overview({ window: "1h" });
      expect(overview.window).toBe("1h");
      flakyStats = overview.data.find((s) => s.upstream === "flaky");
      expect(flakyStats).toBeDefined();
      if ((flakyStats?.succeeded ?? 0) >= 1) break;
      await new Promise((resolve) => setTimeout(resolve, 1_000));
    }
    expect(flakyStats?.succeeded).toBeGreaterThanOrEqual(1);
    expect(flakyStats?.latencyMs.p50).toEqual(expect.any(Number));

    const series = await hy.stats.timeseries({ upstream: "flaky", step: "5m" });
    expect(series).toMatchObject({ upstream: "flaky", step: "5m" });
    expect(series.data.length).toBeGreaterThan(0);
    expect(series.data[0]?.start).toBeInstanceOf(Date);
  });
});
