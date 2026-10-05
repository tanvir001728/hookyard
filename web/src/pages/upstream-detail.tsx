import { lazy, Suspense, useState, type ReactNode } from "react";
import { useQuery } from "@tanstack/react-query";
import { getRouteApi, Link, useNavigate } from "@tanstack/react-router";
import { ArrowLeft, Ban, CircleDot, Gauge, History, Pause, Play, ShieldAlert, Timer, type LucideIcon } from "lucide-react";
import { PageHeader } from "@/components/layout/app-shell";
import { Metric } from "@/components/metric";
import { PauseDialog, type PauseAction } from "@/components/pause-dialog";
import { RangePicker, ranges, stepLabel } from "@/components/range-picker";
import { Badge, type BadgeTone } from "@/components/ui/badge";
import { Button, buttonClass } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { CodeBlock } from "@/components/ui/copy-button";
import { EmptyState, ErrorState, Skeleton } from "@/components/ui/feedback";
import { api, ApiError, type StatsOverview, type StatsTimeseries, type Upstream, type UpstreamEvent } from "@/lib/api";
import { formatAge, formatCount, formatDateTime, formatMs, formatPercent, formatRate, formatRelative } from "@/lib/format";
import { upstreamYaml } from "@/lib/upstream-yaml";
import { cn } from "@/lib/utils";

const ThroughputChart = lazy(() => import("@/components/charts/throughput-chart").then((m) => ({ default: m.ThroughputChart })));
const LatencyChart = lazy(() => import("@/components/charts/latency-chart").then((m) => ({ default: m.LatencyChart })));

const route = getRouteApi("/upstreams/$name");
const REFRESH_MS = 5_000;

type Status = Upstream["state"]["status"];

const statusStyle: Record<Status, { tone: BadgeTone; label: string; icon: LucideIcon }> = {
  active: { tone: "success", label: "Delivering", icon: CircleDot },
  paused: { tone: "warning", label: "Paused", icon: Pause },
  breaker_open: { tone: "danger", label: "Breaker open", icon: ShieldAlert },
  breaker_half_open: { tone: "warning", label: "Probing", icon: Gauge },
  throttled: { tone: "warning", label: "Throttled", icon: Timer },
};

const eventLabel: Record<string, string> = {
  breaker_open: "Breaker opened",
  breaker_half_open: "Breaker half-open: probing",
  breaker_closed: "Breaker closed",
  paused: "Paused",
  resumed: "Resumed",
};

function Row({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className="flex items-baseline justify-between gap-4 py-1.5">
      <dt className="shrink-0 text-sm text-muted-foreground">{label}</dt>
      <dd className="min-w-0 text-right text-sm break-words">{children}</dd>
    </div>
  );
}

/** A used/limit bar. Text carries the numbers; the bar is a visual aid. */
function Usage({ label, used, limit, hint }: { label: string; used: number; limit: number; hint: string }) {
  const pct = limit > 0 ? Math.min(100, (used / limit) * 100) : 0;
  return (
    <div>
      <div className="flex items-baseline justify-between text-sm">
        <span className="text-muted-foreground">{label}</span>
        <span className="tabular-nums">
          {formatCount(used)} <span className="text-muted-foreground">/ {formatCount(limit)}</span>
        </span>
      </div>
      <div className="mt-1.5 h-1.5 overflow-hidden rounded-full bg-muted" aria-hidden>
        <div className="h-full rounded-full bg-primary" style={{ width: `${pct}%` }} />
      </div>
      <p className="mt-1 text-xs text-muted-foreground">{hint}</p>
    </div>
  );
}

function StateBanner({ u }: { u: Upstream }) {
  const s = u.state;
  if (s.status === "paused" && s.pause) {
    return (
      <Banner tone="warning" icon={Pause} title={`Paused by ${s.pause.by} ${formatRelative(s.pause.since)}`}>
        {s.pause.reason && <>“{s.pause.reason}”. </>}
        Requests wait in the queue{s.pause.until ? ` until ${formatDateTime(s.pause.until)}, when deliveries resume on their own` : " until someone resumes deliveries"}.
      </Banner>
    );
  }
  if (s.status === "breaker_open") {
    return (
      <Banner tone="danger" icon={ShieldAlert} title={`The circuit breaker opened ${formatRelative(s.breaker_since)}`}>
        {u.name} kept failing, so deliveries are paused instead of burning retries. After the cooldown
        {u.breaker ? ` (${u.breaker.cooldown})` : ""} a few probe requests decide whether to resume. Waiting requests don't lose any of their max age.
      </Banner>
    );
  }
  if (s.status === "breaker_half_open") {
    return (
      <Banner tone="warning" icon={Gauge} title="Probing whether the upstream has recovered">
        A few requests are being sent{u.breaker ? ` (${u.breaker.probes} probes)` : ""}. If they succeed, deliveries resume; if not, the breaker opens again.
      </Banner>
    );
  }
  if (s.status === "throttled" && s.throttled_until) {
    return (
      <Banner tone="warning" icon={Timer} title={`Throttled until ${formatDateTime(s.throttled_until)}`}>
        {u.name} answered 429 with Retry-After, so all deliveries to it wait until then.
      </Banner>
    );
  }
  return null;
}

