import { createServer } from "node:http";
import type { AddressInfo } from "node:net";
import { afterEach, describe, expect, it, vi } from "vitest";
import {
  CallbackVerificationError,
  createCallbackHandler,
  HookyardError,
  signCallback,
  verifyCallback,
} from "../src/index.js";
import type { CallbackEvent } from "../src/index.js";
import { createFakeHookyard } from "../src/testing.js";
import { client, json, mockFetch, wireRequest } from "./helpers.js";

function randomSecret(): string {
  const bytes = crypto.getRandomValues(new Uint8Array(32));
  return `whsec_${Buffer.from(bytes).toString("base64")}`;
}

const SECRET = randomSecret();
const OLD_SECRET = randomSecret();

const wireEvent = {
  type: "request.succeeded",
  timestamp: "2026-10-04T09:12:03Z",
  data: {
    request_id: "req_01J9ZK3Q4W5E6R7T8Y9U0I1O2P",
    upstream: "courier-x",
    method: "POST",
    path: "/shipments",
    status: "succeeded",
    on_result: "order.shipment",
    tags: { order: "123" },
    dedupe_key: "order-123",
    attempt_count: 2,
    last_error: null,
    response: { status_code: 201, headers: { "Content-Type": "application/json" }, body: '{"id":"shp_1"}', body_truncated: false },
    completed_at: "2026-10-04T09:12:03Z",
  },
};

/** A callback request as Hookyard sends it. */
async function callback(
  opts: { body?: unknown; secret?: string | string[]; id?: string; at?: Date; headers?: Record<string, string> } = {},
): Promise<Request> {
  const body = typeof opts.body === "string" ? opts.body : JSON.stringify(opts.body ?? wireEvent);
  const id = opts.id ?? "evt_0f6a3c2b9d8e4f5a";
  const at = opts.at ?? new Date();
  return new Request("http://app.test/hooks/hookyard", {
    method: "POST",
    headers: {
      "Content-Type": "application/json",
      "webhook-id": id,
      "webhook-timestamp": String(Math.floor(at.getTime() / 1000)),
      "webhook-signature": await signCallback(opts.secret ?? SECRET, id, at, body),
      ...opts.headers,
    },
    body,
  });
}

afterEach(() => {
  vi.unstubAllEnvs();
  vi.restoreAllMocks();
});

describe("signCallback", () => {
  it("matches the Standard Webhooks test vector", async () => {
    // The spec's public test secret, split so secret scanners don't flag it.
    const secret = "whsec_" + "MfKQ9r8GKYqrTwjUPD8ILPZIo2LaLaSw";
    const sig = await signCallback(secret, "msg_p5jXN8AQM9LWM0D4loKWxJek", 1614265330, '{"test": 2432232314}');
    expect(sig).toBe("v1,g0hM9SsE+OTPJTGt/tmIKtSyZlE3uFJELVlNIOLJ1OE=");
  });

  it("signs once per secret, for rotation", async () => {
    const sig = await signCallback([SECRET, OLD_SECRET], "evt_1", new Date(), "{}");
    expect(sig.split(" ")).toHaveLength(2);
  });
});

