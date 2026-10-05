import type { CallbackEvent, CallbackHandler, CallbackHandlerOptions, CallbackInput, CallbackRoutes } from "./callbacks.js";
import type { Job } from "./job.js";

/**
 * A duration: a string with a unit (`"500ms"`, `"30s"`, `"5m"`, `"1h30m"`) or a number of
 * milliseconds (`1500` is sent as `"1500ms"`).
 */
export type Duration = string | number;

/** A point in time: a `Date` or an RFC 3339 string such as `"2026-10-01T10:00:00Z"`. */
export type Timestamp = Date | string;

/** HTTP methods Hookyard can deliver. */
export type HttpMethod = "GET" | "POST" | "PUT" | "PATCH" | "DELETE";

/**
 * Lifecycle of a request.
 *
 * - `scheduled`: waiting for `deliverAt`
 * - `pending`: waiting to be picked up by a worker
 * - `in_flight`: being delivered right now
 * - `failed`: the last attempt failed and a retry is scheduled (see `nextAttemptAt`)
 * - `succeeded`: delivered successfully (final)
 * - `dead`: retries exhausted or a permanent error; in the dead-letter queue (final)
 * - `unknown`: the outcome is uncertain (final)
 * - `canceled`: canceled before delivery (final)
 */
export type RequestStatus =
  | "scheduled"
  | "pending"
  | "in_flight"
  | "failed"
  | "succeeded"
  | "dead"
  | "unknown"
  | "canceled";

/** Statuses a request never leaves (unless it is replayed). */
export type FinalStatus = "succeeded" | "dead" | "unknown" | "canceled";

/** The statuses in which a request is finished. */
export const FINAL_STATUSES: readonly FinalStatus[] = ["succeeded", "dead", "unknown", "canceled"];

/** Reports whether `status` is final: the request will not be delivered again unless replayed. */
export function isFinalStatus(status: RequestStatus): status is FinalStatus {
  return (FINAL_STATUSES as readonly string[]).includes(status);
}

/**
 * Named retry policies.
 *
 * | Preset | Attempts | Initial interval | Max interval | Max age |
 * | --- | --- | --- | --- | --- |
 * | `none` | 1 | – | – | – |
 * | `quick` | 5 | 1s | 30s | 10m |
 * | `standard` | 10 | 5s | 10m | 24h |
 * | `patient` | 25 | 30s | 1h | 72h |
 */
export type RetryPreset = "none" | "quick" | "standard" | "patient";

/**
 * A custom retry policy. Unset fields come from `preset` (if set), then from the upstream's
 * policy. Delays grow exponentially with full jitter, and a `Retry-After` header from the
 * upstream always takes precedence.
 */
export interface RetryPolicyOptions {
  /** Start from this preset and override individual fields. */
  preset?: RetryPreset | undefined;
  /** Total attempts including the first one (1 to 100). */
  maxAttempts?: number | undefined;
  /** Upper bound of the first retry delay. */
  initialInterval?: Duration | undefined;
  /** Upper bound of any retry delay. */
  maxInterval?: Duration | undefined;
  /** Growth factor of the delay between attempts (1 to 10). */
  multiplier?: number | undefined;
  /** Give up (move to the DLQ) once this much time has passed since the request became due. */
  maxAge?: Duration | undefined;
}

/** A retry preset name or a custom policy. */
export type RetryPolicy = RetryPreset | RetryPolicyOptions;

/** A fully resolved retry policy, as reported by the server. */
export interface EffectiveRetryPolicy {
  preset?: RetryPreset;
  maxAttempts: number;
  initialInterval: string;
  maxInterval: string;
  multiplier: number;
  maxAge: string;
}

/** Free-form labels for filtering and grouping (up to 20). */
export type Tags = Record<string, string>;

/** HTTP headers. Header names are case-insensitive. */
export type HeaderMap = Record<string, string>;

