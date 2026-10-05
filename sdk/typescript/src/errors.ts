import type { components } from "./generated/openapi.js";
import type { HookyardRequest } from "./types.js";

/** Stable error codes returned by the Hookyard API. Safe to branch on. */
export type ApiErrorCode = components["schemas"]["ErrorCode"];

/**
 * Error codes for failures detected by the SDK itself:
 *
 * - `connection_error`: Hookyard could not be reached
 * - `timeout`: Hookyard did not respond in time, or a request did not finish in time
 * - `unexpected_response`: the server's response was not a valid Hookyard API response
 * - `invalid_signature`: a received callback's signature is missing, wrong or expired
 */
export type ClientErrorCode = "connection_error" | "timeout" | "unexpected_response" | "invalid_signature";

export type ErrorCode = ApiErrorCode | ClientErrorCode;

/** One invalid field of a `validation_failed` error. */
export interface FieldError {
  /** Dotted path of the invalid field, such as `retry.max_attempts`. */
  field: string;
  message: string;
}

export interface HookyardErrorOptions {
  code: ErrorCode;
  status?: number | undefined;
  details?: readonly FieldError[] | undefined;
  cause?: unknown;
}

/** Base class of every error thrown by the SDK. Branch on `code` or use `instanceof`. */
export class HookyardError extends Error {
  override name = "HookyardError";
  /** HTTP status of Hookyard's response, or `undefined` if there was no response. */
  readonly status: number | undefined;
  /** Stable, machine-readable code. */
  readonly code: ErrorCode;
  /** Per-field problems, for validation errors. Empty otherwise. */
  readonly details: readonly FieldError[];

  constructor(message: string, options: HookyardErrorOptions) {
    super(message, options.cause === undefined ? undefined : { cause: options.cause });
    this.status = options.status;
    this.code = options.code;
    this.details = options.details ?? [];
  }
}

/** The request to Hookyard was malformed (400). */
export class BadRequestError extends HookyardError {
  override name = "BadRequestError";
}

/** The API token is missing or invalid (401). */
export class AuthError extends HookyardError {
  override name = "AuthError";
}

/** The request or upstream does not exist (404). */
export class NotFoundError extends HookyardError {
  override name = "NotFoundError";
}

/** The request is not in a state that allows the action, such as canceling an in-flight request (409). */
export class InvalidStateError extends HookyardError {
  override name = "InvalidStateError";
}

/** The request body exceeds the server's limit (413). */
export class PayloadTooLargeError extends HookyardError {
  override name = "PayloadTooLargeError";
}

/** One or more fields are invalid (422 `validation_failed`). See `details`. */
export class ValidationError extends HookyardError {
  override name = "ValidationError";
}

/** The upstream is not configured in Hookyard (422 `unknown_upstream`). */
export class UnknownUpstreamError extends HookyardError {
  override name = "UnknownUpstreamError";
}

/** Hookyard could not be reached, even after retrying. */
export class ConnectionError extends HookyardError {
  override name = "ConnectionError";
}

/**
 * Something did not happen in time: Hookyard did not respond to an HTTP call, or a request did not
 * reach a final status within `job.result({ timeout })`.
 */
export class TimeoutError extends HookyardError {
  override name = "TimeoutError";
  /** For `job.result()` timeouts: the last known state of the request. */
  readonly request: HookyardRequest | undefined;

  constructor(message: string, options: Omit<HookyardErrorOptions, "code"> & { request?: HookyardRequest }) {
    super(message, { ...options, code: "timeout" });
    this.request = options.request;
  }
}

const API_ERROR_CODES: readonly string[] = [
  "bad_request",
  "unauthorized",
  "not_found",
  "invalid_state",
  "payload_too_large",
  "validation_failed",
  "unknown_upstream",
  "internal",
] satisfies readonly ApiErrorCode[];

type WireError = components["schemas"]["Error"];

function isWireError(body: unknown): body is WireError {
  if (typeof body !== "object" || body === null) return false;
  const error = (body as { error?: unknown }).error;
  return (
    typeof error === "object" &&
    error !== null &&
    typeof (error as { code?: unknown }).code === "string" &&
    typeof (error as { message?: unknown }).message === "string"
  );
}

function codeForStatus(status: number): ApiErrorCode {
  switch (status) {
    case 400:
      return "bad_request";
    case 401:
      return "unauthorized";
    case 404:
      return "not_found";
    case 409:
      return "invalid_state";
    case 413:
      return "payload_too_large";
    case 422:
      return "validation_failed";
    default:
      return "internal";
  }
}

/** Builds the error for a non-2xx response from Hookyard. `body` is the parsed JSON, if any. */
export function errorFromResponse(status: number, body: unknown, rawBody: string, context: string): HookyardError {
  if (!isWireError(body)) {
    const snippet = rawBody.length > 200 ? `${rawBody.slice(0, 200)}…` : rawBody;
    return new HookyardError(
      `${context} failed with HTTP ${status} and a response that is not a Hookyard API error` +
        (snippet ? `: ${JSON.stringify(snippet)}` : "") +
        `. Check that the URL points at a Hookyard server.`,
      { status, code: "unexpected_response" },
    );
  }

  const { message } = body.error;
  const code: ApiErrorCode = API_ERROR_CODES.includes(body.error.code) ? body.error.code : codeForStatus(status);
  const details: FieldError[] = (body.error.details ?? []).map((d) => ({ field: d.field, message: d.message }));
  const options = { status, code, details };

  switch (code) {
    case "bad_request":
      return new BadRequestError(`Hookyard rejected the request: ${message}`, options);
    case "unauthorized":
      return new AuthError(
        `Hookyard rejected the API token: ${message}. Check HOOKYARD_TOKEN (or the token option): ` +
          `it must match a token in the server's HOOKYARD_API_TOKENS.`,
        options,
      );
    case "not_found":
      return new NotFoundError(
        message.startsWith("no route for")
          ? `${message}. Check that the URL points at the root of a Hookyard server (without /v1).`
          : `Not found: ${message}`,
        options,
      );
    case "invalid_state":
      return new InvalidStateError(`Invalid state: ${message}`, options);
    case "payload_too_large":
      return new PayloadTooLargeError(
        `${capitalize(message)}. Send a smaller body, or raise HOOKYARD_MAX_BODY_BYTES on the server.`,
        options,
      );
    case "validation_failed": {
      const fields = details.map((d) => `${d.field}: ${d.message}`).join("; ");
      return new ValidationError(fields ? `${capitalize(message)}: ${fields}` : capitalize(message), options);
    }
    case "unknown_upstream":
      return new UnknownUpstreamError(capitalize(message), options);
    case "internal":
      return new HookyardError(
        `Hookyard failed with an internal error (HTTP ${status}): ${message}. Check the server logs.`,
        options,
      );
  }
}

function capitalize(s: string): string {
  return s.charAt(0).toUpperCase() + s.slice(1);
}
