import type { components } from "./openapi";

// Wire types, generated from api/openapi.yaml (`pnpm generate`).
export type Schemas = components["schemas"];
export type HookyardRequest = Schemas["Request"];
export type RequestStatus = Schemas["RequestStatus"];
export type Attempt = Schemas["Attempt"];
export type Upstream = Schemas["Upstream"];
export type UpstreamEvent = Schemas["UpstreamEvent"];
export type UpstreamStats = Schemas["UpstreamStats"];
export type StatsOverview = Schemas["StatsOverview"];
export type StatsTimeseries = Schemas["StatsTimeseries"];
export type DLQSummary = Schemas["DLQSummary"];
export type DLQGroup = Schemas["DLQGroup"];
export type DLQReplayRequest = Schemas["DLQReplayRequest"];
export type DLQReplayResult = Schemas["DLQReplayResult"];
export type RequestList = Schemas["RequestList"];
export type Callback = Schemas["Callback"];

export interface FieldProblem {
  field: string;
  message: string;
}

/** An error response from the Hookyard API. */
export class ApiError extends Error {
  constructor(
    readonly status: number,
    readonly code: string,
    message: string,
    readonly details: FieldProblem[] = [],
  ) {
    super(message);
    this.name = "ApiError";
  }
}

type Query = Record<string, string | number | boolean | string[] | undefined | null>;

export interface RequestOptions {
  method?: "GET" | "POST" | "DELETE";
  query?: Query;
  body?: unknown;
  signal?: AbortSignal;
}

let onUnauthorized: (() => void) | undefined;

/** Registers a callback for 401 responses, used to show the sign-in page. */
export function setUnauthorizedHandler(fn: () => void): void {
  onUnauthorized = fn;
}

/** Builds a URL with query parameters, dropping empty values. Arrays repeat the key. */
export function buildUrl(path: string, query?: Query): string {
  const params = new URLSearchParams();
  for (const [key, value] of Object.entries(query ?? {})) {
    if (value === undefined || value === null || value === "") continue;
    if (Array.isArray(value)) value.forEach((v) => params.append(key, v));
    else params.set(key, String(value));
  }
  const qs = params.toString();
  return qs ? `${path}?${qs}` : path;
}

/**
 * Calls the Hookyard API with the dashboard session cookie. State-changing
 * requests carry the CSRF header the server requires for cookie sessions.
 */
export async function api<T>(path: string, opts: RequestOptions = {}): Promise<T> {
  const method = opts.method ?? "GET";
  const headers: Record<string, string> = { Accept: "application/json" };
  if (method !== "GET") headers["Hookyard-Csrf"] = "1";
  if (opts.body !== undefined) headers["Content-Type"] = "application/json";

  let res: Response;
  try {
    res = await fetch(buildUrl(path, opts.query), {
      method,
      headers,
      credentials: "same-origin",
      body: opts.body === undefined ? undefined : JSON.stringify(opts.body),
      signal: opts.signal,
    });
  } catch (err) {
    if (err instanceof DOMException && err.name === "AbortError") throw err;
    throw new ApiError(0, "network", "Can't reach Hookyard. Check that the server is running.");
  }

  if (res.status === 204) return undefined as T;
  const data: unknown = await res.json().catch(() => undefined);

  if (!res.ok) {
    const e = (data as { error?: { code?: string; message?: string; details?: FieldProblem[] } } | undefined)?.error;
    if (res.status === 401) onUnauthorized?.();
    throw new ApiError(res.status, e?.code ?? "unknown", e?.message ?? `Request failed with status ${res.status}`, e?.details ?? []);
  }
  return data as T;
}