/** Per-request delivery options. Every field is optional. */
export interface SendOptions {
  /**
   * Makes enqueueing idempotent: while a request with the same key exists for the same upstream
   * (24 hours by default), enqueueing again returns that request instead of creating a new one.
   */
  dedupeKey?: string | undefined;
  /** Retry policy for this request. Defaults to the upstream's policy. */
  retry?: RetryPolicy | undefined;
  /** Deliver no earlier than this time. The request stays `scheduled` until then. */
  deliverAt?: Timestamp | undefined;
  /** Per-attempt timeout for the call to the upstream. Overrides the upstream's timeout. */
  timeout?: Duration | undefined;
  /** Headers for this request. Headers configured on the upstream take precedence. */
  headers?: HeaderMap | undefined;
  /** Labels for filtering and grouping, for example the calling app or tenant. */
  tags?: Tags | undefined;
  /**
   * Where Hookyard POSTs a signed event when the request finishes. Overrides the upstream's
   * `callback_url`; `""` turns callbacks off for this request. Handle it with `hy.handler()`.
   */
  callbackUrl?: string | undefined;
  /** A routing key carried in the callback event, matched by the routes of `hy.handler()`. */
  onResult?: string | undefined;
}

/** Input of {@link HookyardClient.send}. */
export interface SendInput extends SendOptions {
  /** Name of the upstream, as configured in `hookyard.yaml`. */
  upstream: string;
  method: HttpMethod;
  /** Path (and optional query string) appended to the upstream's base URL. Must start with `/`. */
  path: string;
  /**
   * Request body. Objects, arrays, numbers and booleans are sent as JSON with
   * `Content-Type: application/json`. A string is sent verbatim, so set a `Content-Type` header
   * for other formats such as XML or form data. Omit it (or pass `null`) to send no body.
   */
  body?: unknown | undefined;
}

/** Why a delivery attempt failed. */
export type DeliveryErrorCode =
  | "timeout"
  | "connection"
  | "http_status"
  | "classified_failure"
  | "max_age_exceeded"
  | "canceled"
  | "internal";

export interface DeliveryError {
  code: DeliveryErrorCode;
  message: string;
}

/** An outbound request that Hookyard delivers, with its current delivery state. */
export interface HookyardRequest {
  /** Unique ID, prefixed with `req_`. */
  id: string;
  upstream: string;
  method: HttpMethod;
  path: string;
  headers: HeaderMap;
  /** The body as it was enqueued, or `null` for no body. */
  body: unknown;
  dedupeKey: string | null;
  status: RequestStatus;
  /** Number of attempts made so far. */
  attemptCount: number;
  retry: EffectiveRetryPolicy;
  /** Per-attempt timeout, such as `"30s"`. */
  timeout: string;
  tags: Tags;
  deliverAt: Date | null;
  /** When the next attempt is due, for `scheduled`, `pending` and `failed` requests. */
  nextAttemptAt: Date | null;
  /** Why the most recent attempt failed, if it did. */
  lastError: DeliveryError | null;
  /** HTTP status code of the most recent response, if any. */
  lastStatusCode: number | null;
  createdAt: Date;
  updatedAt: Date;
  /** When the request reached a final state. */
  completedAt: Date | null;
  /** Where the completion callback goes, or `null` for none. */
  callbackUrl: string | null;
  /** The routing key carried in the callback event. */
  onResult: string | null;
}

/** A completion callback of a request and its delivery to your app. */
export interface CallbackDelivery {
  /** The event ID, sent as `webhook-id`. */
  id: string;
  requestId: string;
  type: "request.succeeded" | "request.dead" | "request.unknown" | "request.canceled";
  url: string;
  /** The request's status when it finished. */
  requestStatus: RequestStatus;
  /** `failed` once every attempt failed; send it again with `requests.retryCallback()`. */
  status: "pending" | "delivering" | "delivered" | "failed";
  attemptCount: number;
  nextAttemptAt: Date | null;
  lastStatusCode: number | null;
  lastError: string | null;
  lastAttemptAt: Date | null;
  createdAt: Date;
  deliveredAt: Date | null;
}

/** The response an upstream returned for an attempt. */
export interface AttemptResponse {
  headers: HeaderMap;
  /** The response body, truncated to the server's limit (64 KiB by default). */
  body: string;
  bodyTruncated: boolean;
}

