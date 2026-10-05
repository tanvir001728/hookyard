/**
 * Receiving completion callbacks: verifying their signature and routing them to handlers.
 *
 * Hookyard signs callbacks following Standard Webhooks (https://www.standardwebhooks.com/). The
 * code here uses only Web Crypto, so it runs on Node.js, Bun, Deno and edge runtimes.
 *
 * @module
 */
import { toMilliseconds } from "./duration.js";
import { HookyardError } from "./errors.js";
import type { components } from "./generated/openapi.js";
import type { DeliveryError, Duration, FinalStatus, HeaderMap, HttpMethod, Tags } from "./types.js";

type WireEvent = components["schemas"]["CallbackEvent"];

/** The type of a callback event: `request.` followed by the request's final status. */
export type CallbackEventType = WireEvent["type"];

/** The upstream's last response, as carried in a callback event. */
export interface CallbackResponse {
  statusCode: number;
  headers: HeaderMap;
  /** The response body as text (`JSON.parse` it for JSON APIs), truncated to the server's limit. */
  body: string;
  bodyTruncated: boolean;
}

/** A completion callback: a request reached a final state. */
export interface CallbackEvent {
  /** Unique event ID (`webhook-id`). The same on every delivery attempt: deduplicate on it. */
  id: string;
  type: CallbackEventType;
  /** When the request finished. */
  timestamp: Date;
  data: {
    requestId: string;
    upstream: string;
    method: HttpMethod;
    path: string;
    status: FinalStatus;
    /** The routing key given as `onResult` when the request was sent. */
    onResult: string | null;
    tags: Tags;
    dedupeKey: string | null;
    attemptCount: number;
    lastError: DeliveryError | null;
    /** The upstream's last response, or `null` if none arrived. */
    response: CallbackResponse | null;
    completedAt: Date | null;
  };
}

/** One signing secret (`whsec_…`), or several during a rotation. */
export type CallbackSecret = string | readonly string[];

export interface VerifyCallbackOptions {
  /**
   * The secret(s) shared with Hookyard's `HOOKYARD_CALLBACK_SECRETS`. Defaults to the
   * `HOOKYARD_CALLBACK_SECRET` environment variable (comma-separated for several).
   */
  secret?: CallbackSecret | undefined;
  /** How old (or how far in the future) a callback's timestamp may be. Default `"5m"`. */
  tolerance?: Duration | undefined;
}

/**
 * A received callback. A Fetch API `Request` (Next.js App Router, Hono, Bun, Deno, Cloudflare
 * Workers), or its headers and raw body (Express with `express.raw()`, Fastify, NestJS). The body
 * must be the exact bytes received: a re-serialized JSON object won't verify.
 */
export type CallbackInput =
  | Request
  | {
      headers: Headers | Record<string, string | readonly string[] | undefined>;
      body: string | Uint8Array;
    };

/** The callback's signature is missing, invalid or expired, or its body is malformed. */
export class CallbackVerificationError extends HookyardError {
  override name = "CallbackVerificationError";

  constructor(message: string) {
    super(message, { code: "invalid_signature", status: 401 });
  }
}

const DEFAULT_TOLERANCE_MS = 5 * 60_000;
const SECRET_PREFIX = "whsec_";

function readEnv(name: string): string | undefined {
  const env = (globalThis as { process?: { env?: Record<string, string | undefined> } }).process?.env;
  const value = env?.[name]?.trim();
  return value === "" ? undefined : value;
}

/** Resolves the configured secrets, from the option or the environment. */
export function resolveSecrets(secret: CallbackSecret | undefined): string[] {
  const raw = secret ?? readEnv("HOOKYARD_CALLBACK_SECRET") ?? readEnv("HOOKYARD_CALLBACK_SECRETS");
  if (raw === undefined) {
    throw new TypeError(
      "No callback secret: set HOOKYARD_CALLBACK_SECRET to one of the server's HOOKYARD_CALLBACK_SECRETS, " +
        "or pass { callbackSecret } to new Hookyard() ({ secret } to verifyCallback).",
    );
  }
  const list = (typeof raw === "string" ? raw.split(",") : [...raw]).map((s) => s.trim()).filter(Boolean);
  if (list.length === 0) throw new TypeError("The callback secret is empty.");
  for (const s of list) {
    if (!s.startsWith(SECRET_PREFIX)) {
      throw new TypeError(`Callback secrets start with "${SECRET_PREFIX}"; use the same value as the server's HOOKYARD_CALLBACK_SECRETS.`);
    }
  }
  return list;
}

type Subtle = SubtleCrypto;

