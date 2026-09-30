import { ConnectionError, HookyardError, TimeoutError, errorFromResponse } from "./errors.js";
import { formatMilliseconds } from "./duration.js";
import { VERSION } from "./version.js";

/** The `fetch` function the SDK uses. */
export type FetchFunction = (input: string, init: RequestInit) => Promise<Response>;

export interface TransportConfig {
  /** Base URL of the Hookyard server, without a trailing slash. */
  baseUrl: string;
  token: string;
  fetch: FetchFunction;
  /** Timeout of each HTTP call, in milliseconds. */
  timeoutMs: number;
  /** How many times a failed call is retried. */
  maxRetries: number;
}

export interface CallOptions {
  method: "GET" | "POST";
  path: string;
  query?: URLSearchParams | undefined;
  /** JSON body. */
  body?: unknown;
  /**
   * Whether the call may be retried after a network error, a timeout, `429` or `5xx`. Only safe
   * for calls that have no effect when repeated.
   */
  retry: boolean;
}

export interface CallResult<T> {
  status: number;
  headers: globalThis.Headers;
  data: T;
}

const BACKOFF_BASE_MS = 200;
const BACKOFF_MAX_MS = 5_000;
const RETRY_AFTER_MAX_MS = 30_000;

export class Transport {
  constructor(private readonly config: TransportConfig) {}

  get baseUrl(): string {
    return this.config.baseUrl;
  }

  async call<T>(options: CallOptions): Promise<CallResult<T>> {
    const query = options.query?.toString();
    const url = `${this.config.baseUrl}${options.path}${query ? `?${query}` : ""}`;
    const context = `${options.method} ${options.path}`;
    const headers: Record<string, string> = {
      Accept: "application/json",
      Authorization: `Bearer ${this.config.token}`,
      "User-Agent": `hookyard-sdk-typescript/${VERSION}`,
    };
    let body: string | null = null;
    if (options.body !== undefined) {
      headers["Content-Type"] = "application/json";
      body = JSON.stringify(options.body);
    }

    const maxAttempts = options.retry ? this.config.maxRetries + 1 : 1;
    for (let attempt = 1; ; attempt++) {
      const canRetry = attempt < maxAttempts;
      let response: Response;
      try {
        response = await this.fetchWithTimeout(url, { method: options.method, headers, body });
      } catch (err) {
        if (canRetry) {
          await sleep(backoff(attempt));
          continue;
        }
        throw this.transportError(err, context, attempt);
      }

      if (canRetry && (response.status === 429 || response.status >= 500)) {
        // Discard the body so the connection can be reused. Not awaited: nothing depends on it.
        response.body?.cancel().catch(() => undefined);
        await sleep(retryAfter(response) ?? backoff(attempt));
        continue;
      }
      return await parseResponse<T>(response, context);
    }
  }

  private async fetchWithTimeout(url: string, init: RequestInit): Promise<Response> {
    const controller = new AbortController();
    let timer: ReturnType<typeof setTimeout> | undefined;
    const timedOut = new Promise<never>((_, reject) => {
      timer = setTimeout(() => {
        const err = new CallTimeout();
        controller.abort(err);
        reject(err);
      }, this.config.timeoutMs);
    });
    try {
      // Racing also covers custom fetch implementations that ignore the abort signal.
      return await Promise.race([this.config.fetch(url, { ...init, signal: controller.signal }), timedOut]);
    } finally {
      clearTimeout(timer);
    }
  }

  private transportError(err: unknown, context: string, attempts: number): HookyardError {
    const tries = attempts > 1 ? ` (${attempts} attempts)` : "";
    if (err instanceof CallTimeout) {
      return new TimeoutError(
        `Hookyard at ${this.config.baseUrl} did not respond to ${context} within ` +
          `${formatMilliseconds(this.config.timeoutMs)}${tries}. Check that the server is healthy, ` +
          `or raise the timeout option.`,
        {},
      );
    }
    return new ConnectionError(
      `Could not reach Hookyard at ${this.config.baseUrl} for ${context}: ${describeNetworkError(err)}${tries}. ` +
        `Check HOOKYARD_URL and that the server is running.`,
      { code: "connection_error", cause: err },
    );
  }
}

/** Marks an HTTP call aborted by the SDK's own timeout. */
class CallTimeout extends Error {
  constructor() {
    super("timed out");
  }
}

async function parseResponse<T>(response: Response, context: string): Promise<CallResult<T>> {
  const text = await response.text();
  let data: unknown;
  let parsed = false;
  if (text !== "") {
    try {
      data = JSON.parse(text);
      parsed = true;
    } catch {
      // Handled below.
    }
  }
  if (!response.ok) {
    throw errorFromResponse(response.status, data, text, context);
  }
  if (!parsed) {
    throw new HookyardError(
      `${context} returned HTTP ${response.status} without a JSON body. Check that the URL points at a Hookyard server.`,
      { status: response.status, code: "unexpected_response" },
    );
  }
  return { status: response.status, headers: response.headers, data: data as T };
}

/** Exponential backoff with full jitter. `attempt` is the attempt that just failed (1-based). */
function backoff(attempt: number): number {
  return Math.random() * Math.min(BACKOFF_MAX_MS, BACKOFF_BASE_MS * 2 ** (attempt - 1));
}

/** Reads `Retry-After` (seconds or an HTTP date) from a `429` or `5xx` response, if present. */
function retryAfter(response: Response): number | undefined {
  const value = response.headers.get("Retry-After");
  if (!value) return undefined;
  let ms: number;
  if (/^\d+$/.test(value.trim())) {
    ms = Number(value.trim()) * 1_000;
  } else {
    const date = Date.parse(value);
    if (Number.isNaN(date)) return undefined;
    ms = date - Date.now();
  }
  return Math.min(Math.max(ms, 0), RETRY_AFTER_MAX_MS);
}

function describeNetworkError(err: unknown): string {
  if (!(err instanceof Error)) return String(err);
  // Node's fetch reports "fetch failed" and puts the real reason (ECONNREFUSED, ...) in `cause`.
  const cause: unknown = err.cause;
  if (cause instanceof Error) {
    const code = (cause as { code?: unknown }).code;
    return typeof code === "string" && !cause.message.includes(code)
      ? `${err.message} (${code}: ${cause.message})`
      : `${err.message} (${cause.message})`;
  }
  return err.message;
}

export function sleep(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms));
}
