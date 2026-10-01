import { useState, type FormEvent } from "react";
import { useInfiniteQuery, useQuery } from "@tanstack/react-query";
import { getRouteApi, Link, useNavigate } from "@tanstack/react-router";
import { ListFilter, Search, SearchX, X } from "lucide-react";
import { PageHeader } from "@/components/layout/app-shell";
import { allStatuses, StatusBadge, statusLabel } from "@/components/status-badge";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { EmptyState, ErrorState, Skeleton, Spinner } from "@/components/ui/feedback";
import { Input, Select } from "@/components/ui/input";
import { Table, TBody, TD, TH, THead, TR } from "@/components/ui/table";
import { api, type RequestList, type RequestStatus, type Upstream } from "@/lib/api";
import { formatDateTime, formatRelative } from "@/lib/format";
import { cn } from "@/lib/utils";

export const sinceOptions = { "1h": 1, "24h": 24, "7d": 168, "30d": 720 } as const;
export type Since = keyof typeof sinceOptions;

export interface RequestsSearch {
  upstream?: string;
  status?: string;
  q?: string;
  tag?: string;
  since?: Since;
}

/** Keeps only known, non-empty search params. */
export function validateRequestsSearch(s: Record<string, unknown>): RequestsSearch {
  const str = (v: unknown) => (typeof v === "string" && v.trim() ? v.trim() : undefined);
  const out: RequestsSearch = {};
  const upstream = str(s.upstream);
  if (upstream) out.upstream = upstream;
  const status = str(s.status)
    ?.split(",")
    .filter((x) => (allStatuses as string[]).includes(x))
    .join(",");
  if (status) out.status = status;
  const q = str(s.q);
  if (q) out.q = q;
  const tag = str(s.tag);
  if (tag) out.tag = tag;
  const since = str(s.since);
  if (since && since in sinceOptions) out.since = since as Since;
  return out;
}

const route = getRouteApi("/requests");
const PAGE_SIZE = 50;

function StatusChips({ selected, onChange }: { selected: RequestStatus[]; onChange: (s: RequestStatus[]) => void }) {
  return (
    <div role="group" aria-label="Status" className="flex flex-wrap gap-1.5">
      {allStatuses.map((s) => {
        const on = selected.includes(s);
        return (
          <button
            key={s}
            type="button"
            aria-pressed={on}
            onClick={() => onChange(on ? selected.filter((x) => x !== s) : [...selected, s])}
            className={cn(
              "rounded-full border px-2.5 py-0.5 text-xs transition-colors",
              on ? "border-primary bg-primary/10 font-medium text-primary" : "text-muted-foreground hover:bg-muted hover:text-foreground",
            )}
          >
            {statusLabel(s)}
          </button>
        );
      })}
    </div>
  );
}

