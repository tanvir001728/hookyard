import { useState } from "react";
import { CartesianGrid, Line, LineChart, ResponsiveContainer, Tooltip, XAxis, YAxis, type TooltipContentProps } from "recharts";
import { Table, TBody, TD, TH, THead, TR } from "@/components/ui/table";
import type { StatsTimeseries } from "@/lib/api";
import { formatBucket, formatCount } from "@/lib/format";
import { useIsDark } from "@/lib/use-dark";
import { seriesColors, type SeriesPalette } from "./series";

interface Point {
  start: string;
  delivered: number;
  failed: number;
}

const series = [
  { key: "delivered", label: "Delivered" },
  { key: "failed", label: "Failed attempts" },
] as const;

function ChartTooltip({ active, payload, rangeHours, colors }: TooltipContentProps<number, string> & { rangeHours: number; colors: SeriesPalette }) {
  const point = payload?.[0]?.payload as Point | undefined;
  if (!active || !point) return null;
  return (
    <div className="rounded-lg border bg-card px-3 py-2 text-xs shadow-md">
      <p className="mb-1 font-medium">{formatBucket(point.start, rangeHours)}</p>
      {series.map((s) => (
        <p key={s.key} className="flex items-center gap-2">
          <span className="inline-block h-0.5 w-3 rounded" style={{ background: colors[s.key] }} />
          <span className="text-muted-foreground">{s.label}</span>
          <span className="ml-auto font-medium tabular-nums">{formatCount(point[s.key])}</span>
        </p>
      ))}
    </div>
  );
}

/** Delivered requests and failed attempts per bucket, for all upstreams. */
export function ThroughputChart({ data, rangeHours }: { data: StatsTimeseries; rangeHours: number }) {
  const dark = useIsDark();
  const colors = seriesColors[dark ? "dark" : "light"];
  const [asTable, setAsTable] = useState(false);
  const points: Point[] = data.data.map((b) => ({ start: b.start, delivered: b.succeeded, failed: b.failed_attempts }));
  const totals = { delivered: 0, failed: 0 };
  for (const p of points) {
    totals.delivered += p.delivered;
    totals.failed += p.failed;
  }

  return (
    <div>
      <div className="mb-3 flex flex-wrap items-center justify-between gap-3">
        <ul className="flex flex-wrap items-center gap-4 text-xs" aria-label="Legend">
          {series.map((s) => (
            <li key={s.key} className="flex items-center gap-1.5">
              <span className="inline-block h-0.5 w-4 rounded" style={{ background: colors[s.key] }} aria-hidden />
              <span>{s.label}</span>
              <span className="text-muted-foreground tabular-nums">· {formatCount(totals[s.key])} total</span>
            </li>
          ))}
        </ul>
        <button type="button" onClick={() => setAsTable((v) => !v)} className="text-xs text-muted-foreground underline-offset-4 hover:text-foreground hover:underline">
          {asTable ? "View as chart" : "View as table"}
        </button>
      </div>

      {asTable ? (
        <div className="max-h-64 overflow-y-auto rounded-md border">
          <Table>
            <THead>
              <TR>
                <TH>Time</TH>
                <TH className="text-right">Delivered</TH>
                <TH className="text-right">Failed attempts</TH>
              </TR>
            </THead>
            <TBody>
              {[...points].reverse().map((p) => (
                <TR key={p.start}>
                  <TD>{formatBucket(p.start, rangeHours)}</TD>
                  <TD className="text-right tabular-nums">{formatCount(p.delivered)}</TD>
                  <TD className="text-right tabular-nums">{formatCount(p.failed)}</TD>
                </TR>
              ))}
            </TBody>
          </Table>
        </div>
      ) : (
        <div className="h-56" role="img" aria-label="Delivered requests and failed attempts over time. Use View as table for the values.">
          <ResponsiveContainer width="100%" height="100%">
            <LineChart data={points} margin={{ top: 4, right: 8, bottom: 0, left: -12 }}>
              <CartesianGrid vertical={false} stroke={colors.grid} />
              <XAxis
                dataKey="start"
                tickFormatter={(v: string) => formatBucket(v, rangeHours)}
                tick={{ fill: colors.axis, fontSize: 11 }}
                tickLine={false}
                axisLine={false}
                minTickGap={32}
              />
              <YAxis allowDecimals={false} tick={{ fill: colors.axis, fontSize: 11 }} tickLine={false} axisLine={false} width={44} />
              <Tooltip
                cursor={{ stroke: colors.axis, strokeDasharray: "3 3" }}
                content={(props) => <ChartTooltip {...(props as TooltipContentProps<number, string>)} rangeHours={rangeHours} colors={colors} />}
              />
              {series.map((s) => (
                <Line
                  key={s.key}
                  type="linear"
                  dataKey={s.key}
                  name={s.label}
                  stroke={colors[s.key]}
                  strokeWidth={2}
                  dot={false}
                  activeDot={{ r: 4, strokeWidth: 2, stroke: dark ? "#18181b" : "#ffffff" }}
                  isAnimationActive={false}
                />
              ))}
            </LineChart>
          </ResponsiveContainer>
        </div>
      )}
    </div>
  );
}
