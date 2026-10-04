// Conversions between the snake_case wire format (generated from api/openapi.yaml) and the
// camelCase types of the public API.
import { toWireDuration, toWireTimestamp } from "./duration.js";
import type { components } from "./generated/openapi.js";
import type {
  Attempt,
  DeliveryError,
  DlqReplayFilter,
  DlqReplayResult,
  DlqSummary,
  EffectiveRetryPolicy,
  HookyardRequest,
  LatencyPercentiles,
  ListRequestsFilter,
  RequestPage,
  RetryPolicy,
  SendInput,
  StatsOverview,
  StatsTimeseries,
  Upstream,
  UpstreamEvent,
} from "./types.js";

type Schemas = components["schemas"];
export type WireRequest = Schemas["Request"];
export type WireCreateRequest = Schemas["CreateRequest"];

function date(value: string): Date {
  return new Date(value);
}

function optionalDate(value: string | null | undefined): Date | null {
  return value == null ? null : new Date(value);
}

function deliveryError(value: Schemas["DeliveryError"] | null | undefined): DeliveryError | null {
  return value == null ? null : { code: value.code, message: value.message };
}

function effectiveRetry(value: Schemas["EffectiveRetryPolicy"]): EffectiveRetryPolicy {
  return {
    ...(value.preset !== undefined && { preset: value.preset }),
    maxAttempts: value.max_attempts,
    initialInterval: value.initial_interval,
    maxInterval: value.max_interval,
    multiplier: value.multiplier,
    maxAge: value.max_age,
  };
}

function latency(value: Schemas["LatencyPercentiles"]): LatencyPercentiles {
  return { p50: value.p50 ?? null, p95: value.p95 ?? null, p99: value.p99 ?? null };
}

export function fromWireRequest(r: WireRequest): HookyardRequest {
  return {
    id: r.id,
    upstream: r.upstream,
    method: r.method,
    path: r.path,
    headers: r.headers ?? {},
    body: r.body ?? null,
    dedupeKey: r.dedupe_key ?? null,
    status: r.status,
    attemptCount: r.attempt_count,
    retry: effectiveRetry(r.retry),
    timeout: r.timeout ?? "",
    tags: r.tags,
    deliverAt: optionalDate(r.deliver_at),
    nextAttemptAt: optionalDate(r.next_attempt_at),
    lastError: deliveryError(r.last_error),
    lastStatusCode: r.last_status_code ?? null,
    createdAt: date(r.created_at),
    updatedAt: date(r.updated_at),
    completedAt: optionalDate(r.completed_at),
  };
}

export function fromWireRequestList(r: Schemas["RequestList"]): RequestPage {
  return { data: r.data.map(fromWireRequest), nextCursor: r.next_cursor ?? null };
}

export function fromWireAttempt(a: Schemas["Attempt"]): Attempt {
  return {
    number: a.number,
    startedAt: date(a.started_at),
    durationMs: a.duration_ms,
    outcome: a.outcome,
    statusCode: a.status_code ?? null,
    error: deliveryError(a.error),
    response:
      a.response == null
        ? null
        : { headers: a.response.headers, body: a.response.body, bodyTruncated: a.response.body_truncated },
    retryAt: optionalDate(a.retry_at),
    classifiedBy: a.classified_by ?? null,
  };
}

export function fromWireDlqSummary(s: Schemas["DLQSummary"]): DlqSummary {
  return {
    total: s.total,
    groups: s.groups.map((g) => ({
      upstream: g.upstream,
      errorCode: g.error_code,
      statusCode: g.status_code ?? null,
      count: g.count,
      oldestDeadAt: date(g.oldest_dead_at),
      newestDeadAt: date(g.newest_dead_at),
    })),
  };
}

export function fromWireDlqReplayResult(r: Schemas["DLQReplayResult"]): DlqReplayResult {
  return { matched: r.matched, replayed: r.replayed, dryRun: r.dry_run };
}

export function fromWireUpstream(u: Schemas["Upstream"]): Upstream {
  return {
    name: u.name,
    baseUrl: u.base_url,
    timeout: u.timeout,
    retry: effectiveRetry(u.retry),
    headerNames: u.header_names,
    limits: {
      rateLimit: u.limits.rate_limit ?? null,
      burst: u.limits.burst ?? null,
      maxConcurrency: u.limits.max_concurrency ?? null,
    },
    state: {
      status: u.state.status,
      breaker: u.state.breaker,
      breakerSince: optionalDate(u.state.breaker_since),
      pause: u.state.pause
        ? { since: new Date(u.state.pause.since), until: optionalDate(u.state.pause.until), reason: u.state.pause.reason, by: u.state.pause.by }
        : null,
      inFlight: u.state.in_flight,
      availableTokens: u.state.available_tokens ?? null,
      throttledUntil: optionalDate(u.state.throttled_until),
    },
    onTimeout: u.on_timeout,
  };
}

