import { lazy, Suspense } from "react";
import { useQuery } from "@tanstack/react-query";
import { getRouteApi, useNavigate } from "@tanstack/react-router";
import { Activity, AlertTriangle, CheckCircle2, CircleDashed, Inbox, Server, XCircle, type LucideIcon } from "lucide-react";
import { PageHeader } from "@/components/layout/app-shell";
import { Badge, type BadgeTone } from "@/components/ui/badge";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { CodeBlock } from "@/components/ui/copy-button";
import { EmptyState, ErrorState, Skeleton } from "@/components/ui/feedback";
import { api, type StatsOverview, type StatsTimeseries, type Upstream, type UpstreamStats } from "@/lib/api";
import { formatAge, formatCount, formatMs, formatPercent, formatRate } from "@/lib/format";
import { healthLabel, upstreamHealth, type Health } from "@/lib/health";
import { cn } from "@/lib/utils";

export const ranges = {
  "1h": { label: "1 hour", hours: 1, step: "1m" },
  "6h": { label: "6 hours", hours: 6, step: "5m" },
  "24h": { label: "24 hours", hours: 24, step: "15m" },
  "7d": { label: "7 days", hours: 168, step: "2h" },
} as const;
export type RangeKey = keyof typeof ranges;

const REFRESH_MS = 10_000;
// The charting library is large; load it only when a chart is shown.
const ThroughputChart = lazy(() => import("@/components/charts/throughput-chart").then((m) => ({ default: m.ThroughputChart })));
const route = getRouteApi("/");

const healthStyle: Record<Health, { tone: BadgeTone; icon: LucideIcon }> = {
  idle: { tone: "neutral", icon: CircleDashed },
  healthy: { tone: "success", icon: CheckCircle2 },
  degraded: { tone: "warning", icon: AlertTriangle },
  failing: { tone: "danger", icon: XCircle },
};

