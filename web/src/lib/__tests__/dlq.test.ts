import { expect, it } from "vitest";
import { reasonLabel } from "../dlq";

it.each([
  ["http_status", 503, "HTTP 503"],
  ["http_status", null, "HTTP error"],
  ["timeout", null, "Timed out"],
  ["classified_failure", 200, "Failure reported in response"],
  ["connection", null, "Connection failed"],
  ["max_age_exceeded", null, "Retry budget (max age) ran out"],
  ["internal", null, "Internal error"],
] as const)("reasonLabel(%s, %s) = %s", (code, status, want) => {
  expect(reasonLabel(code, status)).toBe(want);
});