function Banner({ tone, icon: Icon, title, children }: { tone: "warning" | "danger"; icon: LucideIcon; title: string; children: ReactNode }) {
  return (
    <div
      role="status"
      className={cn(
        "mb-4 flex gap-3 rounded-lg border p-4 text-sm",
        tone === "danger" ? "border-red-500/30 bg-red-500/5" : "border-amber-500/30 bg-amber-500/5",
      )}
    >
      <Icon className={cn("mt-0.5 size-4 shrink-0", tone === "danger" ? "text-red-600 dark:text-red-400" : "text-amber-600 dark:text-amber-400")} aria-hidden />
      <div>
        <p className="font-medium">{title}</p>
        <p className="mt-1 text-muted-foreground">{children}</p>
      </div>
    </div>
  );
}

function LiveState({ u }: { u: Upstream }) {
  const s = u.state;
  const { tone, label, icon: Icon } = statusStyle[s.status];
  return (
    <Card>
      <CardHeader>
        <CardTitle>Right now</CardTitle>
        <CardDescription>On the Hookyard instance serving this page.</CardDescription>
      </CardHeader>
      <CardContent className="space-y-4">
        <dl>
          <Row label="Status">
            <Badge tone={tone}>
              <Icon className="size-3.5" aria-hidden /> {label}
            </Badge>
          </Row>
          <Row label="Circuit breaker">
            {s.breaker === "off" ? "Off" : s.breaker.replace("_", "-")}
            {s.breaker_since && s.breaker !== "off" && <span className="text-muted-foreground"> · since {formatRelative(s.breaker_since)}</span>}
          </Row>
        </dl>
        {u.limits.max_concurrency ? (
          <Usage label="In flight" used={s.in_flight} limit={u.limits.max_concurrency} hint="Deliveries in progress, against max_concurrency." />
        ) : (
          <dl>
            <Row label="In flight">{formatCount(s.in_flight)}</Row>
          </dl>
        )}
        {u.limits.rate_limit && u.limits.burst && s.available_tokens !== null && s.available_tokens !== undefined && (
          <Usage
            label="Tokens available"
            used={s.available_tokens}
            limit={u.limits.burst}
            hint={`Requests that can be sent at once; refills at ${u.limits.rate_limit}.`}
          />
        )}
      </CardContent>
    </Card>
  );
}

function EventHistory({ name }: { name: string }) {
  const events = useQuery({
    queryKey: ["upstream", name, "events"],
    queryFn: () => api<{ data: UpstreamEvent[] }>(`/v1/upstreams/${encodeURIComponent(name)}/events`, { query: { limit: 20 } }),
    refetchInterval: REFRESH_MS,
  });
  const list = events.data?.data ?? [];
  return (
    <Card>
      <CardHeader>
        <CardTitle className="flex items-center gap-2">
          <History className="size-4" aria-hidden /> History
        </CardTitle>
        <CardDescription>Breaker transitions, pauses and resumes.</CardDescription>
      </CardHeader>
      <CardContent>
        {events.isError ? (
          <ErrorState error={events.error} />
        ) : events.isPending ? (
          <Skeleton className="h-24" />
        ) : list.length === 0 ? (
          <p className="text-sm text-muted-foreground">Nothing has happened yet: the breaker has stayed closed and no one paused deliveries.</p>
        ) : (
          <ol className="space-y-3" aria-label="Upstream history">
            {list.map((e) => (
              <li key={e.id} className="text-sm">
                <p className="flex flex-wrap items-baseline justify-between gap-x-3">
                  <span className="font-medium">{eventLabel[e.kind] ?? e.kind}</span>
                  <span className="text-xs text-muted-foreground" title={formatDateTime(e.at)}>
                    {formatRelative(e.at)}
                  </span>
                </p>
                {(e.reason || e.actor) && (
                  <p className="text-muted-foreground">
                    {e.reason}
                    {e.actor && e.actor !== "system" && <span> · by {e.actor}</span>}
                  </p>
                )}
              </li>
            ))}
          </ol>
        )}
      </CardContent>
    </Card>
  );
}