let subtlePromise: Promise<Subtle> | undefined;

function subtle(): Promise<Subtle> {
  subtlePromise ??= (async () => {
    const global = (globalThis as { crypto?: Crypto }).crypto;
    if (global?.subtle) return global.subtle;
    // Node.js 18 has Web Crypto, but not as a global.
    const nodeCrypto = (await import("node:crypto")) as unknown as { webcrypto: Crypto };
    return nodeCrypto.webcrypto.subtle;
  })();
  return subtlePromise;
}

function base64ToBytes(b64: string): Uint8Array {
  const bin = atob(b64);
  const out = new Uint8Array(bin.length);
  for (let i = 0; i < bin.length; i++) out[i] = bin.charCodeAt(i);
  return out;
}

function bytesToBase64(bytes: Uint8Array): string {
  let bin = "";
  for (const b of bytes) bin += String.fromCharCode(b);
  return btoa(bin);
}

const keyCache = new Map<string, Promise<CryptoKey>>();

function hmacKey(secret: string): Promise<CryptoKey> {
  let key = keyCache.get(secret);
  if (!key) {
    key = (async () => {
      let raw: Uint8Array;
      try {
        raw = base64ToBytes(secret.slice(SECRET_PREFIX.length));
      } catch {
        throw new TypeError(`Invalid callback secret: "${SECRET_PREFIX}" must be followed by base64.`);
      }
      return (await subtle()).importKey("raw", raw as BufferSource, { name: "HMAC", hash: "SHA-256" }, false, ["sign"]);
    })();
    keyCache.set(secret, key);
  }
  return key;
}

/** Computes the `webhook-signature` header value for a message, one `v1,…` entry per secret. */
export async function signCallback(secret: CallbackSecret, id: string, timestamp: Date | number, body: string | Uint8Array): Promise<string> {
  const seconds = typeof timestamp === "number" ? timestamp : Math.floor(timestamp.getTime() / 1000);
  const bodyBytes = typeof body === "string" ? new TextEncoder().encode(body) : body;
  const prefix = new TextEncoder().encode(`${id}.${seconds}.`);
  const content = new Uint8Array(prefix.length + bodyBytes.length);
  content.set(prefix);
  content.set(bodyBytes, prefix.length);
  const sigs: string[] = [];
  for (const s of resolveSecrets(secret)) {
    const mac = await (await subtle()).sign("HMAC", await hmacKey(s), content as BufferSource);
    sigs.push(`v1,${bytesToBase64(new Uint8Array(mac))}`);
  }
  return sigs.join(" ");
}

function timingSafeEqual(a: string, b: string): boolean {
  if (a.length !== b.length) return false;
  let diff = 0;
  for (let i = 0; i < a.length; i++) diff |= a.charCodeAt(i) ^ b.charCodeAt(i);
  return diff === 0;
}

function header(headers: Headers | Record<string, string | readonly string[] | undefined>, name: string): string | undefined {
  if (typeof (headers as Headers).get === "function") return (headers as Headers).get(name) ?? undefined;
  const record = headers as Record<string, string | readonly string[] | undefined>;
  const value = record[name] ?? Object.entries(record).find(([k]) => k.toLowerCase() === name)?.[1];
  return Array.isArray(value) ? value[0] : (value as string | undefined);
}

function isFetchRequest(input: CallbackInput): input is Request {
  return typeof (input as Request).text === "function" && typeof (input as Request).headers?.get === "function";
}

function fromWireEvent(id: string, e: WireEvent): CallbackEvent {
  const d = e.data;
  const r = d.response as { status_code: number; headers: HeaderMap; body: string; body_truncated: boolean } | null;
  return {
    id,
    type: e.type,
    timestamp: new Date(e.timestamp),
    data: {
      requestId: d.request_id,
      upstream: d.upstream,
      method: d.method,
      path: d.path,
      status: d.status as FinalStatus,
      onResult: d.on_result ?? null,
      tags: d.tags ?? {},
      dedupeKey: d.dedupe_key ?? null,
      attemptCount: d.attempt_count,
      lastError: d.last_error ? { code: d.last_error.code, message: d.last_error.message } : null,
      response: r ? { statusCode: r.status_code, headers: r.headers ?? {}, body: r.body, bodyTruncated: r.body_truncated } : null,
      completedAt: d.completed_at ? new Date(d.completed_at) : null,
    },
  };
}