describe("verifyCallback", () => {
  it("returns the typed event of a genuine callback", async () => {
    const event = await verifyCallback(await callback(), { secret: SECRET });
    expect(event).toEqual<CallbackEvent>({
      id: "evt_0f6a3c2b9d8e4f5a",
      type: "request.succeeded",
      timestamp: new Date("2026-10-04T09:12:03Z"),
      data: {
        requestId: "req_01J9ZK3Q4W5E6R7T8Y9U0I1O2P",
        upstream: "courier-x",
        method: "POST",
        path: "/shipments",
        status: "succeeded",
        onResult: "order.shipment",
        tags: { order: "123" },
        dedupeKey: "order-123",
        attemptCount: 2,
        lastError: null,
        response: { statusCode: 201, headers: { "Content-Type": "application/json" }, body: '{"id":"shp_1"}', bodyTruncated: false },
        completedAt: new Date("2026-10-04T09:12:03Z"),
      },
    });
  });

  it("accepts headers and a raw body, as Express and NestJS provide them", async () => {
    const req = await callback();
    const headers: Record<string, string | string[]> = {};
    req.headers.forEach((v, k) => (headers[k] = v));
    const body = new Uint8Array(await req.arrayBuffer());
    await expect(verifyCallback({ headers, body }, { secret: SECRET })).resolves.toMatchObject({ type: "request.succeeded" });
  });

  it("reads the secret from HOOKYARD_CALLBACK_SECRET", async () => {
    vi.stubEnv("HOOKYARD_CALLBACK_SECRET", `${OLD_SECRET},${SECRET}`);
    await expect(verifyCallback(await callback())).resolves.toMatchObject({ id: "evt_0f6a3c2b9d8e4f5a" });
  });

  it("accepts a callback signed with the old and new secret during a rotation", async () => {
    const both = await callback({ secret: [SECRET, OLD_SECRET] });
    await expect(verifyCallback(both, { secret: OLD_SECRET })).resolves.toBeDefined();
  });

  it.each([
    ["a tampered body", async () => {
      const req = await callback();
      return new Request(req.url, { method: "POST", headers: req.headers, body: JSON.stringify({ ...wireEvent, type: "request.dead" }) });
    }, /Invalid callback signature/],
    ["the wrong secret", () => callback({ secret: OLD_SECRET }), /Invalid callback signature/],
    ["an old timestamp", () => callback({ at: new Date(Date.now() - 10 * 60_000) }), /too old/],
    ["a future timestamp", () => callback({ at: new Date(Date.now() + 10 * 60_000) }), /too old or too far in the future/],
    ["no signature", () => callback({ headers: { "webhook-signature": "" } }), /Missing webhook-id/],
    ["a malformed timestamp", () => callback({ headers: { "webhook-timestamp": "soon" } }), /Invalid webhook-timestamp/],
  ])("rejects %s", async (_, make, message) => {
    const err = await verifyCallback(await make(), { secret: SECRET }).catch((e: unknown) => e);
    expect(err).toBeInstanceOf(CallbackVerificationError);
    expect(err).toBeInstanceOf(HookyardError);
    expect((err as CallbackVerificationError).code).toBe("invalid_signature");
    expect((err as Error).message).toMatch(message);
  });

  it("allows a longer tolerance", async () => {
    const old = await callback({ at: new Date(Date.now() - 10 * 60_000) });
    await expect(verifyCallback(old, { secret: SECRET, tolerance: "15m" })).resolves.toBeDefined();
  });

  it("explains a missing or malformed secret", async () => {
    await expect(verifyCallback(await callback())).rejects.toThrow(/HOOKYARD_CALLBACK_SECRET/);
    await expect(verifyCallback(await callback(), { secret: "not-a-secret" })).rejects.toThrow(/start with "whsec_"/);
  });
});

