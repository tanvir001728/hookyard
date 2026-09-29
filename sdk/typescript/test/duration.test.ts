import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";
import { formatMilliseconds, toMilliseconds, toWireDuration, toWireTimestamp } from "../src/duration.js";
import { FINAL_STATUSES, VERSION, isFinalStatus } from "../src/index.js";

describe("durations", () => {
  it.each([
    ["30s", "30s"],
    [" 5m ", "5m"],
    ["1h30m", "1h30m"],
    ["1.5s", "1.5s"],
    ["250ms", "250ms"],
    [1500, "1500ms"],
    [0, "0ms"],
    [1.4, "1ms"],
  ])("toWireDuration(%j) = %j", (input, expected) => {
    expect(toWireDuration(input, "timeout")).toBe(expected);
  });

  it.each(["", "30", "30 s", "30sec", "-5s", "1d", "ms"])("rejects %j", (input) => {
    expect(() => toWireDuration(input, "timeout")).toThrow(TypeError);
  });

  it.each([-1, Number.NaN, Number.POSITIVE_INFINITY])("rejects the number %d", (input) => {
    expect(() => toWireDuration(input, "timeout")).toThrow(`Invalid duration ${JSON.stringify(input)} for timeout`);
  });

  it.each([
    ["30s", 30_000],
    ["1h30m", 5_400_000],
    ["1.5s", 1_500],
    ["2m500ms", 120_500],
    [250, 250],
  ])("toMilliseconds(%j) = %d", (input, expected) => {
    expect(toMilliseconds(input, "timeout")).toBe(expected);
  });

  it("formats milliseconds for messages", () => {
    expect(formatMilliseconds(30_000)).toBe("30s");
    expect(formatMilliseconds(1_500)).toBe("1500ms");
  });

  it("converts timestamps", () => {
    expect(toWireTimestamp(new Date("2026-10-01T10:00:00Z"), "at")).toBe("2026-10-01T10:00:00.000Z");
    expect(toWireTimestamp("2026-10-01T10:00:00+02:00", "at")).toBe("2026-10-01T10:00:00+02:00");
    expect(() => toWireTimestamp("", "at")).toThrow(/Invalid timestamp "" for at/);
  });
});

describe("statuses", () => {
  it("knows the final statuses", () => {
    expect(FINAL_STATUSES).toEqual(["succeeded", "dead", "unknown", "canceled"]);
    expect(isFinalStatus("dead")).toBe(true);
    expect(isFinalStatus("failed")).toBe(false);
  });
});

describe("version", () => {
  it("matches package.json", () => {
    const pkg = JSON.parse(readFileSync(new URL("../package.json", import.meta.url), "utf8")) as { version: string };
    expect(VERSION).toBe(pkg.version);
  });
});
