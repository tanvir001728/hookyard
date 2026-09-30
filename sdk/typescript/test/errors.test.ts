import { describe, expect, it } from "vitest";
import {
  AuthError,
  BadRequestError,
  HookyardError,
  InvalidStateError,
  NotFoundError,
  PayloadTooLargeError,
  UnknownUpstreamError,
  ValidationError,
} from "../src/index.js";
import { apiError, client, mockFetch } from "./helpers.js";

async function failure(response: Response, call: "send" | "get" | "cancel" = "send"): Promise<HookyardError> {
  const hy = client(mockFetch(response).fetch, { maxRetries: 0 });
  const promise =
    call === "send"
      ? hy.to("courier-x").post("/shipments", {})
      : call === "get"
        ? hy.requests.get("req_1")
        : hy.requests.cancel("req_1");
  const err = await promise.then(
    () => {
      throw new Error("expected a rejection");
    },
    (e: unknown) => e,
  );
  expect(err).toBeInstanceOf(HookyardError);
  return err as HookyardError;
}

describe("error mapping", () => {
  it("bad_request → BadRequestError", async () => {
    const err = await failure(apiError(400, "bad_request", 'invalid JSON: unknown field "foo"'));
    expect(err).toBeInstanceOf(BadRequestError);
    expect(err).toMatchObject({ name: "BadRequestError", status: 400, code: "bad_request", details: [] });
    expect(err.message).toBe('Hookyard rejected the request: invalid JSON: unknown field "foo"');
  });

  it("unauthorized → AuthError with a hint about the token", async () => {
    const err = await failure(apiError(401, "unauthorized", "missing or invalid bearer token"));
    expect(err).toBeInstanceOf(AuthError);
    expect(err).toMatchObject({ name: "AuthError", status: 401, code: "unauthorized" });
    expect(err.message).toContain("missing or invalid bearer token");
    expect(err.message).toContain("Check HOOKYARD_TOKEN");
  });

  it("not_found → NotFoundError", async () => {
    const err = await failure(apiError(404, "not_found", 'request "req_1" not found'), "get");
    expect(err).toBeInstanceOf(NotFoundError);
    expect(err).toMatchObject({ status: 404, code: "not_found", message: 'Not found: request "req_1" not found' });
  });

  it("a missing route hints at a wrong URL", async () => {
    const err = await failure(apiError(404, "not_found", "no route for GET /v1/v1/requests/req_1"), "get");
    expect(err).toBeInstanceOf(NotFoundError);
    expect(err.message).toContain("without /v1");
  });

  it("invalid_state → InvalidStateError", async () => {
    const message = "cannot cancel a request that is in_flight (allowed: scheduled, pending, failed)";
    const err = await failure(apiError(409, "invalid_state", message), "cancel");
    expect(err).toBeInstanceOf(InvalidStateError);
    expect(err).toMatchObject({ status: 409, code: "invalid_state", message: `Invalid state: ${message}` });
  });

  it("payload_too_large → PayloadTooLargeError", async () => {
    const err = await failure(apiError(413, "payload_too_large", "request body exceeds 1048576 bytes"));
    expect(err).toBeInstanceOf(PayloadTooLargeError);
    expect(err).toMatchObject({ status: 413, code: "payload_too_large" });
    expect(err.message).toContain("HOOKYARD_MAX_BODY_BYTES");
  });

  it("validation_failed → ValidationError with details", async () => {
    const details = [
      { field: "path", message: 'must start with "/"' },
      { field: "retry.max_attempts", message: "must be between 1 and 100" },
    ];
    const err = await failure(apiError(422, "validation_failed", "2 fields are invalid", details));
    expect(err).toBeInstanceOf(ValidationError);
    expect(err).toMatchObject({ status: 422, code: "validation_failed", details });
    expect(err.message).toBe(
      '2 fields are invalid: path: must start with "/"; retry.max_attempts: must be between 1 and 100',
    );
  });

  it("unknown_upstream → UnknownUpstreamError with the suggestion", async () => {
    const err = await failure(
      apiError(422, "unknown_upstream", 'upstream "courier-y" is not configured (did you mean "courier-x"?)'),
    );
    expect(err).toBeInstanceOf(UnknownUpstreamError);
    expect(err).toMatchObject({ status: 422, code: "unknown_upstream" });
    expect(err.message).toBe('Upstream "courier-y" is not configured (did you mean "courier-x"?)');
  });

  it("internal → HookyardError", async () => {
    const err = await failure(apiError(500, "internal", "internal server error"));
    expect(err.constructor).toBe(HookyardError);
    expect(err).toMatchObject({ status: 500, code: "internal" });
    expect(err.message).toContain("Check the server logs");
  });

  it("an unknown code falls back to the HTTP status", async () => {
    const err = await failure(apiError(404, "gone_fishing", "nope"), "get");
    expect(err).toBeInstanceOf(NotFoundError);
    expect(err.code).toBe("not_found");
  });

  it("a non-JSON error body is an unexpected_response", async () => {
    const err = await failure(new Response("<html>Bad Gateway</html>", { status: 502 }));
    expect(err).toMatchObject({ status: 502, code: "unexpected_response" });
    expect(err.message).toContain("<html>Bad Gateway</html>");
    expect(err.message).toContain("points at a Hookyard server");
  });

  it("a success without a JSON body is an unexpected_response", async () => {
    const err = await failure(new Response("OK", { status: 200 }), "get");
    expect(err).toMatchObject({ status: 200, code: "unexpected_response" });
  });

  it("errors are real Errors with a stack", async () => {
    const err = await failure(apiError(400, "bad_request", "x"));
    expect(err).toBeInstanceOf(Error);
    expect(err.stack).toContain("BadRequestError");
  });
});