function Configuration({ u }: { u: Upstream }) {
  const [asYaml, setAsYaml] = useState(false);
  const r = u.retry;
  return (
    <Card>
      <CardHeader className="flex-row items-center justify-between gap-3">
        <div>
          <CardTitle>Configuration</CardTitle>
          <CardDescription>From hookyard.yaml, with defaults filled in. Header values are never shown.</CardDescription>
        </div>
        <button type="button" onClick={() => setAsYaml((v) => !v)} className="shrink-0 text-xs text-muted-foreground underline-offset-4 hover:text-foreground hover:underline">
          {asYaml ? "View as list" : "View as YAML"}
        </button>
      </CardHeader>
      <CardContent>
        {asYaml ? (
          <CodeBlock code={upstreamYaml(u)} />
        ) : (
          <div className="grid gap-x-8 md:grid-cols-2">
            <dl className="divide-y">
              <Row label="Base URL">
                <span className="font-mono text-xs break-all">{u.base_url}</span>
              </Row>
              <Row label="Timeout">{u.timeout}</Row>
              <Row label="Retry">
                {r.preset ?? "custom"} · {r.max_attempts} attempts, {r.initial_interval}–{r.max_interval} ×{r.multiplier}, max age {r.max_age}
              </Row>
              <Row label="Dedupe window">{u.dedupe_window}</Row>
              <Row label="No response to POST/PATCH">{u.on_timeout === "retry" ? "Retry" : "Mark unknown"}</Row>
              <Row label="Callback URL">{u.callback_url ? <span className="font-mono text-xs break-all">{u.callback_url}</span> : "—"}</Row>
              <Row label="Headers">{u.header_names.length ? <span className="font-mono text-xs">{u.header_names.join(", ")}</span> : "—"}</Row>
            </dl>
            <dl className="divide-y">
              <Row label="Rate limit">{u.limits.rate_limit ? `${u.limits.rate_limit}, burst ${u.limits.burst}` : "None"}</Row>
              <Row label="Max concurrency">{u.limits.max_concurrency ?? "None"}</Row>
              <Row label="Circuit breaker">
                {u.breaker
                  ? `opens at ${Math.round(u.breaker.failure_rate * 100)}% failures (min ${u.breaker.min_calls} in ${u.breaker.window}) or ${u.breaker.consecutive_failures} in a row; cooldown ${u.breaker.cooldown}, ${u.breaker.probes} probes`
                  : "Off"}
              </Row>
            </dl>
          </div>
        )}
        {!asYaml && u.classify.length > 0 && (
          <div className="mt-4">
            <h4 className="mb-2 text-xs font-medium text-muted-foreground">Classification rules (first match wins)</h4>
            <ol className="space-y-1.5 text-sm">
              {u.classify.map((rule) => (
                <li key={rule.name} className="flex flex-wrap items-baseline gap-x-2 rounded-md border px-3 py-2">
                  <span className="font-medium">{rule.name}</span>
                  <span className="text-muted-foreground">
                    {[rule.status && `status ${rule.status}`, rule.body && `${rule.body} ${rule.condition}`].filter(Boolean).join(" and ")}
                  </span>
                  <span className="ml-auto">
                    <Badge tone={rule.then === "success" ? "success" : rule.then === "retry" ? "warning" : "danger"}>→ {rule.then}</Badge>
                  </span>
                </li>
              ))}
            </ol>
          </div>
        )}
      </CardContent>
    </Card>
  );
}