/** One HTTP try of a request. */
export interface Attempt {
  /** 1 for the first attempt. */
  number: number;
  startedAt: Date;
  durationMs: number;
  /** How Hookyard classified the attempt. */
  /** `unknown`: the request was sent but no response arrived. */
  outcome: "success" | "retryable_failure" | "permanent_failure" | "unknown";
  statusCode: number | null;
  error: DeliveryError | null;
  response: AttemptResponse | null;
  /** When the next attempt was scheduled after this one failed, if any. */
  retryAt: Date | null;
  /** The upstream's classification rule that decided the outcome, or `null` for the default rules. */
  classifiedBy: string | null;
}

/** Filters for {@link RequestsApi.list}. All filters are combined with AND. */
export interface ListRequestsFilter {
  upstream?: string | undefined;
  /** One status or several. */
  status?: RequestStatus | readonly RequestStatus[] | undefined;
  /** Only requests that have all of these tags. */
  tags?: Tags | undefined;
  dedupeKey?: string | undefined;
  /** Created at or after this time. */
  createdAfter?: Timestamp | undefined;
  /** Created before this time. */
  createdBefore?: Timestamp | undefined;
  /** Page size, 1 to 200 (server default 50). */
  limit?: number | undefined;
  /** `nextCursor` from a previous page. */
  cursor?: string | undefined;
}

/** One page of requests, newest first. */
export interface RequestPage {
  data: HookyardRequest[];
  /** Pass as `cursor` to get the next page; `null` on the last page. */
  nextCursor: string | null;
}

/** Dead requests grouped by upstream and failure reason. */
export interface DlqGroup {
  upstream: string;
  errorCode: DeliveryErrorCode;
  /** The final HTTP status code, for `http_status` errors. */
  statusCode: number | null;
  count: number;
  oldestDeadAt: Date;
  newestDeadAt: Date;
}

export interface DlqSummary {
  /** Total number of dead requests matching the filter. */
  total: number;
  /** Largest group first. */
  groups: DlqGroup[];
}

/** Filter for {@link DlqApi.replay}. Filters are combined with AND; an empty filter matches the whole DLQ. */
export interface DlqReplayFilter {
  upstream?: string | undefined;
  errorCode?: DeliveryErrorCode | undefined;
  statusCode?: number | undefined;
  /** Only requests that died at or after this time. */
  deadAfter?: Timestamp | undefined;
  /** Only requests that died before this time. */
  deadBefore?: Timestamp | undefined;
  /** Only these requests (up to 1000). */
  ids?: readonly string[] | undefined;
  /** Count matching requests without replaying them. */
  dryRun?: boolean | undefined;
}

export interface DlqReplayResult {
  /** Dead requests that matched the filter. */
  matched: number;
  /** Requests queued again (0 for a dry run). */
  replayed: number;
  dryRun: boolean;
}

/** A configured upstream. Header values are never returned because they often hold secrets. */
export interface Upstream {
  name: string;
  baseUrl: string;
  timeout: string;
  retry: EffectiveRetryPolicy;
  /** Names of headers added to every request. */
  headerNames: string[];
  /** Throttling applied by each Hookyard instance; requests over the limits wait in the queue. */
  limits: UpstreamLimits;
  /** Live delivery state on the Hookyard instance that answered. */
  state: UpstreamState;
  /**
   * What happens when a POST or PATCH was sent but no response arrived: `unknown` (left for a person or
   * the app to settle) or `retry`.
   */
  onTimeout: "unknown" | "retry";
  /** How long a `dedupeKey` is remembered. */
  dedupeWindow: string;
  /** Circuit breaker settings, or `null` when the breaker is off. */
  breaker: UpstreamBreaker | null;
  /** Classification rules, in the order they are tried. */
  classify: ClassificationRule[];
  /** The default callback URL of this upstream's requests. */
  callbackUrl: string | null;
}

export interface UpstreamBreaker {
  /** Opens when this share of calls in `window` fails (with at least `minCalls`). */
  failureRate: number;
  minCalls: number;
  window: string;
  /** Opens after this many failures in a row. */
  consecutiveFailures: number;
  cooldown: string;
  /** Requests sent while half-open to decide whether to close. */
  probes: number;
}

/** A response classification rule, as configured in `hookyard.yaml`. */
export interface ClassificationRule {
  /** The rule's name, or `rule N`. */
  name: string;
  /** Status codes it matches, such as `"200"` or `"5xx"`. */
  status: string | null;
  /** Dot path into the JSON body. */
  body: string | null;
  /** What the body value must satisfy, such as `equals "FAILED"`. */
  condition: string | null;
  then: "success" | "retry" | "fail";
}