export function fromWireUpstreamEvent(e: Schemas["UpstreamEvent"]): UpstreamEvent {
  return { id: e.id, at: new Date(e.at), kind: e.kind, reason: e.reason, actor: e.actor, details: e.details };
}

export function fromWireStatsOverview(s: Schemas["StatsOverview"]): StatsOverview {
  return {
    window: s.window,
    data: s.data.map((u) => ({
      upstream: u.upstream,
      succeeded: u.succeeded,
      failedAttempts: u.failed_attempts,
      dead: u.dead,
      successRate: u.success_rate ?? null,
      throughputPerMin: u.throughput_per_min,
      latencyMs: latency(u.latency_ms),
      queueDepth: u.queue_depth,
      oldestPendingAgeSeconds: u.oldest_pending_age_seconds ?? null,
      dlqSize: u.dlq_size,
    })),
  };
}

export function fromWireStatsTimeseries(s: Schemas["StatsTimeseries"]): StatsTimeseries {
  return {
    upstream: s.upstream ?? null,
    step: s.step,
    data: s.data.map((b) => ({
      start: date(b.start),
      succeeded: b.succeeded,
      failedAttempts: b.failed_attempts,
      dead: b.dead,
      latencyMs: latency(b.latency_ms),
    })),
  };
}

function toWireRetry(retry: RetryPolicy): NonNullable<WireCreateRequest["retry"]> {
  if (typeof retry === "string") return retry;
  const out: Schemas["RetryPolicyObject"] = {};
  if (retry.preset !== undefined) out.preset = retry.preset;
  if (retry.maxAttempts !== undefined) out.max_attempts = retry.maxAttempts;
  if (retry.initialInterval !== undefined) {
    out.initial_interval = toWireDuration(retry.initialInterval, "retry.initialInterval");
  }
  if (retry.maxInterval !== undefined) out.max_interval = toWireDuration(retry.maxInterval, "retry.maxInterval");
  if (retry.multiplier !== undefined) out.multiplier = retry.multiplier;
  if (retry.maxAge !== undefined) out.max_age = toWireDuration(retry.maxAge, "retry.maxAge");
  return out;
}

/** Builds the body of `POST /v1/requests`. Unset options are left out. */
export function toWireCreateRequest(input: SendInput, dedupeKey: string | undefined): WireCreateRequest {
  const out: WireCreateRequest = { upstream: input.upstream, method: input.method, path: input.path };
  if (input.headers !== undefined) out.headers = { ...input.headers };
  if (input.body !== undefined) out.body = input.body;
  if (dedupeKey !== undefined) out.dedupe_key = dedupeKey;
  if (input.deliverAt !== undefined) out.deliver_at = toWireTimestamp(input.deliverAt, "deliverAt");
  if (input.timeout !== undefined) out.timeout = toWireDuration(input.timeout, "timeout");
  if (input.retry !== undefined) out.retry = toWireRetry(input.retry);
  if (input.tags !== undefined) out.tags = { ...input.tags };
  return out;
}

/** Builds the query string of `GET /v1/requests`. */
export function toListQuery(filter: ListRequestsFilter): URLSearchParams {
  const q = new URLSearchParams();
  if (filter.upstream !== undefined) q.set("upstream", filter.upstream);
  if (filter.status !== undefined) {
    const statuses: readonly string[] = typeof filter.status === "string" ? [filter.status] : filter.status;
    if (statuses.length > 0) q.set("status", statuses.join(","));
  }
  for (const [key, value] of Object.entries(filter.tags ?? {})) q.append("tag", `${key}:${value}`);
  if (filter.dedupeKey !== undefined) q.set("dedupe_key", filter.dedupeKey);
  if (filter.createdAfter !== undefined) q.set("created_after", toWireTimestamp(filter.createdAfter, "createdAfter"));
  if (filter.createdBefore !== undefined) {
    q.set("created_before", toWireTimestamp(filter.createdBefore, "createdBefore"));
  }
  if (filter.limit !== undefined) q.set("limit", String(filter.limit));
  if (filter.cursor !== undefined) q.set("cursor", filter.cursor);
  return q;
}

export function toWireDlqReplay(filter: DlqReplayFilter): Schemas["DLQReplayRequest"] {
  const out: Schemas["DLQReplayRequest"] = { dry_run: filter.dryRun ?? false };
  if (filter.upstream !== undefined) out.upstream = filter.upstream;
  if (filter.errorCode !== undefined) out.error_code = filter.errorCode;
  if (filter.statusCode !== undefined) out.status_code = filter.statusCode;
  if (filter.deadAfter !== undefined) out.dead_after = toWireTimestamp(filter.deadAfter, "deadAfter");
  if (filter.deadBefore !== undefined) out.dead_before = toWireTimestamp(filter.deadBefore, "deadBefore");
  if (filter.ids !== undefined) out.ids = [...filter.ids];
  return out;
}
