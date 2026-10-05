import { cn } from "@/lib/utils";

export const ranges = {
  "1h": { label: "1 hour", hours: 1, step: "1m" },
  "6h": { label: "6 hours", hours: 6, step: "5m" },
  "24h": { label: "24 hours", hours: 24, step: "15m" },
  "7d": { label: "7 days", hours: 168, step: "2h" },
} as const;
export type RangeKey = keyof typeof ranges;

/** Validates the `range` search param shared by pages with charts. */
export function validateRangeSearch(search: Record<string, unknown>): { range?: RangeKey } {
  return typeof search.range === "string" && search.range in ranges ? { range: search.range as RangeKey } : {};
}

/** "per minute", "per 5 minutes", "per 2 hours" for a range's bucket size. */
export function stepLabel(key: RangeKey): string {
  const step = ranges[key].step;
  if (step === "1m") return "minute";
  return step.endsWith("h") ? `${step.slice(0, -1)} hours` : `${step.slice(0, -1)} minutes`;
}

export function RangePicker({ value, onChange }: { value: RangeKey; onChange: (key: RangeKey) => void }) {
  return (
    <div role="radiogroup" aria-label="Time range" className="inline-flex rounded-lg border bg-card p-0.5 shadow-xs">
      {(Object.keys(ranges) as RangeKey[]).map((key) => (
        <button
          key={key}
          type="button"
          role="radio"
          aria-checked={value === key}
          onClick={() => onChange(key)}
          className={cn(
            "rounded-md px-3 py-1 text-xs font-medium transition-colors",
            value === key ? "bg-primary text-primary-foreground" : "text-muted-foreground hover:text-foreground",
          )}
        >
          {key}
        </button>
      ))}
    </div>
  );
}