/**
 * - `active`: deliveries flow normally
 * - `paused`: paused by an operator (see `pause`)
 * - `breaker_open`: the circuit breaker opened after failures; deliveries wait
 * - `breaker_half_open`: probe requests are deciding whether to resume
 * - `throttled`: held back after a `429` with `Retry-After`
 */
export type UpstreamStatus = "active" | "paused" | "breaker_open" | "breaker_half_open" | "throttled";

export interface UpstreamState {
  status: UpstreamStatus;
  breaker: "closed" | "open" | "half_open" | "off";
  /** When the breaker entered its current state. */
  breakerSince: Date | null;
  pause: UpstreamPause | null;
  /** Deliveries in progress right now. */
  inFlight: number;
  /** Requests the rate limit allows right now, or `null` without a rate limit. */
  availableTokens: number | null;
  throttledUntil: Date | null;
}

export interface UpstreamPause {
  since: Date;
  /** When the pause ends on its own, or `null` until resumed. */
  until: Date | null;
  reason: string;
  /** Name of the API token that paused it. */
  by: string;
}

export interface PauseOptions {
  reason?: string | undefined;
  /** End the pause at this time (at most 30 days ahead). */
  until?: Timestamp | undefined;
  /** End the pause after this long, such as `"2h"`. Use `until` or `duration`, not both. */
  duration?: Duration | undefined;
}

/** A circuit breaker transition, pause or resume. */
export interface UpstreamEvent {
  id: number;
  at: Date;
  /** `breaker_open`, `breaker_half_open`, `breaker_closed`, `paused` or `resumed`. */
  kind: string;
  reason: string;
  /** `hookyard` for automatic changes, otherwise the API token name. */
  actor: string;
  details: Record<string, unknown>;
}

export interface UpstreamLimits {
  /** Sustained rate such as `"10/s"`, or `null` for unlimited. */
  rateLimit: string | null;
  /** Requests that may be sent at once after a quiet period, or `null` without a rate limit. */
  burst: number | null;
  /** Maximum deliveries in flight at once, or `null` for unlimited. */
  maxConcurrency: number | null;
}

/** Attempt latency percentiles in milliseconds, `null` when there were no attempts. */
export interface LatencyPercentiles {
  p50: number | null;
  p95: number | null;
  p99: number | null;
}

export interface UpstreamStats {
  upstream: string;
  /** Requests delivered successfully in the window. */
  succeeded: number;
  /** Attempts that failed in the window, including ones that were retried. */
  failedAttempts: number;
  /** Requests that moved to the DLQ in the window. */
  dead: number;
  /** `succeeded / (succeeded + dead)` from 0 to 1, or `null` with no finished requests. */
  successRate: number | null;
  /** Attempts per minute, averaged over the window. */
  throughputPerMin: number;
  latencyMs: LatencyPercentiles;
  /** Requests currently waiting to be delivered. */
  queueDepth: number;
  /** Age of the oldest request waiting to be delivered. */
  oldestPendingAgeSeconds: number | null;
  /** Requests currently in the DLQ. */
  dlqSize: number;
}

export interface StatsOverview {
  window: string;
  /** One entry per upstream. */
  data: UpstreamStats[];
}

export interface StatsBucket {
  start: Date;
  succeeded: number;
  failedAttempts: number;
  dead: number;
  latencyMs: LatencyPercentiles;
}

export interface StatsTimeseries {
  /** The upstream, or `null` for all upstreams combined. */
  upstream: string | null;
  step: string;
  /** Oldest first. */
  data: StatsBucket[];
}

export interface StatsOverviewOptions {
  /** How far back to aggregate (1m to 30 days, server default 1h). */
  window?: Duration | undefined;
}

export interface StatsTimeseriesOptions {
  upstream?: string | undefined;
  /** Start of the range. Defaults to one hour before `to`. */
  from?: Timestamp | undefined;
  /** End of the range. Defaults to now. */
  to?: Timestamp | undefined;
  /** Bucket size, rounded up to whole minutes (server default 1m). */
  step?: Duration | undefined;
}

