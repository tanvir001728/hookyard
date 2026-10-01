import { expect, it } from "vitest";
import type { UpstreamStats } from "../api";
import { upstreamHealth } from "../health";

const base: UpstreamStats = {
  upstream: "courier-x",
  succeeded: 0,
  failed_attempts: 0,
  dead: 0,
  success_rate: null,
  throughput_per_min: 0,
  latency_ms: { p50: null, p95: null, p99: null },
  queue_depth: 0,
  oldest_pending_age_seconds: null,
  dlq_size: 0,
};

it.each([
  ["idle", {}],
  ["healthy", { succeeded: 100, success_rate: 1 }],
  ["degraded", { succeeded: 97, dead: 3, success_rate: 0.97 }],
  ["degraded", { succeeded: 10, success_rate: 1, queue_depth: 4, oldest_pending_age_seconds: 900 }],
  ["failing", { succeeded: 5, dead: 5, success_rate: 0.5 }],
  ["healthy", { queue_depth: 2, oldest_pending_age_seconds: 3 }],
] as const)("%s for %o", (want, over) => {
  expect(upstreamHealth({ ...base, ...over })).toBe(want);
});