export function UpstreamDetailPage() {
  const { name } = route.useParams();
  const range = route.useSearch().range ?? "1h";
  const r = ranges[range];
  const navigate = useNavigate();
  const [action, setAction] = useState<PauseAction | null>(null);

  const upstream = useQuery({
    queryKey: ["upstream", name],
    queryFn: () => api<Upstream>(`/v1/upstreams/${encodeURIComponent(name)}`),
    refetchInterval: REFRESH_MS,
  });
  const overview = useQuery({
    queryKey: ["stats", "overview", range],
    queryFn: () => api<StatsOverview>("/v1/stats/overview", { query: { window: `${r.hours}h` } }),
    refetchInterval: 10_000,
  });
  const series = useQuery({
    queryKey: ["stats", "timeseries", range, name],
    queryFn: () =>
      api<StatsTimeseries>("/v1/stats/timeseries", {
        query: { upstream: name, from: new Date(Date.now() - r.hours * 3_600_000).toISOString(), step: r.step },
      }),
    refetchInterval: 10_000,
  });

  if (upstream.isError) {
    const notFound = upstream.error instanceof ApiError && upstream.error.status === 404;
    return (
      <>
        <BackLink />
        {notFound ? (
          <EmptyState icon={<Ban />} title="Upstream not found">
            No upstream named <code className="font-mono">{name}</code> is configured. Check hookyard.yaml.
          </EmptyState>
        ) : (
          <ErrorState error={upstream.error} />
        )}
      </>
    );
  }
  if (upstream.isPending) {
    return (
      <div className="space-y-4">
        <Skeleton className="h-8 w-1/3" />
        <Skeleton className="h-24" />
        <Skeleton className="h-64" />
      </div>
    );
  }

  const u = upstream.data;
  const stats = overview.data?.data.find((s) => s.upstream === name);
  const paused = u.state.status === "paused";
  const { tone, label, icon: Icon } = statusStyle[u.state.status];

  return (
    <>
      <BackLink />
      <PageHeader
        title={u.name}
        description={
          <span className="flex flex-wrap items-center gap-2">
            <Badge tone={tone}>
              <Icon className="size-3.5" aria-hidden /> {label}
            </Badge>
            <span className="font-mono text-xs break-all">{u.base_url}</span>
          </span>
        }
        actions={
          <>
            <Link to="/requests" search={{ upstream: name }} className={buttonClass("outline")}>
              View requests
            </Link>
            {paused ? (
              <Button onClick={() => setAction("resume")}>
                <Play /> Resume
              </Button>
            ) : (
              <Button variant="outline" onClick={() => setAction("pause")}>
                <Pause /> Pause
              </Button>
            )}
          </>
        }
      />
      <PauseDialog upstream={u} action={action} onClose={() => setAction(null)} />
      <StateBanner u={u} />

      <div className="mb-4 flex flex-wrap items-center justify-between gap-3">
        <h2 className="text-sm font-semibold">Last {r.label}</h2>
        <RangePicker value={range} onChange={(key) => void navigate({ to: "/upstreams/$name", params: { name }, search: { range: key }, replace: true })} />
      </div>

      <Card className="mb-6">
        <CardContent className="pt-5">
          {overview.isError ? (
            <ErrorState error={overview.error} />
          ) : !stats ? (
            <Skeleton className="h-14" />
          ) : (
            <dl className="grid grid-cols-2 gap-4 sm:grid-cols-3 lg:grid-cols-5">
              <Metric label="Success rate" value={formatPercent(stats.success_rate)} hint={`${formatCount(stats.succeeded)} delivered, ${formatCount(stats.dead)} dead`} />
              <Metric label="Attempts / min" value={formatRate(stats.throughput_per_min)} hint={`${formatCount(stats.failed_attempts)} failed`} />
              <Metric label="Latency p50" value={formatMs(stats.latency_ms.p50)} hint={`p95 ${formatMs(stats.latency_ms.p95)} · p99 ${formatMs(stats.latency_ms.p99)}`} />
              <Metric
                label="Waiting"
                value={formatCount(stats.queue_depth)}
                hint={stats.oldest_pending_age_seconds !== null ? `oldest ${formatAge(stats.oldest_pending_age_seconds)}` : "queue empty"}
                tone={(stats.oldest_pending_age_seconds ?? 0) > 300 ? "warning" : undefined}
              />
              <Metric label="Dead letters" value={formatCount(stats.dlq_size)} hint={stats.dlq_size > 0 ? "need attention" : "none"} tone={stats.dlq_size > 0 ? "warning" : undefined} />
            </dl>
          )}
        </CardContent>
      </Card>

      <div className="grid gap-6 lg:grid-cols-[1fr_20rem]">
        <div className="min-w-0 space-y-6">
          <Card>
            <CardHeader>
              <CardTitle>Deliveries</CardTitle>
              <CardDescription>Per {stepLabel(range)}.</CardDescription>
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
          <Card>
            <CardHeader>
              <CardTitle>Latency</CardTitle>
              <CardDescription>Time to a response, per attempt, per {stepLabel(range)}.</CardDescription>
            </CardHeader>
            <CardContent>
              {series.isError ? (
                <ErrorState error={series.error} />
              ) : series.data ? (
                <Suspense fallback={<Skeleton className="h-48" />}>
                  <LatencyChart data={series.data} rangeHours={r.hours} />
                </Suspense>
              ) : (
                <Skeleton className="h-48" />
              )}
            </CardContent>
          </Card>
        </div>
        <div className="min-w-0 space-y-6">
          <LiveState u={u} />
          <EventHistory name={name} />
        </div>
      </div>

      <div className="mt-6">
        <Configuration u={u} />
      </div>
    </>
  );
}

function BackLink() {
  return (
    <Link to="/" className="mb-4 inline-flex items-center gap-1 text-sm text-muted-foreground hover:text-foreground">
      <ArrowLeft className="size-4" aria-hidden /> Overview
    </Link>
  );
}
