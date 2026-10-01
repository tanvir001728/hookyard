import { afterEach, describe, expect, it, vi } from "vitest";
import { api, ApiError, buildUrl, setUnauthorizedHandler } from "../api";

function respond(status: number, body?: unknown) {
  return vi.spyOn(globalThis, "fetch").mockResolvedValue(
    new Response(body === undefined ? null : JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } }),
  );
}

afterEach(() => vi.restoreAllMocks());

describe("buildUrl", () => {
  it("drops empty values and repeats array keys", () => {
    expect(buildUrl("/v1/requests", { upstream: "courier-x", status: "", cursor: undefined, tag: ["a:1", "b:2"], limit: 50 })).toBe(
      "/v1/requests?upstream=courier-x&tag=a%3A1&tag=b%3A2&limit=50",
    );
    expect(buildUrl("/v1/dlq")).toBe("/v1/dlq");
  });
});

describe("api", () => {
  it("sends the CSRF header only on state-changing requests", async () => {
    const fetch = respond(200, { ok: true });
    await api("/v1/requests");
    await api("/v1/dlq/replay", { method: "POST", body: { dry_run: true } });

    const [, getInit] = fetch.mock.calls[0]!;
    const [, postInit] = fetch.mock.calls[1]!;
    expect((getInit!.headers as Record<string, string>)["Hookyard-Csrf"]).toBeUndefined();
    expect((postInit!.headers as Record<string, string>)["Hookyard-Csrf"]).toBe("1");
    expect(postInit!.body).toBe('{"dry_run":true}');
    expect(postInit!.credentials).toBe("same-origin");
  });

  it("turns error bodies into ApiError with details", async () => {
    respond(422, { error: { code: "validation_failed", message: "1 field is invalid", details: [{ field: "path", message: "bad" }] } });
    const err = await api("/v1/requests", { method: "POST", body: {} }).catch((e: unknown) => e);
    expect(err).toBeInstanceOf(ApiError);
    expect(err).toMatchObject({ status: 422, code: "validation_failed", details: [{ field: "path", message: "bad" }] });
  });

  it("calls the unauthorized handler on 401", async () => {
    const handler = vi.fn();
    setUnauthorizedHandler(handler);
    respond(401, { error: { code: "unauthorized", message: "nope" } });
    await expect(api("/v1/requests")).rejects.toBeInstanceOf(ApiError);
    expect(handler).toHaveBeenCalledOnce();
  });

  it("reports network failures clearly", async () => {
    vi.spyOn(globalThis, "fetch").mockRejectedValue(new TypeError("Failed to fetch"));
    await expect(api("/v1/requests")).rejects.toMatchObject({ code: "network", message: expect.stringContaining("Can't reach Hookyard") });
  });
});
