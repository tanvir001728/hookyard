/**
 * An in-memory Hookyard for unit tests: `import { createFakeHookyard } from "@hookyard/sdk/testing"`.
 *
 * The fake implements the same {@link HookyardClient} interface as the real client, records every
 * `send()` call in `fake.sent`, and "delivers" requests instantly, so `job.result()` resolves right
 * away with the configured outcome (`succeeded` by default). It makes no network calls.
 *
 * @module
 */
import { createCallbackHandler, signCallback, verifyCallback } from "./callbacks.js";
import type { CallbackEvent, CallbackHandler, CallbackRoutes } from "./callbacks.js";
import { iteratePages } from "./client.js";
import { toMilliseconds, toWireTimestamp } from "./duration.js";
import { toWireCreateRequest } from "./mappers.js";
import type { WireCreateRequest } from "./mappers.js";
import { InvalidStateError, NotFoundError, UnknownUpstreamError, ValidationError } from "./errors.js";
import type { FieldError } from "./errors.js";
import { Job } from "./job.js";
import { isFinalStatus } from "./types.js";
import type {
  Attempt,
  CallbackDelivery,
  DeliveryError,
  DlqGroup,
  DlqReplayFilter,
  EffectiveRetryPolicy,
  HookyardClient,
  HookyardRequest,
  HttpMethod,
  ListRequestsFilter,
  RequestPage,
  RequestStatus,
  RetryPreset,
  SendInput,
  Upstream,
  UpstreamEvent,
  UpstreamPause,
  UpstreamStats,
} from "./types.js";
import { createUpstreamClient } from "./upstream.js";

/** A `send()` call recorded by the fake, with only the options that were set. */
export type SentRequest = SendInput;

/**
 * What happens to a request sent to the fake: a status, or a status with details.
 *
 * Final statuses (`succeeded`, `dead`, `canceled`, `unknown`) make `job.result()` resolve at
 * once. Other statuses (such as `pending`) leave the request unfinished, so `job.result()` times
 * out and `requests.cancel()` works, as with a slow upstream.
 */
export type FakeOutcome =
  | RequestStatus
  | {
      status: RequestStatus;
      /** HTTP status of the upstream's response. Defaults to 200 for `succeeded` and 500 for `dead`. */
      statusCode?: number | null | undefined;
      /** Why delivery failed. Defaults to an `http_status` error for `dead`. */
      error?: DeliveryError | null | undefined;
    };

export interface FakeHookyardOptions {
  /**
   * The outcome of every request, or a function that picks one per request. Default `"succeeded"`.
   *
   * ```ts
   * createFakeHookyard({ outcome: (req) => (req.path.startsWith("/refunds") ? "dead" : "succeeded") });
   * ```
   */
  outcome?: FakeOutcome | ((request: SentRequest) => FakeOutcome) | undefined;
  /** Outcomes per upstream name. They take precedence over `outcome`. */
  outcomes?: Record<string, FakeOutcome> | undefined;
  /**
   * Configured upstream names. When set, sending to any other upstream throws an
   * `UnknownUpstreamError`, like a real server. When unset, every upstream is accepted.
   */
  upstreams?: readonly string[] | undefined;
  /**
   * Receives a completion callback for every request that finishes, as your app would: a handler
   * from `hy.handler()` / `createCallbackHandler()`, or routes. Its events are dispatched before
   * `send()` (or `cancel()`, `resolve()`) returns; an error thrown by a route is rethrown there.
   */
  callbacks?: CallbackHandler | CallbackRoutes | undefined;
  /** The secret the fake signs callbacks with. Defaults to a random one; see `fake.callbackSecret`. */
  callbackSecret?: string | undefined;
}

