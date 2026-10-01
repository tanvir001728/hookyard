import { describe, expect, it } from "vitest";
import type { HookyardRequest } from "../api";
import { toCurl, toSdk } from "../copy-as";
import { REDACTED, redactHeaders } from "../redact";

const req = {
  id: "req_01",
  upstream: "courier-x",
  method: "POST",
  path: "/shipments?notify=true",
  headers: { "X-Correlation-Id": "abc", Authorization: "Bearer app-secret", "X-Api-Key": "k" },
  body: { order_id: 1, note: "it's here" },
  tags: { app: "orders" },
} as unknown as HookyardRequest;

describe("redactHeaders", () => {
  it("hides values of sensitive headers only", () => {
    expect(redactHeaders(req.headers)).toEqual({ "X-Correlation-Id": "abc", Authorization: REDACTED, "X-Api-Key": REDACTED });
  });
});

describe("toCurl", () => {
  it("reproduces the request with secrets redacted and quotes escaped", () => {
    const curl = toCurl(req, "http://localhost:8080");
    expect(curl).toContain("curl -X POST http://localhost:8080/v1/requests");
    expect(curl).toContain('$HOOKYARD_TOKEN"');
    expect(curl).not.toContain("app-secret");
    expect(curl).toContain(`it'\\''s here`);
    const json = curl.slice(curl.indexOf("-d '") + 4, -1).replace(/'\\''/g, "'");
    expect(JSON.parse(json)).toMatchObject({ upstream: "courier-x", method: "POST", path: "/shipments?notify=true", tags: { app: "orders" } });
  });
});

describe("toSdk", () => {
  it("uses the method helper with body and options", () => {
    const code = toSdk(req);
    expect(code.startsWith('await hy.to("courier-x").post("/shipments?notify=true", {')).toBe(true);
    expect(code).toContain('"tags"');
    expect(code).not.toContain("app-secret");
  });

  it("omits the body argument for GET", () => {
    expect(toSdk({ ...req, method: "GET", headers: {}, tags: {}, body: null } as unknown as HookyardRequest)).toBe(
      'await hy.to("courier-x").get("/shipments?notify=true");',
    );
  });
});
