import type { Duration, Timestamp } from "./types.js";

const DURATION_RE = /^(?:\d+(?:\.\d+)?(?:ms|s|m|h))+$/;
const PART_RE = /(\d+(?:\.\d+)?)(ms|s|m|h)/g;
const UNIT_MS: Record<string, number> = { ms: 1, s: 1_000, m: 60_000, h: 3_600_000 };

function invalidDuration(value: unknown, field: string): TypeError {
  return new TypeError(
    `Invalid duration ${JSON.stringify(value)} for ${field}: use a number of milliseconds or a ` +
      `string with a unit, such as "500ms", "30s", "5m" or "1h30m".`,
  );
}

/**
 * Converts a duration to the wire format: strings are validated and passed through, numbers of
 * milliseconds become `"<n>ms"`.
 */
export function toWireDuration(value: Duration, field: string): string {
  if (typeof value === "number") {
    if (!Number.isFinite(value) || value < 0) throw invalidDuration(value, field);
    // Whole milliseconds keep the value readable and within the server's resolution.
    return `${Math.round(value)}ms`;
  }
  if (typeof value !== "string") throw invalidDuration(value, field);
  const trimmed = value.trim();
  if (!DURATION_RE.test(trimmed)) throw invalidDuration(value, field);
  return trimmed;
}

/** Converts a duration to milliseconds, for timeouts the SDK enforces itself. */
export function toMilliseconds(value: Duration, field: string): number {
  if (typeof value === "number") {
    if (!Number.isFinite(value) || value < 0) throw invalidDuration(value, field);
    return value;
  }
  const wire = toWireDuration(value, field);
  let total = 0;
  for (const [, amount, unit] of wire.matchAll(PART_RE)) {
    total += Number(amount) * (UNIT_MS[unit ?? "ms"] ?? 1);
  }
  return total;
}

/** Formats milliseconds for messages, for example `"30s"` or `"1500ms"`. */
export function formatMilliseconds(ms: number): string {
  if (ms >= 1_000 && ms % 1_000 === 0) return `${ms / 1_000}s`;
  return `${ms}ms`;
}

/** Converts a timestamp to RFC 3339. Strings are passed through unchanged. */
export function toWireTimestamp(value: Timestamp, field: string): string {
  if (value instanceof Date) {
    if (Number.isNaN(value.getTime())) {
      throw new TypeError(`Invalid date for ${field}: the Date object is invalid.`);
    }
    return value.toISOString();
  }
  if (typeof value === "string" && value !== "") return value;
  throw new TypeError(
    `Invalid timestamp ${JSON.stringify(value)} for ${field}: use a Date or an RFC 3339 string ` +
      `such as "2026-10-01T10:00:00Z".`,
  );
}
