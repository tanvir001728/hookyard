/**
 * An in-memory Hookyard for unit tests: `import { createFakeHookyard } from "@hookyard/sdk/testing"`.
 *
 * The fake implements the same {@link HookyardClient} interface as the real client, records every
 * `send()` call in `fake.sent`, and "delivers" requests instantly, so `job.result()` resolves right
 * away with the configured outcome (`succeeded` by default). It makes no network calls.
 *
 * @module
 */
import { iteratePages } from "./client.js";
import { toWireCreateRequest } from "./mappers.js";
import type { WireCreateRequest } from "./mappers.js";
import { InvalidStateError, NotFoundError, UnknownUpstreamError, ValidationError } from "./errors.js";
import type { FieldError } from "./errors.js";
import { Job } from "./job.js";
import { isFinalStatus } from "./types.js";
import type {
  Attempt,
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
}

/** An in-memory {@link HookyardClient} for tests. */
export interface FakeHookyard extends HookyardClient {
  /** Every `send()` call, in order, including ones that were deduplicated. */
  readonly sent: SentRequest[];
  /** Forgets every sent request. */
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
      });
    }
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
    };
    const enqueued = clone(request);
    const entry: Stored = { request, sent: recorded, attempts: [] };
    store.set(request.id, entry);
    deliver(entry);
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

  const upstream = (name: string): Upstream => ({
    name,
    baseUrl: `https://${name}.invalid`,
    timeout: DEFAULT_TIMEOUT,
    retry: { ...PRESETS.standard },
    headerNames: [],
    limits: { rateLimit: null, burst: null, maxConcurrency: null },
  });

  const fake: FakeHookyard = {
    sent,
    reset() {
      sent.length = 0;
      store.clear();
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
        return snapshot;
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
        return clone(entry.request);
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
        if (!upstreamNames().includes(name)) {
          throw new NotFoundError(`Not found: upstream "${name}" is not configured`, { status: 404, code: "not_found" });
        }
        return upstream(name);
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
