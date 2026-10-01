const integer = new Intl.NumberFormat("en-US");
const compact = new Intl.NumberFormat("en-US", { notation: "compact", maximumFractionDigits: 1 });

/** 1234 -> "1,234"; large values compact: 1250000 -> "1.3M". */
export function formatCount(n: number): string {
  return Math.abs(n) >= 100_000 ? compact.format(n) : integer.format(n);
}

/** 0.98765 -> "98.8%"; 1 -> "100%"; null -> "—". */
export function formatPercent(ratio: number | null | undefined): string {
  if (ratio === null || ratio === undefined) return "—";
  const pct = ratio * 100;
  if (pct === 100 || pct === 0) return `${pct}%`;
  return `${pct >= 99.95 ? pct.toFixed(2) : pct.toFixed(1)}%`;
}

/** Milliseconds: 0.4 -> "<1 ms", 42.5 -> "43 ms", 2500 -> "2.5 s". */
export function formatMs(ms: number | null | undefined): string {
  if (ms === null || ms === undefined) return "—";
  if (ms < 1) return "<1 ms";
  if (ms < 1000) return `${Math.round(ms)} ms`;
  return `${(ms / 1000).toFixed(ms < 10_000 ? 1 : 0)} s`;
}

/** Seconds as a short age: 45 -> "45s", 200 -> "3m 20s", 7300 -> "2h 1m", 200000 -> "2d 7h". */
export function formatAge(seconds: number | null | undefined): string {
  if (seconds === null || seconds === undefined) return "—";
  const s = Math.max(0, Math.floor(seconds));
  if (s < 60) return `${s}s`;
  if (s < 3600) return `${Math.floor(s / 60)}m${s % 60 ? ` ${s % 60}s` : ""}`;
  if (s < 86_400) return `${Math.floor(s / 3600)}h${Math.floor((s % 3600) / 60) ? ` ${Math.floor((s % 3600) / 60)}m` : ""}`;
  return `${Math.floor(s / 86_400)}d${Math.floor((s % 86_400) / 3600) ? ` ${Math.floor((s % 86_400) / 3600)}h` : ""}`;
}

/** A per-minute rate: 0 -> "0", 0.25 -> "0.25", 12.345 -> "12.3", 1500 -> "1,500". */
export function formatRate(n: number): string {
  if (n === 0) return "0";
  if (n < 1) return n.toFixed(2);
  if (n < 100) return n.toFixed(1).replace(/\.0$/, "");
  return integer.format(Math.round(n));
}

/** A time axis label for a bucket start, sized to the range. */
export function formatBucket(iso: string, rangeHours: number): string {
  const d = new Date(iso);
  return rangeHours > 48
    ? d.toLocaleDateString(undefined, { month: "short", day: "numeric", hour: "numeric" })
    : d.toLocaleTimeString(undefined, { hour: "2-digit", minute: "2-digit" });
}

const relative = new Intl.RelativeTimeFormat("en", { numeric: "auto" });

/** A timestamp relative to now: "5 minutes ago", "in 2 hours". */
export function formatRelative(iso: string | Date | null | undefined, now = Date.now()): string {
  if (!iso) return "—";
  const diff = (new Date(iso).getTime() - now) / 1000;
  const abs = Math.abs(diff);
  if (abs < 10) return diff < 0 ? "just now" : "in a few seconds";
  if (abs < 60) return relative.format(Math.round(diff), "second");
  if (abs < 3600) return relative.format(Math.round(diff / 60), "minute");
  if (abs < 86_400) return relative.format(Math.round(diff / 3600), "hour");
  return relative.format(Math.round(diff / 86_400), "day");
}

/** A full local timestamp for titles and detail views. */
export function formatDateTime(iso: string | null | undefined): string {
  if (!iso) return "—";
  return new Date(iso).toLocaleString(undefined, { dateStyle: "medium", timeStyle: "medium" });
}