/** An in-memory {@link HookyardClient} for tests. */
export interface FakeHookyard extends HookyardClient {
  /** Every `send()` call, in order, including ones that were deduplicated. */
  readonly sent: SentRequest[];
  /**
   * Every completion callback the fake sent, in order: one per finished request that has a
   * `callbackUrl` (or for every finished request when the `callbacks` option is set).
   */
  readonly callbacks: CallbackEvent[];
  /** The secret the fake signs callbacks with. `fake.handler()` and `fake.verifyCallback()` use it. */
  readonly callbackSecret: string;
  /** The callback event for a finished request, as Hookyard would send it. */
  callbackEvent(requestId: string): CallbackEvent;
  /**
   * A signed Fetch API `Request` carrying a finished request's callback, to test your callback
   * endpoint end to end (sign it for your app's secret with `{ secret }`).
   */
  signedCallback(requestId: string, options?: { url?: string | undefined; secret?: string | undefined }): Promise<Request>;
  /** Forgets every sent request and callback. */
  reset(): void;
}

const METHODS: readonly HttpMethod[] = ["GET", "POST", "PUT", "PATCH", "DELETE"];

const PRESETS: Record<RetryPreset, EffectiveRetryPolicy> = {
  none: { preset: "none", maxAttempts: 1, initialInterval: "1s", maxInterval: "1s", multiplier: 2, maxAge: "24h" },
  quick: { preset: "quick", maxAttempts: 5, initialInterval: "1s", maxInterval: "30s", multiplier: 2, maxAge: "10m" },
  standard: {
    preset: "standard",
    maxAttempts: 10,
    initialInterval: "5s",
    maxInterval: "10m",
    multiplier: 2,
    maxAge: "24h",
  },
  patient: {
    preset: "patient",
    maxAttempts: 25,
    initialInterval: "30s",
    maxInterval: "1h",
    multiplier: 2,
    maxAge: "72h",
  },
};

const DEFAULT_TIMEOUT = "30s";

interface Stored {
  request: HookyardRequest;
  sent: SentRequest;
  attempts: Attempt[];
  /** IDs of the callbacks sent for this request. */
  callbacks: string[];
}

function isHandler(v: CallbackHandler | CallbackRoutes): v is CallbackHandler {
  return typeof (v as CallbackHandler).dispatch === "function" && typeof (v as CallbackHandler).fetch === "function";
}

function randomSecret(): string {
  const bytes = new Uint8Array(32);
  globalThis.crypto.getRandomValues(bytes);
  let bin = "";
  for (const b of bytes) bin += String.fromCharCode(b);
  return `whsec_${btoa(bin)}`;
}

/** The JSON body Hookyard sends for an event. */
function toWireEvent(e: CallbackEvent): unknown {
  const d = e.data;
  return {
    type: e.type,
    timestamp: e.timestamp.toISOString(),
    data: {
      request_id: d.requestId,
      upstream: d.upstream,
      method: d.method,
      path: d.path,
      status: d.status,
      on_result: d.onResult,
      tags: d.tags,
      dedupe_key: d.dedupeKey,
      attempt_count: d.attemptCount,
      last_error: d.lastError,
      response: d.response && {
        status_code: d.response.statusCode,
        headers: d.response.headers,
        body: d.response.body,
        body_truncated: d.response.bodyTruncated,
      },
      completed_at: d.completedAt?.toISOString() ?? null,
    },
  };
}

/**
 * Creates an in-memory Hookyard for tests.
 *
 * ```ts
 * const fake = createFakeHookyard();
 * await placeOrder(fake); // code under test takes a HookyardClient
 * expect(fake.sent).toContainEqual(expect.objectContaining({ upstream: "courier-x", path: "/shipments" }));
 * ```
 */