export function RequestsPage() {
  const search = route.useSearch();
  const navigate = useNavigate();
  const [q, setQ] = useState(search.q ?? "");
  const [tag, setTag] = useState(search.tag ?? "");

  const setSearch = (patch: Partial<RequestsSearch>) =>
    void navigate({ to: "/requests", search: (prev) => validateRequestsSearch({ ...prev, ...patch }), replace: true });

  const statuses = (search.status?.split(",") ?? []) as RequestStatus[];
  const upstreams = useQuery({ queryKey: ["upstreams"], queryFn: () => api<{ data: Upstream[] }>("/v1/upstreams"), staleTime: 60_000 });

  const requests = useInfiniteQuery({
    queryKey: ["requests", search],
    initialPageParam: undefined as string | undefined,
    queryFn: ({ pageParam, signal }) =>
      api<RequestList>("/v1/requests", {
        signal,
        query: {
          upstream: search.upstream,
          status: search.status,
          dedupe_key: search.q,
          tag: search.tag ? [search.tag] : undefined,
          created_after: search.since ? new Date(Date.now() - sinceOptions[search.since] * 3_600_000).toISOString() : undefined,
          limit: PAGE_SIZE,
          cursor: pageParam,
        },
      }),
    getNextPageParam: (last) => last.next_cursor ?? undefined,
    refetchInterval: 10_000,
  });

  const onSearch = (e: FormEvent) => {
    e.preventDefault();
    const value = q.trim();
    // A request ID jumps straight to the request.
    if (/^req_[0-9A-Za-z]+$/.test(value)) {
      void navigate({ to: "/requests/$id", params: { id: value } });
      return;
    }
    setSearch({ q: value || undefined, tag: tag.trim() || undefined });
  };

  const rows = requests.data?.pages.flatMap((p) => p.data) ?? [];
  const filtered = Boolean(search.upstream || search.status || search.q || search.tag || search.since);

  return (
    <>
      <PageHeader title="Requests" description="Every request enqueued through Hookyard, newest first." />

      <Card className="mb-4 p-4">
        <form onSubmit={onSearch} className="flex flex-col gap-3">
          <div className="flex flex-wrap gap-2">
            <div className="relative min-w-56 flex-1">
              <Search className="pointer-events-none absolute top-2.5 left-3 size-4 text-muted-foreground" aria-hidden />
              <Input className="pl-9" placeholder="Request ID or dedupe key" value={q} onChange={(e) => setQ(e.target.value)} aria-label="Request ID or dedupe key" />
            </div>
            <Input className="w-44" placeholder="Tag, e.g. app:orders" value={tag} onChange={(e) => setTag(e.target.value)} aria-label="Tag" />
            <Select
              className="w-44"
              aria-label="Upstream"
              value={search.upstream ?? ""}
              onChange={(e) => setSearch({ upstream: e.target.value || undefined })}
            >
              <option value="">All upstreams</option>
              {upstreams.data?.data.map((u) => (
                <option key={u.name} value={u.name}>
                  {u.name}
                </option>
              ))}
            </Select>
            <Select className="w-36" aria-label="Created" value={search.since ?? ""} onChange={(e) => setSearch({ since: (e.target.value || undefined) as Since | undefined })}>
              <option value="">Any time</option>
              <option value="1h">Last hour</option>
              <option value="24h">Last 24 hours</option>
              <option value="7d">Last 7 days</option>
              <option value="30d">Last 30 days</option>
            </Select>
            <Button type="submit" variant="secondary">
              <ListFilter /> Apply
            </Button>
          </div>
          <div className="flex flex-wrap items-center justify-between gap-2">
            <StatusChips selected={statuses} onChange={(s) => setSearch({ status: s.join(",") || undefined })} />
            {filtered && (
              <Button
                variant="ghost"
                size="sm"
                onClick={() => {
                  setQ("");
                  setTag("");
                  void navigate({ to: "/requests", search: {}, replace: true });
                }}
              >
                <X /> Clear filters
              </Button>
            )}
          </div>
        </form>
      </Card>

      {requests.isError ? (
        <ErrorState error={requests.error} />
      ) : (
        <Card>
          {requests.isPending ? (
            <div className="space-y-3 p-5">
              {[0, 1, 2, 3].map((i) => (
                <Skeleton key={i} className="h-6" />
              ))}
            </div>
          ) : rows.length === 0 ? (
            <EmptyState icon={<SearchX />} title={filtered ? "No requests match these filters" : "No requests yet"}>
              {filtered ? "Try removing a filter." : "Requests appear here as soon as an application enqueues them."}
            </EmptyState>
          ) : (
            <>
              <Table>
                <THead>
                  <TR>
                    <TH>Status</TH>
                    <TH>Request</TH>
                    <TH>Upstream</TH>
                    <TH className="text-right">Attempts</TH>
                    <TH className="text-right">Last response</TH>
                    <TH className="text-right">Created</TH>
                  </TR>
                </THead>
                <TBody>
                  {rows.map((r) => (
                    <TR key={r.id} className="group relative hover:bg-muted/50">
                      <TD>
                        <StatusBadge status={r.status} />
                      </TD>
                      <TD className="max-w-md">
                        <Link to="/requests/$id" params={{ id: r.id }} className="block truncate font-mono text-xs after:absolute after:inset-0">
                          <span className="font-semibold">{r.method}</span> {r.path}
                        </Link>
                        {r.last_error && <p className="truncate text-xs text-muted-foreground">{r.last_error.message}</p>}
                      </TD>
                      <TD className="whitespace-nowrap">{r.upstream}</TD>
                      <TD className="text-right tabular-nums">{r.attempt_count}</TD>
                      <TD className="text-right tabular-nums">{r.last_status_code ?? "—"}</TD>
                      <TD className="text-right whitespace-nowrap text-muted-foreground" title={formatDateTime(r.created_at)}>
                        {formatRelative(r.created_at)}
                      </TD>
                    </TR>
                  ))}
                </TBody>
              </Table>
              {requests.hasNextPage && (
                <div className="flex justify-center border-t p-3">
                  <Button variant="outline" size="sm" onClick={() => void requests.fetchNextPage()} disabled={requests.isFetchingNextPage}>
                    {requests.isFetchingNextPage && <Spinner />} Load more
                  </Button>
                </div>
              )}
            </>
          )}
        </Card>
      )}
    </>
  );
}
