import { cn } from "@/lib/utils";

/** A labelled number in a `<dl>`, with an optional hint below it. */
export function Metric({ label, value, hint, tone }: { label: string; value: string; hint?: string; tone?: "warning" }) {
  return (
    <div className="min-w-0">
      <dt className="text-xs text-muted-foreground">{label}</dt>
      <dd className={cn("mt-0.5 truncate text-lg font-semibold tabular-nums", tone === "warning" && "text-amber-600 dark:text-amber-400")}>{value}</dd>
      {hint && <dd className="text-xs text-muted-foreground">{hint}</dd>}
    </div>
  );
}