export function createFakeHookyard(options: FakeHookyardOptions = {}): FakeHookyard {
  const sent: SentRequest[] = [];
  const emitted: CallbackEvent[] = [];
  const callbackSecret = options.callbackSecret ?? randomSecret();
  const receiver =
    options.callbacks === undefined
      ? undefined
      : isHandler(options.callbacks)
        ? options.callbacks
        : createCallbackHandler(options.callbacks, { secret: callbackSecret });
  const store = new Map<string, Stored>();
  let counter = 0;

  const find = (id: string): Stored => {
    const found = store.get(id);
    if (!found) throw new NotFoundError(`Not found: request "${id}" not found`, { status: 404, code: "not_found" });
    return found;
  };
  const load = async (id: string): Promise<HookyardRequest> => clone(find(id).request);

  const resolveOutcome = (request: SentRequest) => {
    const perUpstream = options.outcomes?.[request.upstream];
    const configured = perUpstream ?? options.outcome ?? "succeeded";
    const outcome = typeof configured === "function" ? configured(request) : configured;
    return typeof outcome === "string" ? { status: outcome } : outcome;
  };

  /** Applies the outcome to a stored request, as if it had been delivered. */
  const deliver = (entry: Stored): void => {
    const outcome = resolveOutcome(entry.sent);
    const now = new Date();
    const req = entry.request;
    const status = outcome.status;
    const delivered = status === "succeeded" || status === "dead" || status === "unknown";
    const statusCode =
      outcome.statusCode !== undefined
        ? outcome.statusCode
        : status === "succeeded"
          ? 200
          : status === "dead"
            ? 500
            : null;
    const error =
      outcome.error !== undefined
        ? outcome.error
        : status === "dead"
          ? { code: "http_status" as const, message: `upstream responded with ${statusCode ?? 500}` }
          : status === "unknown"
            ? { code: "timeout" as const, message: "no response within the attempt timeout" }
            : status === "canceled"
              ? { code: "canceled" as const, message: "the request was canceled" }
              : null;

    req.status = status;
    req.updatedAt = now;
    req.completedAt = isFinalStatus(status) ? now : null;
    req.nextAttemptAt = isFinalStatus(status) ? null : now;
    req.lastError = error;
    req.lastStatusCode = delivered ? statusCode : null;
    if (delivered) {
      req.attemptCount += 1;
      entry.attempts.push({
        number: req.attemptCount,
        startedAt: now,
        durationMs: 0,
        outcome: status === "succeeded" ? "success" : "permanent_failure",
        statusCode,
        error: status === "succeeded" ? null : error,
        response: statusCode === null ? null : { headers: {}, body: "", bodyTruncated: false },
        retryAt: null,
        classifiedBy: null,
      });
    }
  };

  const eventFor = (entry: Stored, id: string): CallbackEvent => {
    const r = entry.request;
    if (!isFinalStatus(r.status)) {
      throw invalidState("send a callback for", r.status, "succeeded, dead, unknown, canceled");
    }
    const last = entry.attempts[entry.attempts.length - 1];
    const response = last?.response && last.statusCode !== null ? { statusCode: last.statusCode, ...last.response } : null;
    return {
      id,
      type: `request.${r.status}`,
      timestamp: r.completedAt ?? r.updatedAt,
      data: {
        requestId: r.id,
        upstream: r.upstream,
        method: r.method,
        path: r.path,
        status: r.status,
        onResult: r.onResult,
        tags: { ...r.tags },
        dedupeKey: r.dedupeKey,
        attemptCount: r.attemptCount,
        lastError: r.lastError && { ...r.lastError },
        response,
        completedAt: r.completedAt,
      },
    };
  };

  /** Sends the callback of a request that just finished, like the server's dispatcher. */
  const emit = async (entry: Stored): Promise<void> => {
    if (!isFinalStatus(entry.request.status)) return;
    if (entry.request.callbackUrl === null && receiver === undefined) return;
    const event = eventFor(entry, `evt_fake${String(emitted.length + 1).padStart(8, "0")}`);
    emitted.push(event);
    entry.callbacks.push(event.id);
    if (receiver) await receiver.dispatch(event);
  };

  const send = async (input: SendInput): Promise<Job> => {
    const recorded = compact(input);
    sent.push(recorded);

    // Same option checks and conversions as the real client (throws TypeError on bad durations).
    const wire = toWireCreateRequest(input, input.dedupeKey);
    if (options.upstreams && !options.upstreams.includes(input.upstream)) {
      throw new UnknownUpstreamError(
        `Upstream "${input.upstream}" is not configured (configured upstreams: ${options.upstreams.join(", ")})`,
        { status: 422, code: "unknown_upstream" },
      );
    }
    const problems = validate(input);
    if (problems.length > 0) {
      const summary = problems.length === 1 ? "1 field is invalid" : `${problems.length} fields are invalid`;
      throw new ValidationError(`${summary}: ${problems.map((p) => `${p.field}: ${p.message}`).join("; ")}`, {
        status: 422,
        code: "validation_failed",
        details: problems,
      });
    }

    if (input.dedupeKey !== undefined) {
      for (const entry of store.values()) {
        if (entry.request.upstream === input.upstream && entry.request.dedupeKey === input.dedupeKey) {
          return new Job(clone(entry.request), true, load);
        }
      }
    }

    counter += 1;
    const now = new Date();
    const deliverAt = input.deliverAt === undefined ? null : new Date(input.deliverAt);
    const scheduled = deliverAt !== null && deliverAt.getTime() > now.getTime();
    const request: HookyardRequest = {
      id: `req_fake${String(counter).padStart(8, "0")}`,
      upstream: input.upstream,
      method: input.method,
      path: input.path,
      headers: { ...input.headers },
      body: input.body ?? null,
      dedupeKey: input.dedupeKey ?? null,
      status: scheduled ? "scheduled" : "pending",
      attemptCount: 0,
      retry: effectiveRetry(wire.retry),
      timeout: wire.timeout ?? DEFAULT_TIMEOUT,
      tags: { ...input.tags },
      deliverAt,
      nextAttemptAt: deliverAt ?? now,
      lastError: null,
      lastStatusCode: null,
      createdAt: now,
      updatedAt: now,
      completedAt: null,
      callbackUrl: input.callbackUrl || null,
      onResult: input.onResult ?? null,
    };
    const enqueued = clone(request);
    const entry: Stored = { request, sent: recorded, attempts: [], callbacks: [] };
    store.set(request.id, entry);
    deliver(entry);
    await emit(entry);
    return new Job(enqueued, false, load);
  };

  const list = async (filter: ListRequestsFilter = {}): Promise<RequestPage> => {
    const statuses =
      filter.status === undefined ? undefined : typeof filter.status === "string" ? [filter.status] : filter.status;
    const after = filter.createdAfter === undefined ? undefined : new Date(filter.createdAfter).getTime();
    const before = filter.createdBefore === undefined ? undefined : new Date(filter.createdBefore).getTime();
    const matches = [...store.values()]
      .map((e) => e.request)
      .filter(
        (r) =>
          (filter.upstream === undefined || r.upstream === filter.upstream) &&
          (statuses === undefined || statuses.length === 0 || statuses.includes(r.status)) &&
          (filter.dedupeKey === undefined || r.dedupeKey === filter.dedupeKey) &&
          Object.entries(filter.tags ?? {}).every(([k, v]) => r.tags[k] === v) &&
          (after === undefined || r.createdAt.getTime() >= after) &&
          (before === undefined || r.createdAt.getTime() < before),
      )
      .reverse(); // newest first
    const offset = filter.cursor === undefined ? 0 : Number(filter.cursor);
    const limit = filter.limit ?? 50;
    const data = matches.slice(offset, offset + limit).map(clone);
    const next = offset + limit < matches.length ? String(offset + limit) : null;
    return { data, nextCursor: next };
  };

  const dead = (upstream?: string) =>
    [...store.values()].filter(
      (e) => e.request.status === "dead" && (upstream === undefined || e.request.upstream === upstream),
    );

  const upstreamNames = (): string[] =>
    [...new Set([...(options.upstreams ?? []), ...[...store.values()].map((e) => e.request.upstream)])].sort();

  const pauses = new Map<string, UpstreamPause>();
  const events = new Map<string, UpstreamEvent[]>();
  const addEvent = (name: string, kind: string, reason: string) => {
    const list = events.get(name) ?? [];
    list.unshift({ id: list.length + 1, at: new Date(), kind, reason, actor: "fake", details: {} });
    events.set(name, list);
  };
  const upstream = (name: string): Upstream => {
    const pause = pauses.get(name) ?? null;
    return {
      name,
      baseUrl: `https://${name}.invalid`,
      timeout: DEFAULT_TIMEOUT,
      retry: { ...PRESETS.standard },
      headerNames: [],
      limits: { rateLimit: null, burst: null, maxConcurrency: null },
      onTimeout: "unknown",
      state: {
        status: pause ? "paused" : "active",
        breaker: "closed",
        breakerSince: null,
        pause,
        inFlight: 0,
        availableTokens: null,
        throttledUntil: null,
      },
    };
  };
  const knownUpstream = (name: string): void => {
    if (!upstreamNames().includes(name)) {
      throw new NotFoundError(`Not found: upstream "${name}" is not configured`, { status: 404, code: "not_found" });
    }
  };

  const fake: FakeHookyard = {
    sent,
    callbacks: emitted,
    callbackSecret,
    callbackEvent(requestId) {
      const entry = find(requestId);
      return eventFor(entry, entry.callbacks[entry.callbacks.length - 1] ?? `evt_fake_${requestId}`);
    },
    async signedCallback(requestId, opts = {}) {
      const event = fake.callbackEvent(requestId);
      const body = JSON.stringify(toWireEvent(event));
      const now = new Date();
      return new Request(opts.url ?? "http://localhost/hooks/hookyard", {
        method: "POST",
        headers: {
          "Content-Type": "application/json",
          "webhook-id": event.id,
          "webhook-timestamp": String(Math.floor(now.getTime() / 1000)),
          "webhook-signature": await signCallback(opts.secret ?? callbackSecret, event.id, now, body),
        },
        body,
      });
    },
    verifyCallback: (input, opts = {}) => verifyCallback(input, { ...opts, secret: callbackSecret }),
    handler: (routes, opts = {}) => createCallbackHandler(routes, { ...opts, secret: callbackSecret }),
    reset() {
      sent.length = 0;
      emitted.length = 0;
      store.clear();
      pauses.clear();
      events.clear();
      counter = 0;
    },
    to: (name) => createUpstreamClient(send, name),
    send,
    requests: {
      list,
      iterate: (filter = {}) =>
        iteratePages((cursor) => list({ ...filter, ...(cursor !== undefined && { cursor }) })),
      get: load,
      async attempts(id) {
        return find(id).attempts.map((a) => ({ ...a }));
      },
      async replay(id) {
        const entry = find(id);
        if (!isFinalStatus(entry.request.status)) {
          throw invalidState("replay", entry.request.status, "dead, succeeded, canceled, unknown");
        }
        Object.assign(entry.request, { status: "pending", completedAt: null, updatedAt: new Date() });
        const snapshot = clone(entry.request);
        deliver(entry);
        await emit(entry);
        return snapshot;
      },
      async resolve(id, outcome) {
        const entry = find(id);
        const { status } = entry.request;
        if (status !== "unknown") throw invalidState("resolve", status, "unknown");
        const now = new Date();
        Object.assign(entry.request, {
          status: outcome,
          completedAt: now,
          updatedAt: now,
          nextAttemptAt: null,
          lastError: outcome === "succeeded" ? null : entry.request.lastError,
        });
        await emit(entry);
        return clone(entry.request);
      },
      async cancel(id) {
        const entry = find(id);
        const { status } = entry.request;
        if (status !== "scheduled" && status !== "pending" && status !== "failed") {
          throw invalidState("cancel", status, "scheduled, pending, failed");
        }
        const now = new Date();
        Object.assign(entry.request, {
          status: "canceled",
          completedAt: now,
          updatedAt: now,
          nextAttemptAt: null,
          lastError: { code: "canceled", message: "the request was canceled" },
        });
        await emit(entry);
        return clone(entry.request);
      },
      async callbacks(id) {
        const entry = find(id);
        return entry.callbacks.map((eventId): CallbackDelivery => {
          const event = emitted.find((e) => e.id === eventId) as CallbackEvent;
          return {
            id: eventId,
            requestId: id,
            type: event.type,
            url: entry.request.callbackUrl ?? "",
            requestStatus: event.data.status,
            status: "delivered",
            attemptCount: 1,
            nextAttemptAt: null,
            lastStatusCode: 204,
            lastError: null,
            lastAttemptAt: event.timestamp,
            createdAt: event.timestamp,
            deliveredAt: event.timestamp,
          };
        });
      },
      async retryCallback(id, callbackId) {
        const entry = find(id);
        if (!entry.callbacks.includes(callbackId)) {
          throw new NotFoundError(`Not found: callback "${callbackId}" of request "${id}" not found`, { status: 404, code: "not_found" });
        }
        throw new InvalidStateError("Invalid state: cannot retry a callback that is delivered (allowed: failed)", {
          status: 409,
          code: "invalid_state",
        });
      },
    },
    dlq: {
      async summary({ upstream: name } = {}) {
        const groups = new Map<string, DlqGroup>();
        for (const { request: r } of dead(name)) {
          const errorCode = r.lastError?.code ?? "internal";
          const key = `${r.upstream}\u0000${errorCode}\u0000${r.lastStatusCode ?? ""}`;
          const deadAt = r.completedAt ?? r.updatedAt;
          const group = groups.get(key);
          if (group) {
            group.count += 1;
            if (deadAt < group.oldestDeadAt) group.oldestDeadAt = deadAt;
            if (deadAt > group.newestDeadAt) group.newestDeadAt = deadAt;
          } else {
            groups.set(key, {
              upstream: r.upstream,
              errorCode,
              statusCode: r.lastStatusCode,
              count: 1,
              oldestDeadAt: deadAt,
              newestDeadAt: deadAt,
            });
          }
        }
        const list = [...groups.values()].sort((a, b) => b.count - a.count);
        return { total: list.reduce((n, g) => n + g.count, 0), groups: list };
      },
      async replay(filter: DlqReplayFilter = {}) {
        const matched = dead(filter.upstream).filter(({ request: r }) => {
          const deadAt = (r.completedAt ?? r.updatedAt).getTime();
          return (
            (filter.errorCode === undefined || r.lastError?.code === filter.errorCode) &&
            (filter.statusCode === undefined || r.lastStatusCode === filter.statusCode) &&
            (filter.deadAfter === undefined || deadAt >= new Date(filter.deadAfter).getTime()) &&
            (filter.deadBefore === undefined || deadAt < new Date(filter.deadBefore).getTime()) &&
            (filter.ids === undefined || filter.ids.includes(r.id))
          );
        });
        const dryRun = filter.dryRun ?? false;
        if (!dryRun) {
          for (const entry of matched) {
            Object.assign(entry.request, { status: "pending", completedAt: null });
            deliver(entry);
          }
        }
        return { matched: matched.length, replayed: dryRun ? 0 : matched.length, dryRun };
      },
    },
    upstreams: {
      async list() {
        return upstreamNames().map(upstream);
      },
      async get(name) {
        knownUpstream(name);
        return upstream(name);
      },
      async pause(name, options = {}) {
        knownUpstream(name);
        let until: Date | null = null;
        if (options.until !== undefined) until = new Date(toWireTimestamp(options.until, "until"));
        if (options.duration !== undefined) until = new Date(Date.now() + toMilliseconds(options.duration, "duration"));
        pauses.set(name, { since: pauses.get(name)?.since ?? new Date(), until, reason: options.reason ?? "", by: "fake" });
        addEvent(name, "paused", options.reason ?? "");
        return upstream(name);
      },
      async resume(name, options = {}) {
        knownUpstream(name);
        if (!pauses.delete(name)) {
          throw new InvalidStateError(`Conflict: upstream "${name}" is not paused`, { status: 409, code: "invalid_state" });
        }
        addEvent(name, "resumed", options.reason ?? "");
        return upstream(name);
      },
      async events(name, { limit = 50 } = {}) {
        knownUpstream(name);
        return (events.get(name) ?? []).slice(0, limit);
      },
    },
    stats: {
      async overview({ window } = {}) {
        const data = upstreamNames().map((name): UpstreamStats => {
          const reqs = [...store.values()].filter((e) => e.request.upstream === name).map((e) => e.request);
          const count = (s: RequestStatus) => reqs.filter((r) => r.status === s).length;
          const succeeded = count("succeeded");
          const deadCount = count("dead");
          const attempts = reqs.reduce((n, r) => n + r.attemptCount, 0);
          return {
            upstream: name,
            succeeded,
            failedAttempts: attempts - succeeded,
            dead: deadCount,
            successRate: succeeded + deadCount === 0 ? null : succeeded / (succeeded + deadCount),
            throughputPerMin: 0,
            latencyMs: { p50: null, p95: null, p99: null },
            queueDepth: count("pending") + count("failed"),
            oldestPendingAgeSeconds: null,
            dlqSize: deadCount,
          };
        });
        return { window: window === undefined ? "1h" : String(window), data };
      },
      async timeseries({ upstream: name, step } = {}) {
        return { upstream: name ?? null, step: step === undefined ? "1m" : String(step), data: [] };
      },
    },
  };
  return fake;
}

