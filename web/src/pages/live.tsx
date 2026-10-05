import { useCallback, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { getRouteApi, Link, useNavigate } from "@tanstack/react-router";
import { Pause, Play, Radio, Trash2 } from "lucide-react";
import { PageHeader } from "@/components/layout/app-shell";
import { StatusBadge } from "@/components/status-badge";
import { Badge, type BadgeTone } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { EmptyState } from "@/components/ui/feedback";
import { Label, Select } from "@/components/ui/input";
import { api, type RequestStatus, type Schemas, type Upstream } from "@/lib/api";
import { formatDateTime, formatMs } from "@/lib/format";
import { useEventStream, type ConnectionState, type LiveEvent } from "@/lib/live";
import { cn } from "@/lib/utils";

const route = getRouteApi("/live");
// Rows kept on screen (and events buffered while paused).
const MAX_ROWS = 500;

const statusFilters = {
  "": { label: "Everything", statuses: "" },
  problems: { label: "Problems (failed, dead, unknown)", statuses: "failed,dead,unknown" },
  succeeded: { label: "Succeeded", statuses: "succeeded" },
  dead: { label: "Dead", statuses: "dead" },
  unknown: { label: "Unknown", statuses: "unknown" },
} as const;
type StatusFilter = keyof typeof statusFilters;

export function validateLiveSearch(search: Record<string, unknown>): { upstream?: string; show?: Exclude<StatusFilter, ""> } {
  const out: { upstream?: string; show?: Exclude<StatusFilter, ""> } = {};
  if (typeof search.upstream === "string" && search.upstream) out.upstream = search.upstream;
  if (typeof search.show === "string" && search.show in statusFilters && search.show !== "") out.show = search.show as Exclude<StatusFilter, "">;
  return out;
}

const connectionStyle: Record<ConnectionState, { label: string; dot: string }> = {
  connecting: { label: "Connecting…", dot: "bg-muted-foreground" },
  open: { label: "Live", dot: "bg-emerald-500 animate-pulse" },
  reconnecting: { label: "Reconnecting…", dot: "bg-amber-500" },
};

const outcomeStyle: Record<Schemas["AttemptEvent"]["outcome"], { tone: BadgeTone; label: string }> = {
  success: { tone: "success", label: "Success" },
  retryable_failure: { tone: "warning", label: "Retryable failure" },
  permanent_failure: { tone: "danger", label: "Permanent failure" },
  unknown: { tone: "warning", label: "No response" },
};

const upstreamKind: Record<string, { tone: BadgeTone; label: string }> = {
  breaker_open: { tone: "danger", label: "Breaker opened" },
  breaker_half_open: { tone: "warning", label: "Breaker probing" },
  breaker_closed: { tone: "success", label: "Breaker closed" },
  paused: { tone: "warning", label: "Paused" },
  resumed: { tone: "success", label: "Resumed" },
};

const actionLabel: Record<Schemas["RequestEvent"]["action"], string> = {
  enqueued: "Enqueued",
  canceled: "Canceled",
  resolved: "Resolved",
  replayed: "Replayed",
};

function time(at: string): string {
  return new Date(at).toLocaleTimeString(undefined, { hour: "2-digit", minute: "2-digit", second: "2-digit" });
}

function Row({ e }: { e: LiveEvent }) {
  const at = (
    <time dateTime={e.at} title={formatDateTime(e.at)} className="w-20 shrink-0 font-mono text-xs text-muted-foreground tabular-nums">
      {time(e.at)}
    </time>
  );
  if (e.type === "upstream") {
    const d = e.data as Schemas["UpstreamEventData"];
    const k = upstreamKind[d.kind] ?? { tone: "neutral" as const, label: d.kind };
    return (
      <li className="flex flex-wrap items-center gap-x-3 gap-y-1 bg-muted/40 px-4 py-2 text-sm">
        {at}
        <Badge tone={k.tone}>{k.label}</Badge>
        <Link to="/upstreams/$name" params={{ name: d.upstream }} className="font-medium hover:underline">
          {d.upstream}
        </Link>
        <span className="min-w-0 text-muted-foreground">
          {d.reason}
          {d.actor && d.actor !== "hookyard" && ` · by ${d.actor}`}
        </span>
      </li>
    );
  }
  const d = e.data as Schemas["AttemptEvent"] | Schemas["RequestEvent"];
  const attempt = e.type === "attempt" ? (d as Schemas["AttemptEvent"]) : null;
  return (
    <li className="flex flex-wrap items-center gap-x-3 gap-y-1 px-4 py-2 text-sm">
      {at}
      {attempt ? (
        <Badge tone={outcomeStyle[attempt.outcome].tone}>{outcomeStyle[attempt.outcome].label}</Badge>
      ) : (
        <Badge tone="neutral">{actionLabel[(d as Schemas["RequestEvent"]).action]}</Badge>
      )}
      <span className="shrink-0 text-muted-foreground">{d.upstream}</span>
      {/* On phones the path gets a line of its own. */}
      <Link
        to="/requests/$id"
        params={{ id: d.request_id }}
        className="order-last min-w-0 basis-full truncate font-mono text-xs hover:underline sm:order-none sm:basis-0 sm:flex-1"
      >
        {d.method} {d.path}
      </Link>
      <span className="ml-auto flex shrink-0 items-center gap-2 text-xs text-muted-foreground tabular-nums sm:ml-0">
        {attempt && (
          <>
            {attempt.status_code != null && <span className="font-mono text-foreground">{attempt.status_code}</span>}
            <span>{formatMs(attempt.duration_ms)}</span>
            <span>#{attempt.attempt}</span>
          </>
        )}
        <StatusBadge status={d.status as RequestStatus} />
      </span>
    </li>
  );
}

export function LivePage() {
  const { upstream, show = "" } = route.useSearch();
  const navigate = useNavigate();
  const [rows, setRows] = useState<LiveEvent[]>([]);
  const [paused, setPaused] = useState(false);
  const [buffered, setBuffered] = useState<LiveEvent[]>([]);

  const upstreams = useQuery({ queryKey: ["upstreams"], queryFn: () => api<{ data: Upstream[] }>("/v1/upstreams"), staleTime: 60_000 });

  const params = new URLSearchParams();
  if (upstream) params.set("upstream", upstream);
  if (statusFilters[show].statuses) params.set("status", statusFilters[show].statuses);
  const query = params.toString();

  const onEvent = useCallback(
    (e: LiveEvent) => {
      if (paused) setBuffered((b) => [e, ...b].slice(0, MAX_ROWS));
      else setRows((r) => [e, ...r].slice(0, MAX_ROWS));
    },
    [paused],
  );
  const { state, droppedAt } = useEventStream(query, onEvent);

  const setFilter = (next: { upstream?: string; show?: string }) => {
    setRows([]);
    setBuffered([]);
    const search = validateLiveSearch({ upstream, show, ...next });
    void navigate({ to: "/live", search, replace: true });
  };
  const resume = () => {
    setRows((r) => [...buffered, ...r].slice(0, MAX_ROWS));
    setBuffered([]);
    setPaused(false);
  };
  const conn = connectionStyle[state];

  return (
    <>
      <PageHeader
        title="Live"
        description="Deliveries and changes as they happen on this Hookyard instance. Nothing here is stored: it starts empty."
        actions={
          <>
            <span className="inline-flex items-center gap-2 text-sm" role="status" aria-live="polite">
              <span className={cn("size-2 rounded-full", conn.dot)} aria-hidden />
              {conn.label}
            </span>
            {paused ? (
              <Button onClick={resume}>
                <Play /> Resume{buffered.length > 0 && ` (${buffered.length} new)`}
              </Button>
            ) : (
              <Button variant="outline" onClick={() => setPaused(true)}>
                <Pause /> Pause
              </Button>
            )}
            <Button variant="ghost" onClick={() => setRows([])} disabled={rows.length === 0} aria-label="Clear">
              <Trash2 /> <span className="sr-only sm:not-sr-only">Clear</span>
            </Button>
          </>
        }
      />

      <div className="mb-4 flex flex-wrap items-end gap-3">
        <div className="flex flex-col gap-1.5">
          <Label htmlFor="live-upstream">Upstream</Label>
          <Select id="live-upstream" value={upstream ?? ""} onChange={(e) => setFilter({ upstream: e.target.value })}>
            <option value="">All upstreams</option>
            {(upstreams.data?.data ?? []).map((u) => (
              <option key={u.name} value={u.name}>
                {u.name}
              </option>
            ))}
          </Select>
        </div>
        <div className="flex flex-col gap-1.5">
          <Label htmlFor="live-show">Show</Label>
          <Select id="live-show" value={show} onChange={(e) => setFilter({ show: e.target.value })}>
            {(Object.keys(statusFilters) as StatusFilter[]).map((k) => (
              <option key={k} value={k}>
                {statusFilters[k].label}
              </option>
            ))}
          </Select>
        </div>
      </div>

      {droppedAt && (
        <p role="status" className="mb-4 rounded-lg border border-amber-500/30 bg-amber-500/5 p-3 text-sm">
          The browser fell behind at {time(droppedAt.toISOString())}, so some events were skipped. The stream reconnected; narrow the filters if this keeps
          happening.
        </p>
      )}

      <Card className="overflow-hidden">
        {rows.length === 0 ? (
          <EmptyState icon={<Radio />} title={paused ? "Paused" : "Waiting for deliveries…"}>
            {paused ? "New events are kept until you resume." : "Events appear here as soon as they happen. Send a request to see one."}
          </EmptyState>
        ) : (
          <ol className="divide-y" aria-label="Live events">
            {rows.map((e) => (
              <Row key={e.id} e={e} />
            ))}
          </ol>
        )}
      </Card>
      {rows.length >= MAX_ROWS && <p className="mt-2 text-xs text-muted-foreground">Showing the latest {MAX_ROWS} events.</p>}
    </>
  );
}
