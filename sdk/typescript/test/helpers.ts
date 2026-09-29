import { vi } from "vitest";
import type { components } from "../src/generated/openapi.js";
import { Hookyard } from "../src/index.js";
import type { FetchFunction, HookyardOptions } from "../src/index.js";

export type WireRequest = components["schemas"]["Request"];

export const BASE_URL = "http://hookyard.test";
export const TOKEN = "test-token";

/** A complete wire-format request, as returned by the server. */
export function wireRequest(overrides: Partial<WireRequest> = {}): WireRequest {
  return {
    id: "req_01J9ZK3Q4W5E6R7T8Y9U0I1O2P",
    upstream: "courier-x",
    method: "POST",
    path: "/shipments",
    headers: {},
    body: { order_id: 123 },
    dedupe_key: null,
    status: "pending",
    attempt_count: 0,
    retry: {
      preset: "standard",
      max_attempts: 10,
      initial_interval: "5s",
      max_interval: "10m",
      multiplier: 2,
      max_age: "24h",
    },
    timeout: "30s",
    tags: {},
    deliver_at: null,
    next_attempt_at: "2026-09-29T10:00:00Z",
    last_error: null,
    last_status_code: null,
    created_at: "2026-09-29T10:00:00Z",
    updated_at: "2026-09-29T10:00:00Z",
    completed_at: null,
    ...overrides,
  };
}

export function json(status: number, body: unknown, headers: Record<string, string> = {}): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json", ...headers },
  });
}

export function apiError(status: number, code: string, message: string, details?: unknown[]): Response {
  return json(status, { error: { code, message, ...(details && { details }) } });
}

export interface RecordedCall {
  url: URL;
  method: string;
  headers: Record<string, string>;
  /** Parsed JSON body, or undefined without one. */
  body: unknown;
}

type Reply = Response | Error | ((call: RecordedCall, signal: AbortSignal | undefined) => Response | Promise<Response>);

/**
 * A fake `fetch` that answers with the given replies in order (the last one repeats) and records
 * every call.
 */
export function mockFetch(...replies: Reply[]) {
  const calls: RecordedCall[] = [];
  const fn = vi.fn(async (input: string, init: RequestInit): Promise<Response> => {
    const headers = Object.fromEntries(new globalThis.Headers(init.headers).entries());
    const call: RecordedCall = {
      url: new URL(input),
      method: init.method ?? "GET",
      headers,
      body: typeof init.body === "string" ? JSON.parse(init.body) : undefined,
    };
    calls.push(call);
    const reply = replies[Math.min(calls.length - 1, replies.length - 1)];
    if (reply === undefined) throw new Error("mockFetch: no reply configured");
    if (reply instanceof Error) throw reply;
    if (typeof reply === "function") return reply(call, init.signal ?? undefined);
    return reply.clone();
  });
  return { fetch: fn, calls };
}

export function client(fetch: FetchFunction, options: HookyardOptions = {}): Hookyard {
  return new Hookyard({ url: BASE_URL, token: TOKEN, fetch, ...options });
}

/** A network failure like the one Node's fetch reports. */
export function networkError(): Error {
  const cause = Object.assign(new Error("connect ECONNREFUSED 127.0.0.1:8080"), { code: "ECONNREFUSED" });
  return new TypeError("fetch failed", { cause });
}

/** Makes backoff delays zero so retry tests run instantly. */
export function noJitter(): void {
  vi.spyOn(Math, "random").mockReturnValue(0);
}
