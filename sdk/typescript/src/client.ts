import { toMilliseconds, toWireDuration, toWireTimestamp } from "./duration.js";
import type { components } from "./generated/openapi.js";
import { Job } from "./job.js";
import {
  fromWireAttempt,
  fromWireDlqReplayResult,
  fromWireDlqSummary,
  fromWireRequest,
  fromWireRequestList,
  fromWireStatsOverview,
  fromWireStatsTimeseries,
  fromWireUpstream,
  fromWireUpstreamEvent,
  toListQuery,
  toWireCreateRequest,
  toWireDlqReplay,
} from "./mappers.js";
import { Transport } from "./transport.js";
import type { FetchFunction } from "./transport.js";
import type {
  Attempt,
  DlqApi,
  DlqReplayFilter,
  DlqReplayResult,
  DlqSummary,
  Duration,
  HookyardClient,
  HookyardRequest,
  ListRequestsFilter,
  PauseOptions,
  RequestPage,
  RequestsApi,
  SendInput,
  StatsApi,
  StatsOverview,
  StatsOverviewOptions,
  StatsTimeseries,
  StatsTimeseriesOptions,
  Upstream,
  UpstreamClient,
  UpstreamEvent,
  UpstreamsApi,
} from "./types.js";
import { createUpstreamClient } from "./upstream.js";

type Schemas = components["schemas"];

export interface HookyardOptions {
  /** Base URL of the Hookyard server, such as `"http://localhost:8080"`. Defaults to `HOOKYARD_URL`. */
  url?: string | undefined;
  /** API token (one of the server's `HOOKYARD_API_TOKENS`). Defaults to `HOOKYARD_TOKEN`. */
  token?: string | undefined;
  /** A `fetch` implementation, for example to add tracing. Defaults to the global `fetch`. */
  fetch?: FetchFunction | undefined;
  /** Timeout of each HTTP call to Hookyard (not to the upstream). Default `"10s"`. */
  timeout?: Duration | undefined;
  /**
   * How many times the SDK retries a call to Hookyard after a network error, a timeout, `429` or
   * `5xx`. Retrying never creates duplicate requests. Default `2`; `0` disables retries.
   */
  maxRetries?: number | undefined;
}

const DEFAULT_TIMEOUT_MS = 10_000;
const DEFAULT_MAX_RETRIES = 2;
const DEDUPLICATED_HEADER = "Hookyard-Deduplicated";

function readEnv(name: string): string | undefined {
  const env = (globalThis as { process?: { env?: Record<string, string | undefined> } }).process?.env;
  const value = env?.[name]?.trim();
  return value === "" ? undefined : value;
}

function normalizeUrl(raw: string): string {
  let url: URL;
  try {
    url = new URL(raw);
  } catch {
    throw new TypeError(`Invalid Hookyard URL ${JSON.stringify(raw)}: use an absolute URL such as "http://localhost:8080".`);
  }
  if (url.protocol !== "http:" && url.protocol !== "https:") {
    throw new TypeError(`Invalid Hookyard URL ${JSON.stringify(raw)}: the scheme must be http or https.`);
  }
  if (url.search || url.hash) {
    throw new TypeError(`Invalid Hookyard URL ${JSON.stringify(raw)}: remove the query string and fragment.`);
  }
  return url.toString().replace(/\/+$/, "");
}

/** Generates a dedupe key that makes the SDK's own retries of one `send()` call safe. */
function generateDedupeKey(): string {
  const c = (globalThis as { crypto?: { randomUUID?: () => string } }).crypto;
  if (typeof c?.randomUUID === "function") return `sdk_${c.randomUUID()}`;
  let id = "";
  for (let i = 0; i < 32; i++) id += Math.floor(Math.random() * 16).toString(16);
  return `sdk_${id}`;
}

/**
 * The Hookyard client.
 *
 * ```ts
 * const hy = new Hookyard(); // reads HOOKYARD_URL and HOOKYARD_TOKEN
 * await hy.to("courier-x").post("/shipments", { orderId: 123 });
 * ```
 */
export class Hookyard implements HookyardClient {
  readonly requests: RequestsApi;
  readonly dlq: DlqApi;
  readonly upstreams: UpstreamsApi;
  readonly stats: StatsApi;

  readonly #transport: Transport;
  readonly #maxRetries: number;