function invalidState(action: string, status: RequestStatus, allowed: string): InvalidStateError {
  return new InvalidStateError(`Invalid state: cannot ${action} a request that is ${status} (allowed: ${allowed})`, {
    status: 409,
    code: "invalid_state",
  });
}

function validate(input: SendInput): FieldError[] {
  const problems: FieldError[] = [];
  if (!input.upstream) problems.push({ field: "upstream", message: "is required" });
  if (!METHODS.includes(input.method)) problems.push({ field: "method", message: `must be one of ${METHODS.join(", ")}` });
  if (!input.path) problems.push({ field: "path", message: "is required" });
  else if (!input.path.startsWith("/")) problems.push({ field: "path", message: 'must start with "/"' });
  return problems.sort((a, b) => a.field.localeCompare(b.field));
}

/** Resolves a retry option the way the server does, with `standard` as the upstream's policy. */
function effectiveRetry(retry: WireCreateRequest["retry"]): EffectiveRetryPolicy {
  if (retry === undefined) return { ...PRESETS.standard };
  if (typeof retry === "string") return { ...(PRESETS[retry] ?? PRESETS.standard) };
  const base = PRESETS[retry.preset ?? "standard"];
  return {
    ...(retry.preset !== undefined && { preset: retry.preset }),
    maxAttempts: retry.max_attempts ?? base.maxAttempts,
    initialInterval: retry.initial_interval ?? base.initialInterval,
    maxInterval: retry.max_interval ?? base.maxInterval,
    multiplier: retry.multiplier ?? base.multiplier,
    maxAge: retry.max_age ?? base.maxAge,
  };
}

/** Copies the input, leaving out options that are `undefined`. */
function compact(input: SendInput): SentRequest {
  return Object.fromEntries(Object.entries(input).filter(([, v]) => v !== undefined)) as SentRequest;
}

function clone(r: HookyardRequest): HookyardRequest {
  return {
    ...r,
    headers: { ...r.headers },
    tags: { ...r.tags },
    retry: { ...r.retry },
    lastError: r.lastError && { ...r.lastError },
  };
}

export type { HookyardClient, SendInput } from "./types.js";
