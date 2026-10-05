import { useState } from "react";
import { CartesianGrid, Line, LineChart, ResponsiveContainer, Tooltip, XAxis, YAxis, type TooltipContentProps } from "recharts";
import { Table, TBody, TD, TH, THead, TR } from "@/components/ui/table";
import type { StatsTimeseries } from "@/lib/api";
import { formatBucket, formatMs } from "@/lib/format";
import { useIsDark } from "@/lib/use-dark";
import { latencyColors, seriesColors } from "./series";

interface Point {
  start: string;
  p50: number | null;
  p95: number | null;
  p99: number | null;
}

const series = [
  { key: "p50", label: "p50" },
  { key: "p95", label: "p95" },
  { key: "p99", label: "p99" },
] as const;

type Colors = (typeof latencyColors)["light"];

function ChartTooltip({ active, payload, rangeHours, colors }: TooltipContentProps<number, string> & { rangeHours: number; colors: Colors }) {
  const point = payload?.[0]?.payload as Point | undefined;
  if (!active || !point) return null;
  return (
    <div className="rounded-lg border bg-card px-3 py-2 text-xs shadow-md">
      <p className="mb-1 font-medium">{formatBucket(point.start, rangeHours)}</p>
      {[...series].reverse().map((s) => (
        <p key={s.key} className="flex items-center gap-2">
          <span className="inline-block h-0.5 w-3 rounded" style={{ background: colors[s.key] }} />
          <span className="text-muted-foreground">{s.label}</span>
          <span className="ml-auto pl-3 font-medium tabular-nums">{formatMs(point[s.key])}</span>
        </p>
      ))}
    </div>
  );
}

/** Attempt latency percentiles per bucket. Buckets without attempts leave a gap. */
export function LatencyChart({ data, rangeHours }: { data: StatsTimeseries; rangeHours: number }) {
  const dark = useIsDark();
  const colors = latencyColors[dark ? "dark" : "light"];
  const chrome = seriesColors[dark ? "dark" : "light"];
  const [asTable, setAsTable] = useState(false);
  const points: Point[] = data.data.map((b) => ({
    start: b.start,
    p50: b.latency_ms.p50 ?? null,
    p95: b.latency_ms.p95 ?? null,
    p99: b.latency_ms.p99 ?? null,
  }));
  const withData = points.filter((p) => p.p50 !== null);

  return (
    <div>
      <div className="mb-3 flex flex-wrap items-center justify-between gap-3">
        <ul className="flex flex-wrap items-center gap-4 text-xs" aria-label="Legend">
          {series.map((s) => (
            <li key={s.key} className="flex items-center gap-1.5">
              <span className="inline-block h-0.5 w-4 rounded" style={{ background: colors[s.key] }} aria-hidden />
              <span>{s.label}</span>
            </li>
          ))}
        </ul>
        <button type="button" onClick={() => setAsTable((v) => !v)} className="text-xs text-muted-foreground underline-offset-4 hover:text-foreground hover:underline">
          {asTable ? "View as chart" : "View as table"}
        </button>
      </div>

      {withData.length === 0 ? (
        <p className="flex h-48 items-center justify-center text-sm text-muted-foreground">No attempts in this period.</p>
      ) : asTable ? (
        <div className="max-h-64 overflow-y-auto rounded-md border">
          <Table>
            <THead>
              <TR>
                <TH>Time</TH>
                {series.map((s) => (
                  <TH key={s.key} className="text-right">
                    {s.label}
                  </TH>
                ))}
              </TR>
            </THead>
            <TBody>
              {[...withData].reverse().map((p) => (
                <TR key={p.start}>
                  <TD>{formatBucket(p.start, rangeHours)}</TD>
                  {series.map((s) => (
                    <TD key={s.key} className="text-right tabular-nums">
                      {formatMs(p[s.key])}
                    </TD>
                  ))}
                </TR>
              ))}
            </TBody>
          </Table>
        </div>
      ) : (
        <div className="h-48" role="img" aria-label="Attempt latency percentiles (p50, p95, p99) over time. Use View as table for the values.">
          <ResponsiveContainer width="100%" height="100%">
            <LineChart data={points} margin={{ top: 4, right: 8, bottom: 0, left: -4 }}>
              <CartesianGrid vertical={false} stroke={chrome.grid} />
              <XAxis
                dataKey="start"
                tickFormatter={(v: string) => formatBucket(v, rangeHours)}
                tick={{ fill: chrome.axis, fontSize: 11 }}
                tickLine={false}
                axisLine={false}
                minTickGap={32}
              />
              <YAxis tickFormatter={(v: number) => (v === 0 ? "0" : formatMs(v))} tick={{ fill: chrome.axis, fontSize: 11 }} tickLine={false} axisLine={false} width={56} />
              <Tooltip
                cursor={{ stroke: chrome.axis, strokeDasharray: "3 3" }}
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
                  connectNulls={false}
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