  constructor(options: HookyardOptions = {}) {
    const url = options.url ?? readEnv("HOOKYARD_URL");
    if (!url) {
      throw new TypeError(
        "Hookyard URL is not set. Set HOOKYARD_URL or pass { url } to new Hookyard(), " +
          'for example new Hookyard({ url: "http://localhost:8080" }).',
      );
    }
    const token = options.token ?? readEnv("HOOKYARD_TOKEN");
    if (!token) {
      throw new TypeError(
        "Hookyard API token is not set. Set HOOKYARD_TOKEN or pass { token } to new Hookyard(). " +
          "Use one of the tokens in the server's HOOKYARD_API_TOKENS.",
      );
    }
    const maxRetries = options.maxRetries ?? DEFAULT_MAX_RETRIES;
    if (!Number.isInteger(maxRetries) || maxRetries < 0) {
      throw new TypeError(`Invalid maxRetries ${String(maxRetries)}: use a whole number, 0 or more.`);
    }
    const timeoutMs = options.timeout === undefined ? DEFAULT_TIMEOUT_MS : toMilliseconds(options.timeout, "timeout");
    if (timeoutMs <= 0) throw new TypeError("Invalid timeout: must be greater than 0.");

    const fetchFn: FetchFunction | undefined = options.fetch ?? globalThis.fetch?.bind(globalThis);
    if (!fetchFn) {
      throw new TypeError("No fetch implementation found. Use Node.js 18 or later, or pass { fetch }.");
    }

    this.#maxRetries = maxRetries;
    this.#transport = new Transport({ baseUrl: normalizeUrl(url), token, fetch: fetchFn, timeoutMs, maxRetries });

    const transport = this.#transport;
    const getRequest = async (id: string): Promise<HookyardRequest> => {
      const res = await transport.call<Schemas["Request"]>({ method: "GET", path: `/v1/requests/${enc(id)}`, retry: true });
      return fromWireRequest(res.data);
    };

    const listRequests = async (filter: ListRequestsFilter = {}): Promise<RequestPage> => {
      const res = await transport.call<Schemas["RequestList"]>({
        method: "GET",
        path: "/v1/requests",
        query: toListQuery(filter),
        retry: true,
      });
      return fromWireRequestList(res.data);
    };

    this.requests = {
      list: listRequests,
      iterate: (filter = {}) =>
        iteratePages((cursor) => listRequests({ ...filter, ...(cursor !== undefined && { cursor }) })),
      get: getRequest,
      async attempts(id: string): Promise<Attempt[]> {
        const res = await transport.call<Schemas["AttemptList"]>({
          method: "GET",
          path: `/v1/requests/${enc(id)}/attempts`,
          retry: true,
        });
        return res.data.data.map(fromWireAttempt);
      },
      // Replay and cancel are not retried: if a first try reached the server, a retry would fail
      // with a confusing 409 even though the action succeeded.
      async replay(id: string): Promise<HookyardRequest> {
        const res = await transport.call<Schemas["Request"]>({
          method: "POST",
          path: `/v1/requests/${enc(id)}/replay`,
          retry: false,
        });
        return fromWireRequest(res.data);
      },
      async cancel(id: string): Promise<HookyardRequest> {
        const res = await transport.call<Schemas["Request"]>({
          method: "POST",
          path: `/v1/requests/${enc(id)}/cancel`,
          retry: false,
        });
        return fromWireRequest(res.data);
      },
    };

    this.dlq = {
      async summary(options: { upstream?: string | undefined } = {}): Promise<DlqSummary> {
        const query = new URLSearchParams();
        if (options.upstream !== undefined) query.set("upstream", options.upstream);
        const res = await transport.call<Schemas["DLQSummary"]>({ method: "GET", path: "/v1/dlq", query, retry: true });
        return fromWireDlqSummary(res.data);
      },
      // Bulk replay is idempotent: replayed requests are no longer dead and are not matched again.
      async replay(filter: DlqReplayFilter = {}): Promise<DlqReplayResult> {
        const res = await transport.call<Schemas["DLQReplayResult"]>({
          method: "POST",
          path: "/v1/dlq/replay",
          body: toWireDlqReplay(filter),
          retry: true,
        });
        return fromWireDlqReplayResult(res.data);
      },
    };

    this.upstreams = {
      async list(): Promise<Upstream[]> {
        const res = await transport.call<Schemas["UpstreamList"]>({ method: "GET", path: "/v1/upstreams", retry: true });
        return res.data.data.map(fromWireUpstream);
      },
      async get(name: string): Promise<Upstream> {
        const res = await transport.call<Schemas["Upstream"]>({
          method: "GET",
          path: `/v1/upstreams/${enc(name)}`,
          retry: true,
        });
        return fromWireUpstream(res.data);
      },
      async pause(name: string, options: PauseOptions = {}): Promise<Upstream> {
        const body: Record<string, string> = {};
        if (options.reason !== undefined) body.reason = options.reason;
        if (options.until !== undefined) body.until = toWireTimestamp(options.until, "until");
        if (options.duration !== undefined) body.duration = toWireDuration(options.duration, "duration");
        // Pausing again only updates the reason and end, so retrying is safe.
        const res = await transport.call<Schemas["Upstream"]>({ method: "POST", path: `/v1/upstreams/${enc(name)}/pause`, body, retry: true });
        return fromWireUpstream(res.data);
      },
      async resume(name: string, options: { reason?: string | undefined } = {}): Promise<Upstream> {
        const body = options.reason !== undefined ? { reason: options.reason } : {};
        // Not retried: a retry after a resume that went through would report "not paused".
        const res = await transport.call<Schemas["Upstream"]>({ method: "POST", path: `/v1/upstreams/${enc(name)}/resume`, body, retry: false });
        return fromWireUpstream(res.data);
      },
      async events(name: string, options: { limit?: number | undefined } = {}): Promise<UpstreamEvent[]> {
        const query = new URLSearchParams();
        if (options.limit !== undefined) query.set("limit", String(options.limit));
        const res = await transport.call<{ data: Schemas["UpstreamEvent"][] }>({ method: "GET", path: `/v1/upstreams/${enc(name)}/events`, query, retry: true });
        return res.data.data.map(fromWireUpstreamEvent);
      },
    };

    this.stats = {
      async overview(options: StatsOverviewOptions = {}): Promise<StatsOverview> {
        const query = new URLSearchParams();
        if (options.window !== undefined) query.set("window", toWireDuration(options.window, "window"));
        const res = await transport.call<Schemas["StatsOverview"]>({
          method: "GET",
          path: "/v1/stats/overview",
          query,
          retry: true,
        });
        return fromWireStatsOverview(res.data);
      },
      async timeseries(options: StatsTimeseriesOptions = {}): Promise<StatsTimeseries> {
        const query = new URLSearchParams();
        if (options.upstream !== undefined) query.set("upstream", options.upstream);
        if (options.from !== undefined) query.set("from", toWireTimestamp(options.from, "from"));
        if (options.to !== undefined) query.set("to", toWireTimestamp(options.to, "to"));
        if (options.step !== undefined) query.set("step", toWireDuration(options.step, "step"));
        const res = await transport.call<Schemas["StatsTimeseries"]>({
          method: "GET",
          path: "/v1/stats/timeseries",
          query,
          retry: true,
        });
        return fromWireStatsTimeseries(res.data);
      },
    };
  }