/**
 * Verifies a callback's signature and timestamp and returns its event. Throws a
 * {@link CallbackVerificationError} if it isn't a genuine, recent callback from Hookyard.
 *
 * ```ts
 * const event = await verifyCallback(request); // a Fetch API Request
 * const event = await verifyCallback({ headers: req.headers, body: req.body }); // Express + express.raw()
 * ```
 */
export async function verifyCallback(input: CallbackInput, options: VerifyCallbackOptions = {}): Promise<CallbackEvent> {
  const secrets = resolveSecrets(options.secret);
  const headers = isFetchRequest(input) ? input.headers : input.headers;
  const body = isFetchRequest(input) ? await input.text() : input.body;

  const id = header(headers, "webhook-id");
  const timestamp = header(headers, "webhook-timestamp");
  const signature = header(headers, "webhook-signature");
  if (!id || !timestamp || !signature) {
    throw new CallbackVerificationError(
      "Missing webhook-id, webhook-timestamp or webhook-signature header: this is not a Hookyard callback.",
    );
  }
  const seconds = Number(timestamp);
  if (!/^\d+$/.test(timestamp) || !Number.isSafeInteger(seconds)) {
    throw new CallbackVerificationError("Invalid webhook-timestamp header.");
  }
  const tolerance = options.tolerance === undefined ? DEFAULT_TOLERANCE_MS : toMilliseconds(options.tolerance, "tolerance");
  if (Math.abs(Date.now() - seconds * 1000) > tolerance) {
    throw new CallbackVerificationError(
      "The callback's timestamp is too old or too far in the future (a replay, or a clock that is off).",
    );
  }

  const expected = (await signCallback(secrets, id, seconds, body)).split(" ");
  const received = signature.split(" ").filter(Boolean);
  if (!received.some((sig) => expected.some((want) => timingSafeEqual(sig, want)))) {
    throw new CallbackVerificationError(
      "Invalid callback signature. Check that the secret matches one of the server's HOOKYARD_CALLBACK_SECRETS " +
        "and that the body is the raw, unparsed request body.",
    );
  }

  let parsed: WireEvent;
  try {
    parsed = JSON.parse(typeof body === "string" ? body : new TextDecoder().decode(body)) as WireEvent;
  } catch {
    throw new CallbackVerificationError("The callback body is not valid JSON.");
  }
  if (typeof parsed?.type !== "string" || typeof parsed.data !== "object" || parsed.data === null) {
    throw new CallbackVerificationError("The callback body is not a Hookyard event.");
  }
  return fromWireEvent(id, parsed);
}

/** Handles one event. Throw (or reject) to have Hookyard retry the callback later. */
export type CallbackEventHandler = (event: CallbackEvent) => unknown;

/** Handlers per final status. Statuses without a handler are acknowledged and ignored. */
export type OutcomeHandlers = Partial<Record<FinalStatus, CallbackEventHandler>>;

/**
 * Routes, keyed by the request's `onResult` or, without one, its upstream name. A route is a
 * handler for every outcome, or handlers per outcome. `"*"` catches everything else.
 */
export type CallbackRoutes = Record<string, OutcomeHandlers | CallbackEventHandler>;

export interface CallbackHandlerOptions extends VerifyCallbackOptions {
  /** Called for events no route handles. By default they are acknowledged and ignored. */
  onUnhandled?: CallbackEventHandler | undefined;
  /** Called when a handler throws, before Hookyard is told to retry. Defaults to `console.error`. */
  onError?: ((error: unknown, event: CallbackEvent) => void) | undefined;
}

/** A minimal Node.js `IncomingMessage`. */
interface NodeRequest extends AsyncIterable<unknown> {
  headers: Record<string, string | string[] | undefined>;
  /** Set by body parsers such as `express.raw()` (a Buffer) or `express.text()` (a string). */
  body?: unknown;
  /** Set by some frameworks (NestJS with `rawBody: true`, Fastify plugins). */
  rawBody?: unknown;
}

/** A minimal Node.js `ServerResponse`. */
interface NodeResponse {
  statusCode: number;
  setHeader(name: string, value: string): unknown;
  end(body?: string): unknown;
}

/** A callback endpoint that verifies, routes and acknowledges Hookyard's callbacks. */
export interface CallbackHandler {
  /**
   * Fetch API handler: `Request` in, `Response` out. Next.js App Router
   * (`export const POST = handler.fetch`), Hono, Bun, Deno, Cloudflare Workers.
   */
  fetch(request: Request): Promise<Response>;
  /**
   * Node.js handler for `http.createServer` and Express (`app.post("/hooks/hookyard", handler.node)`).
   * Mount it without a JSON body parser, or with `express.raw({ type: "application/json" })`: the
   * signature covers the raw body.
   */
  node(req: NodeRequest, res: NodeResponse): Promise<void>;
  /** Verifies a callback and dispatches its event. Throws if it isn't genuine or a handler fails. */
  handle(input: CallbackInput): Promise<CallbackEvent>;
  /** Runs the routes for an already verified event, for example in tests. */
  dispatch(event: CallbackEvent): Promise<void>;
}