function RangePicker({ value }: { value: RangeKey }) {
  const navigate = useNavigate();
  return (
    <div role="radiogroup" aria-label="Time range" className="inline-flex rounded-lg border bg-card p-0.5 shadow-xs">
      {(Object.keys(ranges) as RangeKey[]).map((key) => (
        <button
          key={key}
          type="button"
          role="radio"
          aria-checked={value === key}
          onClick={() => void navigate({ to: "/", search: { range: key }, replace: true })}
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

function Metric({ label, value, hint, tone }: { label: string; value: string; hint?: string; tone?: "warning" }) {
  return (
    <div className="min-w-0">
      <dt className="text-xs text-muted-foreground">{label}</dt>
      <dd className={cn("mt-0.5 truncate text-lg font-semibold tabular-nums", tone === "warning" && "text-amber-600 dark:text-amber-400")}>{value}</dd>
      {hint && <dd className="text-xs text-muted-foreground">{hint}</dd>}
    </div>
  );
}

function UpstreamCard({ stats, upstream, rangeLabel }: { stats: UpstreamStats; upstream?: Upstream; rangeLabel: string }) {
  const health = upstreamHealth(stats);
  const { tone, icon: Icon } = healthStyle[health];
  const { p50, p95, p99 } = stats.latency_ms;

  return (
    <Card>
      <CardHeader className="flex-row items-start justify-between gap-3">
        <div className="min-w-0">
          <CardTitle className="truncate text-base">{stats.upstream}</CardTitle>
          <CardDescription className="truncate font-mono text-xs">{upstream?.base_url ?? "no longer configured"}</CardDescription>
        </div>
        <Badge tone={tone}>
          <Icon className="size-3.5" aria-hidden />
          {healthLabel[health]}
        </Badge>
      </CardHeader>
      <CardContent>
        <dl className="grid grid-cols-2 gap-x-4 gap-y-4 sm:grid-cols-3">
          <Metric label="Success rate" value={formatPercent(stats.success_rate)} hint={`${formatCount(stats.succeeded)} delivered, ${formatCount(stats.dead)} dead`} />
          <Metric label="Attempts / min" value={formatRate(stats.throughput_per_min)} hint={`${formatCount(stats.failed_attempts)} failed in ${rangeLabel}`} />
          <Metric label="Latency p50" value={formatMs(p50)} hint={`p95 ${formatMs(p95)} · p99 ${formatMs(p99)}`} />
          <Metric
            label="Waiting"
            value={formatCount(stats.queue_depth)}
            hint={stats.oldest_pending_age_seconds !== null ? `oldest ${formatAge(stats.oldest_pending_age_seconds)}` : "queue empty"}
            tone={(stats.oldest_pending_age_seconds ?? 0) > 300 ? "warning" : undefined}
          />
          <Metric label="Dead letters" value={formatCount(stats.dlq_size)} hint={stats.dlq_size > 0 ? "need attention" : "none"} tone={stats.dlq_size > 0 ? "warning" : undefined} />
        </dl>
      </CardContent>
    </Card>
  );
}

function FirstRequest({ upstream }: { upstream: string }) {
  const origin = window.location.origin;
  const curl = `curl -X POST ${origin}/v1/requests \\
  -H "Authorization: Bearer $HOOKYARD_TOKEN" \\
  -d '{"upstream":"${upstream}","method":"POST","path":"/","body":{"hello":"world"}}'`;
  const sdk = `import { Hookyard } from "@hookyard/sdk";

const hy = new Hookyard({ url: "${origin}" }); // token from HOOKYARD_TOKEN
await hy.to("${upstream}").post("/", { hello: "world" });`;

  return (
    <Card className="mb-6">
      <CardHeader>
        <CardTitle className="flex items-center gap-2 text-base">
          <Inbox className="size-4" aria-hidden /> Send your first request
        </CardTitle>
        <CardDescription>Nothing has been delivered yet. Enqueue a request with curl or the TypeScript SDK, and it will show up here.</CardDescription>
      </CardHeader>
      <CardContent className="grid gap-4 lg:grid-cols-2">
        <CodeBlock code={curl} />
        <CodeBlock code={sdk} />
      </CardContent>
    </Card>
  );
}

export function OverviewPage() {
  const range = route.useSearch().range ?? "1h";
  const r = ranges[range];

  const upstreams = useQuery({ queryKey: ["upstreams"], queryFn: () => api<{ data: Upstream[] }>("/v1/upstreams"), staleTime: 60_000 });
  const overview = useQuery({
    queryKey: ["stats", "overview", range],
    queryFn: () => api<StatsOverview>("/v1/stats/overview", { query: { window: `${r.hours}h` } }),
    refetchInterval: REFRESH_MS,
  });
  const series = useQuery({
    queryKey: ["stats", "timeseries", range],
    queryFn: () =>
      api<StatsTimeseries>("/v1/stats/timeseries", {
        query: { from: new Date(Date.now() - r.hours * 3_600_000).toISOString(), step: r.step },
      }),
    refetchInterval: REFRESH_MS,
  });

  const byName = new Map((upstreams.data?.data ?? []).map((u) => [u.name, u]));
  const stats = overview.data?.data ?? [];
  const noTraffic = overview.isSuccess && stats.every((s) => upstreamHealth(s) === "idle" && s.dlq_size === 0);

  return (
    <>
      <PageHeader
        title="Overview"
        description={
          <span className="inline-flex items-center gap-1.5">
            <Activity className="size-3.5" aria-hidden /> Delivery health for the last {r.label} · refreshes every 10s
          </span>
        }
        actions={<RangePicker value={range} />}
      />

      {overview.isError ? (
        <ErrorState error={overview.error} />
      ) : overview.isPending ? (
        <div className="grid gap-4 md:grid-cols-2">
          <Skeleton className="h-48" />
          <Skeleton className="h-48" />
        </div>
      ) : stats.length === 0 ? (
        <Card>
          <EmptyState icon={<Server />} title="No upstreams configured">
            Add upstreams to <code className="font-mono">hookyard.yaml</code> and restart Hookyard.
          </EmptyState>
        </Card>
      ) : (
        <>
          {noTraffic && stats[0] && <FirstRequest upstream={stats[0].upstream} />}

          <Card className="mb-6">
            <CardHeader>
              <CardTitle>Deliveries</CardTitle>
              <CardDescription>All upstreams, per {r.step === "2h" ? "2 hours" : r.step.replace("m", " minutes").replace(/^1 minutes$/, "minute")}.</CardDescription>
            </CardHeader>
            <CardContent>
              {series.isError ? (
                <ErrorState error={series.error} />
              ) : series.data ? (
                <Suspense fallback={<Skeleton className="h-56" />}>
                  <ThroughputChart data={series.data} rangeHours={r.hours} />
                </Suspense>
              ) : (
                <Skeleton className="h-56" />
              )}
            </CardContent>
          </Card>

          <h2 className="mb-3 text-sm font-semibold">Upstreams</h2>
          <div className="grid gap-4 md:grid-cols-2">
            {stats.map((s) => (
              <UpstreamCard key={s.upstream} stats={s} upstream={byName.get(s.upstream)} rangeLabel={r.label} />
            ))}
          </div>
        </>
      )}
    </>
  );
}