  /** Base URL of the Hookyard server this client talks to. */
  get url(): string {
    return this.#transport.baseUrl;
  }

  to(upstream: string): UpstreamClient {
    return createUpstreamClient((input) => this.send(input), upstream);
  }

  /**
   * Enqueues a request and returns as soon as Hookyard has stored it.
   *
   * If the call to Hookyard fails with a network error, a timeout, `429` or `5xx`, it is retried
   * with the same dedupe key, so a retry never creates a second request. Without a `dedupeKey`,
   * the SDK generates one (`sdk_<uuid>`) that covers the retries of this call only.
   */
  async send(input: SendInput): Promise<Job> {
    const generated = input.dedupeKey === undefined && this.#maxRetries > 0;
    const dedupeKey = generated ? generateDedupeKey() : input.dedupeKey;
    const res = await this.#transport.call<Schemas["Request"]>({
      method: "POST",
      path: "/v1/requests",
      body: toWireCreateRequest(input, dedupeKey),
      retry: dedupeKey !== undefined,
    });
    // A generated key is unique to this call, so a dedupe hit can only be one of our own retries
    // finding the request that an earlier, seemingly failed try created.
    const deduplicated = !generated && res.status === 200 && res.headers.get(DEDUPLICATED_HEADER) === "true";
    return new Job(fromWireRequest(res.data), deduplicated, this.requests.get);
  }
}

function enc(segment: string): string {
  return encodeURIComponent(segment);
}

/** Iterates over the items of every page, following `nextCursor`. */
export async function* iteratePages(
  fetchPage: (cursor: string | undefined) => Promise<RequestPage>,
): AsyncGenerator<HookyardRequest, void, undefined> {
  let cursor: string | undefined;
  do {
    const page = await fetchPage(cursor);
    yield* page.data;
    cursor = page.nextCursor ?? undefined;
  } while (cursor !== undefined);
}