function route(routes: CallbackRoutes, event: CallbackEvent): CallbackEventHandler | undefined {
  const { onResult, upstream, status } = event.data;
  const candidates = [onResult, upstream, "*"].filter((k): k is string => k !== null && Object.hasOwn(routes, k));
  for (const key of candidates) {
    const r = routes[key];
    if (typeof r === "function") return r;
    const h = r?.[status];
    if (h) return h;
  }
  return undefined;
}

async function readNodeBody(req: NodeRequest): Promise<string | Uint8Array> {
  for (const candidate of [req.rawBody, req.body]) {
    if (typeof candidate === "string" || candidate instanceof Uint8Array) return candidate;
  }
  if (req.body !== undefined && req.body !== null && typeof req.body === "object" && Object.keys(req.body).length > 0) {
    throw new TypeError(
      "The request body was already parsed as JSON, so its signature can't be checked. Mount the Hookyard " +
        'handler before express.json(), or use express.raw({ type: "application/json" }) for its route.',
    );
  }
  const chunks: Uint8Array[] = [];
  for await (const chunk of req) {
    chunks.push(typeof chunk === "string" ? new TextEncoder().encode(chunk) : (chunk as Uint8Array));
  }
  const out = new Uint8Array(chunks.reduce((n, c) => n + c.length, 0));
  let offset = 0;
  for (const c of chunks) {
    out.set(c, offset);
    offset += c.length;
  }
  return out;
}

function errorBody(message: string): string {
  return JSON.stringify({ error: message });
}

/**
 * Creates a callback endpoint from routes.
 *
 * ```ts
 * const handler = createCallbackHandler({
 *   "order.shipment": {
 *     succeeded: (e) => markShipped(e.data.tags.order, JSON.parse(e.data.response!.body)),
 *     dead: (e) => alertOps(e),
 *   },
 * });
 * ```
 */
export function createCallbackHandler(routes: CallbackRoutes, options: CallbackHandlerOptions = {}): CallbackHandler {
  // Fail at startup, not on the first callback, if no secret is configured.
  resolveSecrets(options.secret);
  const onError = options.onError ?? ((error: unknown, event: CallbackEvent) => console.error(`Hookyard callback ${event.id} failed:`, error));

  const dispatch = async (event: CallbackEvent): Promise<void> => {
    const h = route(routes, event) ?? options.onUnhandled;
    if (h) await h(event);
  };

  const handle = async (input: CallbackInput): Promise<CallbackEvent> => {
    const event = await verifyCallback(input, options);
    try {
      await dispatch(event);
    } catch (error) {
      onError(error, event);
      throw error;
    }
    return event;
  };

  // Hookyard retries any non-2xx answer: a bad signature is answered 401 (and will fail again
  // until the secret is fixed), a failing handler 500.
  const outcome = async (input: () => Promise<CallbackInput>): Promise<{ status: number; body: string }> => {
    let event: CallbackEvent;
    try {
      event = await verifyCallback(await input(), options);
    } catch (error) {
      if (error instanceof CallbackVerificationError) return { status: 401, body: errorBody(error.message) };
      throw error;
    }
    try {
      await dispatch(event);
    } catch (error) {
      onError(error, event);
      return { status: 500, body: errorBody("the callback handler failed; Hookyard will retry") };
    }
    return { status: 204, body: "" };
  };

  return {
    handle,
    dispatch,
    async fetch(request) {
      const { status, body } = await outcome(async () => request);
      return new Response(status === 204 ? null : body, {
        status,
        headers: status === 204 ? {} : { "Content-Type": "application/json" },
      });
    },
    async node(req, res) {
      let result: { status: number; body: string };
      try {
        result = await outcome(async () => ({ headers: req.headers, body: await readNodeBody(req) }));
      } catch (error) {
        // A setup problem, such as a body already parsed as JSON: say so instead of hanging.
        console.error("Hookyard callback handler:", error);
        result = { status: 500, body: errorBody(error instanceof Error ? error.message : String(error)) };
      }
      const { status, body } = result;
      res.statusCode = status;
      if (body) res.setHeader("Content-Type", "application/json");
      res.end(body || undefined);
    },
  };
}
