import { useState } from "react";
import { useInfiniteQuery, useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { getRouteApi, Link, useNavigate } from "@tanstack/react-router";
import { CheckCircle2, CircleHelp, PartyPopper, RotateCcw, XCircle } from "lucide-react";
import { PageHeader } from "@/components/layout/app-shell";
import { ResolveDialog, type Resolution } from "@/components/resolve-dialog";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { Dialog } from "@/components/ui/dialog";
import { EmptyState, ErrorState, Skeleton, Spinner } from "@/components/ui/feedback";
import { Label, Select } from "@/components/ui/input";
import { api, type HookyardRequest, type RequestList, type Upstream } from "@/lib/api";
import { formatCount, formatDateTime, formatRelative } from "@/lib/format";

const route = getRouteApi("/unknown");
const PAGE_SIZE = 25;

export function validateUnknownSearch(search: Record<string, unknown>): { upstream?: string } {
  return typeof search.upstream === "string" && search.upstream !== "" ? { upstream: search.upstream } : {};
}

/** Confirms replaying a request that may already have gone through. */
function ReplayDialog({ request, onClose }: { request: HookyardRequest | null; onClose: () => void }) {
  const qc = useQueryClient();
  const replay = useMutation({
    mutationFn: (id: string) => api<HookyardRequest>(`/v1/requests/${id}/replay`, { method: "POST" }),
    onSuccess: (data) => {
      qc.setQueryData(["request", data.id], data);
      void qc.invalidateQueries({ queryKey: ["requests"] });
      onClose();
    },
  });
  return (
    <Dialog open={request !== null} onClose={onClose} title="Send it again?">
      {request && (
        <div>
          <p className="text-sm text-muted-foreground">
            {request.upstream} may already have processed this request. Sending it again could do the same thing twice, such as
            charging a customer or creating a second shipment. Replay only if you checked that it didn't go through, or if {request.upstream} deduplicates it.
          </p>
          {replay.isError && <ErrorState error={replay.error} className="mt-3" />}
          <div className="mt-5 flex flex-col-reverse gap-2 sm:flex-row sm:justify-end">
            <Button variant="outline" onClick={onClose} data-autofocus>
              Cancel
            </Button>
            <Button variant="destructive" onClick={() => replay.mutate(request.id)} disabled={replay.isPending}>
              {replay.isPending ? <Spinner /> : <RotateCcw />} Replay anyway
            </Button>
          </div>
        </div>
      )}
    </Dialog>
  );
}

function UnknownItem({ r, onResolve, onReplay }: { r: HookyardRequest; onResolve: (res: Resolution) => void; onReplay: () => void }) {
  return (
    <li className="flex flex-col gap-3 p-4 sm:flex-row sm:items-start sm:justify-between">
      <div className="min-w-0 space-y-1">
        <Link to="/requests/$id" params={{ id: r.id }} className="block font-mono text-sm font-medium break-all hover:underline">
          {r.method} {r.path}
        </Link>
        <p className="text-sm text-muted-foreground">
          to <span className="text-foreground">{r.upstream}</span> · sent <span title={formatDateTime(r.completed_at)}>{formatRelative(r.completed_at)}</span> · attempt{" "}
          {r.attempt_count}
        </p>
        {/* Just the cause: the banner above explains what it means. */}
        <p className="text-sm">{r.last_error?.message.split(";")[0] ?? "The request was sent, but no response arrived."}</p>
        {Object.keys(r.tags).length > 0 && (
          <p className="text-xs text-muted-foreground">
            {Object.entries(r.tags)
              .map(([k, v]) => `${k}: ${v}`)
              .join(" · ")}
          </p>
        )}
      </div>
      <div className="flex shrink-0 flex-wrap gap-2">
        <Button size="sm" onClick={() => onResolve("succeeded")}>
          <CheckCircle2 /> Mark as delivered
        </Button>
        <Button size="sm" variant="outline" onClick={() => onResolve("dead")}>
          <XCircle /> Mark as failed
        </Button>
        <Button size="sm" variant="ghost" onClick={onReplay}>
          <RotateCcw /> Replay
        </Button>
      </div>
    </li>
  );
}

export function UnknownPage() {
  const { upstream } = route.useSearch();
  const navigate = useNavigate();
  const [resolving, setResolving] = useState<{ request: HookyardRequest; resolution: Resolution } | null>(null);
  const [replaying, setReplaying] = useState<HookyardRequest | null>(null);

  const upstreams = useQuery({ queryKey: ["upstreams"], queryFn: () => api<{ data: Upstream[] }>("/v1/upstreams"), staleTime: 60_000 });
  const list = useInfiniteQuery({
    queryKey: ["requests", "unknown", "list", upstream ?? ""],
    queryFn: ({ pageParam }) =>
      api<RequestList>("/v1/requests", { query: { status: "unknown", upstream, limit: PAGE_SIZE, cursor: pageParam, count: pageParam === undefined } }),
    initialPageParam: undefined as string | undefined,
    getNextPageParam: (last) => last.next_cursor ?? undefined,
    refetchInterval: 15_000,
  });
  const items = list.data?.pages.flatMap((p) => p.data) ?? [];
  const total = list.data?.pages[0]?.total ?? items.length;

  return (
    <>
      <PageHeader
        title="Unknown outcomes"
        description="Requests that were sent but got no response, so Hookyard can't tell whether the upstream processed them."
      />

      <div role="note" className="mb-4 flex gap-3 rounded-lg border border-amber-500/30 bg-amber-500/5 p-4 text-sm">
        <CircleHelp className="mt-0.5 size-4 shrink-0 text-amber-600 dark:text-amber-400" aria-hidden />
        <p className="text-muted-foreground">
          To avoid doing something twice, Hookyard doesn't retry these POST and PATCH requests. Check with the upstream (its dashboard, or by looking the
          record up), then <span className="text-foreground">mark each one as delivered</span> or{" "}
          <span className="text-foreground">as failed</span> (it moves to the dead-letter queue). Replay only when a duplicate is harmless.
        </p>
      </div>

      <div className="mb-4 flex flex-wrap items-end justify-between gap-3">
        <div className="flex flex-col gap-1.5">
          <Label htmlFor="unknown-upstream">Upstream</Label>
          <Select
            id="unknown-upstream"
            value={upstream ?? ""}
            onChange={(e) => void navigate({ to: "/unknown", search: e.target.value ? { upstream: e.target.value } : {}, replace: true })}
          >
            <option value="">All upstreams</option>
            {(upstreams.data?.data ?? []).map((u) => (
              <option key={u.name} value={u.name}>
                {u.name}
              </option>
            ))}
          </Select>
        </div>
        {list.isSuccess && items.length > 0 && (
          <p className="text-sm text-muted-foreground" role="status">
            {formatCount(total)} {total === 1 ? "request needs" : "requests need"} a decision
          </p>
        )}
      </div>

      {list.isError ? (
        <ErrorState error={list.error} />
      ) : list.isPending ? (
        <Skeleton className="h-40" />
      ) : items.length === 0 ? (
        <Card>
          <EmptyState icon={<PartyPopper />} title="Nothing to settle">
            Every request that was sent got an answer{upstream ? ` from ${upstream}` : ""}.
          </EmptyState>
        </Card>
      ) : (
        <Card>
          <CardContent className="p-0">
            <ul className="divide-y" aria-label="Requests with an unknown outcome">
              {items.map((r) => (
                <UnknownItem
                  key={r.id}
                  r={r}
                  onResolve={(resolution) => setResolving({ request: r, resolution })}
                  onReplay={() => setReplaying(r)}
                />
              ))}
            </ul>
            {list.hasNextPage && (
              <div className="border-t p-3 text-center">
                <Button variant="ghost" size="sm" onClick={() => void list.fetchNextPage()} disabled={list.isFetchingNextPage}>
                  {list.isFetchingNextPage && <Spinner />} Load more
                </Button>
              </div>
            )}
          </CardContent>
        </Card>
      )}

      {resolving && <ResolveDialog request={resolving.request} resolution={resolving.resolution} onClose={() => setResolving(null)} />}
      <ReplayDialog request={replaying} onClose={() => setReplaying(null)} />
    </>
  );
}
