import { describe, expect, it } from "vitest";
import { formatAge, formatCount, formatMs, formatPercent, formatRate } from "../format";

describe("format", () => {
  it.each([
    [0, "0"],
    [1234, "1,234"],
    [99_999, "99,999"],
    [1_250_000, "1.3M"],
  ])("formatCount(%s) = %s", (n, want) => expect(formatCount(n)).toBe(want));

  it.each([
    [null, "—"],
    [1, "100%"],
    [0, "0%"],
    [0.98765, "98.8%"],
    [0.99961, "99.96%"],
  ])("formatPercent(%s) = %s", (n, want) => expect(formatPercent(n)).toBe(want));

  it.each([
    [null, "—"],
    [0.4, "<1 ms"],
    [42.5, "43 ms"],
    [2500, "2.5 s"],
    [45_000, "45 s"],
  ])("formatMs(%s) = %s", (n, want) => expect(formatMs(n)).toBe(want));

  it.each([
    [null, "—"],
    [45, "45s"],
    [120, "2m"],
    [200, "3m 20s"],
    [7300, "2h 1m"],
    [200_000, "2d 7h"],
  ])("formatAge(%s) = %s", (n, want) => expect(formatAge(n)).toBe(want));

  it.each([
    [0, "0"],
    [0.25, "0.25"],
    [12.34, "12.3"],
    [12, "12"],
    [1500.4, "1,500"],
  ])("formatRate(%s) = %s", (n, want) => expect(formatRate(n)).toBe(want));
});
