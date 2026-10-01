import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { PartyPopper, RotateCcw } from "lucide-react";
import { PageHeader } from "@/components/layout/app-shell";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { Dialog } from "@/components/ui/dialog";
import { EmptyState, ErrorState, Skeleton, Spinner } from "@/components/ui/feedback";
import { Table, TBody, TD, TH, THead, TR } from "@/components/ui/table";
import { api, type DLQGroup, type DLQReplayRequest, type DLQReplayResult } from "@/lib/api";
import { reasonLabel, useDLQSummary } from "@/lib/dlq";
import { formatCount, formatDateTime, formatRelative } from "@/lib/format";

/** Which dead requests to replay; the dialog adds dry_run itself. */
type ReplayFilter = Omit<DLQReplayRequest, "dry_run">;

interface Target {
  title: string;
  filter: ReplayFilter;
}

function groupFilter(g: DLQGroup): ReplayFilter {
  const f: ReplayFilter = { upstream: g.upstream, error_code: g.error_code };
  if (g.status_code !== null && g.status_code !== undefined) f.status_code = g.status_code;
  return f;
}

/**
 * Confirms a bulk replay. A dry run counts the matching requests first, so the
 * dialog shows exactly how many will be replayed. Keyed per target, so each
 * opening starts fresh.
 */
function ReplayConfirm({ target, onClose }: { target: Target; onClose: () => void }) {
  const qc = useQueryClient();
  // The dry run only counts, so it is a query rather than a mutation.
  const dryRun = useQuery({
    queryKey: ["dlq", "dry-run", target.filter],
    queryFn: () => api<DLQReplayResult>("/v1/dlq/replay", { method: "POST", body: { ...target.filter, dry_run: true } }),
    staleTime: 0,
    gcTime: 0,
  });
  const replay = useMutation({
    mutationFn: () => api<DLQReplayResult>("/v1/dlq/replay", { method: "POST", body: { ...target.filter, dry_run: false } }),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: ["dlq"] });
      void qc.invalidateQueries({ queryKey: ["requests"] });
      void qc.invalidateQueries({ queryKey: ["stats"] });
    },
  });
  const matched = dryRun.data?.matched;

  if (replay.isSuccess) {
    return (
      <>
        <p role="status" className="text-sm">
          Queued <strong>{formatCount(replay.data.replayed)}</strong> {replay.data.replayed === 1 ? "request" : "requests"} for delivery again, each
          with a fresh retry budget.
        </p>
        <div className="mt-5 flex justify-end">
          <Button onClick={onClose} data-autofocus>
            Done
          </Button>
        </div>
      </>
    );
  }

  return (
    <>
      <div className="text-sm text-muted-foreground">
        {dryRun.isPending ? (
          <span className="flex items-center gap-2">
            <Spinner /> Counting matching requests…
          </span>
        ) : dryRun.isError ? (
          <ErrorState error={dryRun.error} />
        ) : matched === 0 ? (
          "No dead requests match any more. They may already have been replayed."
        ) : (
          <>
            <strong className="text-foreground">{formatCount(matched ?? 0)}</strong> dead {matched === 1 ? "request" : "requests"} will be delivered again
            with a fresh retry budget. Previous attempts are kept.
          </>
        )}
      </div>
      {replay.isError && <ErrorState error={replay.error} className="mt-3" />}
      <div className="mt-5 flex justify-end gap-2">
        <Button variant="outline" onClick={onClose} data-autofocus>
          Cancel
        </Button>
        <Button onClick={() => replay.mutate()} disabled={!matched || replay.isPending}>
          {replay.isPending ? <Spinner /> : <RotateCcw />}
          Replay{matched ? ` ${formatCount(matched)}` : ""}
        </Button>
      </div>
    </>
  );
}

function ReplayDialog({ target, onClose }: { target: Target | null; onClose: () => void }) {
  return (
    <Dialog open={target !== null} onClose={onClose} title={target?.title ?? ""}>
      {target && <ReplayConfirm key={JSON.stringify(target.filter)} target={target} onClose={onClose} />}
    </Dialog>
  );
}

export function DLQPage() {
  const summary = useDLQSummary();
  const [target, setTarget] = useState<Target | null>(null);
  const groups = summary.data?.groups ?? [];

  return (
    <>
      <PageHeader
        title="Dead letters"
        description="Requests that exhausted their retries or failed permanently, grouped by upstream and reason."
        actions={
          groups.length > 1 && (
            <Button variant="outline" onClick={() => setTarget({ title: "Replay all dead letters?", filter: {} })}>
              <RotateCcw /> Replay all
            </Button>
          )
        }
      />

      {summary.isError ? (
        <ErrorState error={summary.error} />
      ) : (
        <Card>
          {summary.isPending ? (
            <div className="space-y-3 p-5">
              <Skeleton className="h-6" />
              <Skeleton className="h-6" />
            </div>
          ) : groups.length === 0 ? (
            <EmptyState icon={<PartyPopper />} title="No dead letters">
              Every request was delivered or is still being retried.
            </EmptyState>
          ) : (
            <Table>
              <THead>
                <TR>
                  <TH>Upstream</TH>
                  <TH>Reason</TH>
                  <TH className="text-right">Requests</TH>
                  <TH>Newest</TH>
                  <TH>Oldest</TH>
                  <TH>
                    <span className="sr-only">Actions</span>
                  </TH>
                </TR>
              </THead>
              <TBody>
                {groups.map((g) => {
                  const reason = reasonLabel(g.error_code, g.status_code);
                  return (
                    <TR key={`${g.upstream}/${g.error_code}/${g.status_code ?? ""}`}>
                      <TD className="font-medium whitespace-nowrap">{g.upstream}</TD>
                      <TD className="whitespace-nowrap">
                        {reason}
                        <span className="ml-1.5 font-mono text-xs text-muted-foreground">{g.error_code}</span>
                      </TD>
                      <TD className="text-right font-semibold tabular-nums">{formatCount(g.count)}</TD>
                      <TD className="whitespace-nowrap text-muted-foreground" title={formatDateTime(g.newest_dead_at)}>
                        {formatRelative(g.newest_dead_at)}
                      </TD>
                      <TD className="whitespace-nowrap text-muted-foreground" title={formatDateTime(g.oldest_dead_at)}>
                        {formatRelative(g.oldest_dead_at)}
                      </TD>
                      <TD>
                        <div className="flex justify-end gap-2">
                          <Link
                            to="/requests"
                            search={{ upstream: g.upstream, status: "dead" }}
                            className="inline-flex h-8 items-center rounded-md px-3 text-xs font-medium hover:bg-muted"
                          >
                            Inspect
                          </Link>
                          <Button
                            size="sm"
                            variant="outline"
                            onClick={() => setTarget({ title: `Replay ${g.upstream}: ${reason}?`, filter: groupFilter(g) })}
                            aria-label={`Replay ${g.count} dead requests for ${g.upstream} (${reason})`}
                          >
                            <RotateCcw /> Replay
                          </Button>
                        </div>
                      </TD>
                    </TR>
                  );
                })}
              </TBody>
            </Table>
          )}
        </Card>
      )}
      {summary.data && summary.data.total > 0 && (
        <p className="mt-3 text-xs text-muted-foreground">
          {formatCount(summary.data.total)} dead {summary.data.total === 1 ? "request" : "requests"} in total. Replaying is safe to repeat: replayed
          requests leave the dead-letter queue.
        </p>
      )}

      <ReplayDialog target={target} onClose={() => setTarget(null)} />
    </>
  );
}