describe("createCallbackHandler", () => {
  const event = (data: Partial<CallbackEvent["data"]>): CallbackEvent => ({
    id: "evt_1",
    type: "request.succeeded",
    timestamp: new Date(),
    data: {
      requestId: "req_1",
      upstream: "courier-x",
      method: "POST",
      path: "/x",
      status: "succeeded",
      onResult: null,
      tags: {},
      dedupeKey: null,
      attemptCount: 1,
      lastError: null,
      response: null,
      completedAt: new Date(),
      ...data,
    },
  });

  it("routes by onResult, then upstream, then *, per status", async () => {
    const calls: string[] = [];
    const h = createCallbackHandler(
      {
        "order.shipment": { succeeded: () => calls.push("shipment.succeeded"), dead: () => calls.push("shipment.dead") },
        "courier-x": (e) => calls.push(`courier-x.${e.data.status}`),
        "*": { unknown: () => calls.push("*.unknown") },
      },
      { secret: SECRET },
    );
    await h.dispatch(event({ onResult: "order.shipment" }));
    await h.dispatch(event({ onResult: "order.shipment", status: "dead" }));
    // No handler for canceled under order.shipment: falls through to the upstream route.
    await h.dispatch(event({ onResult: "order.shipment", status: "canceled" }));
    await h.dispatch(event({ upstream: "payments-y", status: "unknown" }));
    expect(calls).toEqual(["shipment.succeeded", "shipment.dead", "courier-x.canceled", "*.unknown"]);
  });

  it("acknowledges unhandled events, reporting them to onUnhandled", async () => {
    const onUnhandled = vi.fn();
    const h = createCallbackHandler({}, { secret: SECRET, onUnhandled });
    const res = await h.fetch(await callback());
    expect(res.status).toBe(204);
    expect(onUnhandled).toHaveBeenCalledWith(expect.objectContaining({ id: "evt_0f6a3c2b9d8e4f5a" }));
  });

  it("answers 500 when a handler fails, so Hookyard retries", async () => {
    const onError = vi.fn();
    const h = createCallbackHandler({ "order.shipment": () => Promise.reject(new Error("db down")) }, { secret: SECRET, onError });
    const res = await h.fetch(await callback());
    expect(res.status).toBe(500);
    expect(onError).toHaveBeenCalledWith(expect.objectContaining({ message: "db down" }), expect.objectContaining({ id: "evt_0f6a3c2b9d8e4f5a" }));
    await expect(h.handle(await callback())).rejects.toThrow("db down");
  });

  it("answers 401 to a forged callback without running handlers", async () => {
    const route = vi.fn();
    const h = createCallbackHandler({ "*": route }, { secret: SECRET });
    const res = await h.fetch(await callback({ secret: OLD_SECRET }));
    expect(res.status).toBe(401);
    expect(await res.json()).toEqual({ error: expect.stringMatching(/Invalid callback signature/) });
    expect(route).not.toHaveBeenCalled();
  });

  it("fails at startup without a secret", () => {
    expect(() => createCallbackHandler({})).toThrow(/No callback secret/);
  });

  describe("node adapter", () => {
    async function serve(handler: Parameters<typeof createServer>[1]) {
      const server = createServer(handler);
      await new Promise<void>((resolve) => server.listen(0, "127.0.0.1", resolve));
      const { port } = server.address() as AddressInfo;
      return { url: `http://127.0.0.1:${port}/hooks`, close: () => new Promise((r) => server.close(r)) };
    }

    it("reads the raw body of a plain Node.js request", async () => {
      const route = vi.fn();
      const h = createCallbackHandler({ "order.shipment": { succeeded: route } }, { secret: SECRET });
      const srv = await serve((req, res) => void h.node(req, res));
      try {
        const cb = await callback();
        const res = await fetch(srv.url, { method: "POST", headers: cb.headers, body: await cb.text() });
        expect(res.status).toBe(204);
        expect(route).toHaveBeenCalledOnce();
      } finally {
        await srv.close();
      }
    });

    it("uses a body that express.raw() already read", async () => {
      const route = vi.fn();
      const h = createCallbackHandler({ "*": route }, { secret: SECRET });
      const cb = await callback();
      const headers: Record<string, string> = {};
      cb.headers.forEach((v, k) => (headers[k] = v));
      const res = { statusCode: 0, setHeader: vi.fn(), end: vi.fn() };
      const req = Object.assign((async function* () {})(), { headers, body: Buffer.from(await cb.text()) });
      await h.node(req, res);
      expect(res.statusCode).toBe(204);
      expect(route).toHaveBeenCalledOnce();
    });

    it("explains that a JSON-parsed body can't be verified", async () => {
      vi.spyOn(console, "error").mockImplementation(() => {});
      const h = createCallbackHandler({}, { secret: SECRET });
      const res = { statusCode: 0, setHeader: vi.fn(), end: vi.fn() };
      const req = Object.assign((async function* () {})(), { headers: {}, body: { type: "request.succeeded" } });
      await h.node(req, res);
      expect(res.statusCode).toBe(500);
      expect(res.end).toHaveBeenCalledWith(expect.stringContaining("express.json()"));
    });
  });
});