/** Sends requests to one upstream. Returned by {@link HookyardClient.to}. */
export interface UpstreamClient {
  /** The upstream's name. */
  readonly upstream: string;
  get(path: string, options?: SendOptions): Promise<Job>;
  delete(path: string, options?: SendOptions): Promise<Job>;
  post(path: string, body?: unknown, options?: SendOptions): Promise<Job>;
  put(path: string, body?: unknown, options?: SendOptions): Promise<Job>;
  patch(path: string, body?: unknown, options?: SendOptions): Promise<Job>;
}

export interface RequestsApi {
  /** Lists one page of requests, newest first. */
  list(filter?: ListRequestsFilter): Promise<RequestPage>;
  /** Iterates over every request matching the filter, fetching pages as needed. */
  iterate(filter?: Omit<ListRequestsFilter, "cursor">): AsyncIterableIterator<HookyardRequest>;
  get(id: string): Promise<HookyardRequest>;
  /** Every delivery attempt of a request, oldest first. */
  attempts(id: string): Promise<Attempt[]>;
  /** Queues a finished request for delivery again, with a fresh retry budget. */
  replay(id: string): Promise<HookyardRequest>;
  /** Cancels a request that has not finished and is not in flight. */
  cancel(id: string): Promise<HookyardRequest>;
  /**
   * Settles a request whose status is `unknown` (it was sent, but no response arrived), after checking
   * with the upstream: `succeeded`, or `dead` to move it to the dead-letter queue.
   */
  resolve(id: string, outcome: "succeeded" | "dead", options?: { reason?: string | undefined }): Promise<HookyardRequest>;
  /** The completion callbacks of a request, oldest first. */
  callbacks(id: string): Promise<CallbackDelivery[]>;
  /** Sends a failed callback again, with a fresh attempt budget and the same event. */
  retryCallback(id: string, callbackId: string): Promise<CallbackDelivery>;
}

export interface DlqApi {
  /** Dead requests grouped by upstream and failure reason. */
  summary(options?: { upstream?: string | undefined }): Promise<DlqSummary>;
  /** Queues every dead request matching the filter for delivery again. Try `dryRun: true` first. */
  replay(filter?: DlqReplayFilter): Promise<DlqReplayResult>;
}

export interface UpstreamsApi {
  /** All configured upstreams, sorted by name. */
  list(): Promise<Upstream[]>;
  get(name: string): Promise<Upstream>;
  /**
   * Pauses deliveries to an upstream. Requests wait in the queue without using up attempts, and the
   * paused time doesn't count against their `max_age`.
   */
  pause(name: string, options?: PauseOptions): Promise<Upstream>;
  /** Resumes a paused upstream. Throws `InvalidStateError` if it isn't paused. */
  resume(name: string, options?: { reason?: string | undefined }): Promise<Upstream>;
  /** Breaker transitions, pauses and resumes, newest first. */
  events(name: string, options?: { limit?: number | undefined }): Promise<UpstreamEvent[]>;
}

export interface StatsApi {
  /** Per-upstream health over a recent window. */
  overview(options?: StatsOverviewOptions): Promise<StatsOverview>;
  /** Delivery metrics over time. */
  timeseries(options?: StatsTimeseriesOptions): Promise<StatsTimeseries>;
}

/**
 * The public interface of a Hookyard client. {@link Hookyard} implements it, and so does the
 * in-memory fake from `@hookyard/sdk/testing`, so application code can depend on this type.
 */
export interface HookyardClient {
  /**
   * Verifies a completion callback's signature and returns its event. Throws a
   * `CallbackVerificationError` if it isn't a genuine, recent callback from Hookyard.
   */
  verifyCallback(input: CallbackInput, options?: { tolerance?: Duration | undefined }): Promise<CallbackEvent>;
  /** Creates an endpoint for completion callbacks that verifies and routes them. */
  handler(routes: CallbackRoutes, options?: Omit<CallbackHandlerOptions, "secret">): CallbackHandler;
  /** Returns a client that sends requests to `upstream`. */
  to(upstream: string): UpstreamClient;
  /** Enqueues a request. The low-level equivalent of `to(upstream).post(...)` and friends. */
  send(input: SendInput): Promise<Job>;
  readonly requests: RequestsApi;
  readonly dlq: DlqApi;
  readonly upstreams: UpstreamsApi;
  readonly stats: StatsApi;
}
