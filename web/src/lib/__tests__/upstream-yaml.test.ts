import { describe, expect, it } from "vitest";
import type { Upstream } from "../api";
import { upstreamYaml } from "../upstream-yaml";

const base = {
  name: "courier-x",
  base_url: "https://api.courier-x.example/v2",
  timeout: "15s",
  retry: { preset: "patient", max_attempts: 20, initial_interval: "30s", max_interval: "1h", multiplier: 2, max_age: "72h" },
  header_names: ["Authorization", "X-Api-Version"],
  limits: { rate_limit: "10/s", burst: 10, max_concurrency: 4 },
  on_timeout: "unknown",
  dedupe_window: "24h",
  breaker: { failure_rate: 0.5, min_calls: 20, window: "1m", consecutive_failures: 5, cooldown: "30s", probes: 3 },
  classify: [
    { name: "fake success", status: "200", body: "status", condition: 'equals "FAILED"', then: "retry" },
    { name: "rule 2", status: "404", body: null, condition: null, then: "success" },
    { name: "rule 3", status: null, body: "error.code", condition: "exists", then: "fail" },
  ],
  callback_url: "http://orders:3000/hooks",
} as unknown as Upstream;

describe("upstreamYaml", () => {
  it("renders the resolved configuration as a hookyard.yaml entry", () => {
    expect(upstreamYaml(base)).toBe(`courier-x:
  base_url: https://api.courier-x.example/v2
  timeout: 15s
  retry:
    preset: patient
    max_attempts: 20
    initial_interval: 30s
    max_interval: 1h
    multiplier: 2
    max_age: 72h
  dedupe_window: 24h
  rate_limit: 10/s
  burst: 10
  max_concurrency: 4
  breaker:
    failure_rate: 0.5
    min_calls: 20
    window: 1m
    consecutive_failures: 5
    cooldown: 30s
    probes: 3
  on_timeout: unknown
  callback_url: http://orders:3000/hooks
  classify:
    - name: fake success
      status: "200"
      body: status
      equals: "FAILED"
      then: retry
    - status: "404"
      then: success
    - body: error.code
      exists: true
      then: fail
  headers: # values are hidden
    Authorization: "\${AUTHORIZATION}"
    X-Api-Version: "\${X_API_VERSION}"`);
  });

  it("leaves out what isn't set and shows a disabled breaker", () => {
    const yaml = upstreamYaml({ ...base, header_names: [], limits: { rate_limit: null, burst: null, max_concurrency: null }, breaker: null, classify: [], callback_url: null } as unknown as Upstream);
    expect(yaml).toContain("  breaker: off");
    expect(yaml).not.toMatch(/rate_limit|max_concurrency|classify|headers|callback_url/);
  });
});