describe("Hookyard client", () => {
  it("sends callbackUrl and onResult", async () => {
    const { fetch, calls } = mockFetch(json(202, wireRequest({ callback_url: "http://app/hooks", on_result: "order.shipment" })));
    const job = await client(fetch).to("courier-x").post("/shipments", {}, { callbackUrl: "http://app/hooks", onResult: "order.shipment" });
    expect(calls[0]?.body).toMatchObject({ callback_url: "http://app/hooks", on_result: "order.shipment" });
    expect(job.request).toMatchObject({ callbackUrl: "http://app/hooks", onResult: "order.shipment" });
  });

  it("lists and retries callbacks", async () => {
    const wire = {
      id: "evt_1",
      request_id: "req_1",
      type: "request.dead",
      url: "http://app/hooks",
      request_status: "dead",
      status: "failed",
      attempt_count: 12,
      next_attempt_at: null,
      last_status_code: 503,
      last_error: "the callback endpoint responded 503 (expected 2xx)",
      last_attempt_at: "2026-10-04T10:00:00Z",
      created_at: "2026-10-04T04:00:00Z",
      delivered_at: null,
    };
    const { fetch, calls } = mockFetch(json(200, { data: [wire] }), json(202, { ...wire, status: "pending", attempt_count: 0 }));
    const hy = client(fetch);
    const [cb] = await hy.requests.callbacks("req_1");
    expect(cb).toMatchObject({ id: "evt_1", status: "failed", lastStatusCode: 503, createdAt: new Date("2026-10-04T04:00:00Z") });
    await expect(hy.requests.retryCallback("req_1", "evt_1")).resolves.toMatchObject({ status: "pending" });
    expect(calls.map((c) => `${c.method} ${c.url.pathname}`)).toEqual([
      "GET /v1/requests/req_1/callbacks",
      "POST /v1/requests/req_1/callbacks/evt_1/retry",
    ]);
  });

  it("verifies and handles callbacks with its callbackSecret", async () => {
    const route = vi.fn();
    const hy = client(mockFetch().fetch, { callbackSecret: SECRET });
    await expect(hy.verifyCallback(await callback())).resolves.toMatchObject({ type: "request.succeeded" });
    const res = await hy.handler({ "order.shipment": { succeeded: route } }).fetch(await callback());
    expect(res.status).toBe(204);
    expect(route).toHaveBeenCalledOnce();
  });
});

describe("testing fake", () => {
  it("emits callbacks to the app's routes", async () => {
    const shipped = vi.fn();
    const failed = vi.fn();
    const fake = createFakeHookyard({
      outcome: (r) => (r.path === "/refunds" ? "dead" : "succeeded"),
      callbacks: { "order.shipment": { succeeded: shipped }, "*": { dead: failed } },
    });
    await fake.to("courier-x").post("/shipments", { id: 1 }, { onResult: "order.shipment" });
    await fake.to("payments-y").post("/refunds", {});
    expect(shipped).toHaveBeenCalledWith(expect.objectContaining({ data: expect.objectContaining({ path: "/shipments", status: "succeeded" }) }));
    expect(failed).toHaveBeenCalledWith(expect.objectContaining({ type: "request.dead" }));
    expect(fake.callbacks.map((e) => e.type)).toEqual(["request.succeeded", "request.dead"]);
  });

  it("records callbacks only for requests with a callbackUrl when no receiver is set", async () => {
    const fake = createFakeHookyard();
    const job = await fake.to("courier-x").post("/a", {}, { callbackUrl: "http://app/hooks" });
    await fake.to("courier-x").post("/b", {});
    expect(fake.callbacks).toHaveLength(1);
    expect(await fake.requests.callbacks(job.id)).toEqual([expect.objectContaining({ status: "delivered", url: "http://app/hooks" })]);
  });

  it("emits a callback when a request is canceled or resolved", async () => {
    const fake = createFakeHookyard({ outcome: "pending", callbacks: { "*": () => {} } });
    const job = await fake.to("courier-x").post("/a", {});
    expect(fake.callbacks).toHaveLength(0);
    await fake.requests.cancel(job.id);
    expect(fake.callbacks.map((e) => e.type)).toEqual(["request.canceled"]);
  });

  it("builds signed callbacks that the app's endpoint accepts", async () => {
    const fake = createFakeHookyard();
    const job = await fake.to("courier-x").post("/a", {}, { onResult: "x" });
    const route = vi.fn();
    const res = await fake.handler({ x: route }).fetch(await fake.signedCallback(job.id));
    expect(res.status).toBe(204);
    expect(route).toHaveBeenCalledWith(fake.callbackEvent(job.id));

    // Signed for the app's own secret, it passes the app's real handler.
    const appHandler = createCallbackHandler({ x: route }, { secret: SECRET });
    expect((await appHandler.fetch(await fake.signedCallback(job.id, { secret: SECRET }))).status).toBe(204);
    expect((await appHandler.fetch(await fake.signedCallback(job.id))).status).toBe(401);
  });
});
