import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { RotateCcw } from "lucide-react";
import { Badge, type BadgeTone } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { ErrorState, Spinner } from "@/components/ui/feedback";
import { api, type Callback } from "@/lib/api";
import { formatDateTime, formatRelative } from "@/lib/format";

const statusStyle: Record<Callback["status"], { tone: BadgeTone; label: string }> = {
  pending: { tone: "neutral", label: "Waiting" },
  delivering: { tone: "info", label: "Sending" },
  delivered: { tone: "success", label: "Delivered" },
  failed: { tone: "danger", label: "Failed" },
};

/** The completion callbacks of a request and their delivery to the app. */
export function CallbacksCard({ requestId, url, finished }: { requestId: string; url: string; finished: boolean }) {
  const qc = useQueryClient();
  const key = ["request", requestId, "callbacks"];
  const callbacks = useQuery({
    queryKey: key,
    queryFn: () => api<{ data: Callback[] }>(`/v1/requests/${requestId}/callbacks`),
    // Follow until the request is finished and every callback is settled.
    refetchInterval: (q) => {
      const settled = q.state.data?.data.every((c) => c.status === "delivered" || c.status === "failed");
      return finished && settled && q.state.data?.data.length ? false : 2000;
    },
  });
  const retry = useMutation({
    mutationFn: (id: string) => api<Callback>(`/v1/requests/${requestId}/callbacks/${id}/retry`, { method: "POST" }),
    onSuccess: () => void qc.invalidateQueries({ queryKey: key }),
  });

  const list = callbacks.data?.data ?? [];
  return (
    <Card role="region" aria-label="Callbacks">
      <CardHeader>
        <CardTitle>Callbacks</CardTitle>
        <p className="text-xs break-all text-muted-foreground">
          to <span className="font-mono">{url}</span>
        </p>
      </CardHeader>
      <CardContent>
        {callbacks.isError ? (
          <ErrorState error={callbacks.error} />
        ) : list.length === 0 ? (
          <p className="text-sm text-muted-foreground">Your app is told when this request finishes.</p>
        ) : (
          <ul className="divide-y">
            {list.map((c) => {
              const style = statusStyle[c.status];
              return (
                <li key={c.id} className="flex flex-wrap items-start justify-between gap-3 py-3 first:pt-0 last:pb-0">
                  <div className="min-w-0 space-y-1">
                    <div className="flex flex-wrap items-center gap-2">
                      <Badge tone={style.tone}>{style.label}</Badge>
                      <span className="font-mono text-sm">{c.type}</span>
                      {c.last_status_code != null && <span className="font-mono text-sm text-muted-foreground">{c.last_status_code}</span>}
                    </div>
                    <p className="text-xs text-muted-foreground">
                      {c.attempt_count} {c.attempt_count === 1 ? "attempt" : "attempts"}
                      {c.delivered_at && (
                        <>
                          {" "}
                          · delivered <span title={formatDateTime(c.delivered_at)}>{formatRelative(c.delivered_at)}</span>
                        </>
                      )}
                      {c.status === "pending" && c.next_attempt_at && c.attempt_count > 0 && (
                        <>
                          {" "}
                          · next try <span title={formatDateTime(c.next_attempt_at)}>{formatRelative(c.next_attempt_at)}</span>
                        </>
                      )}
                    </p>
                    {c.last_error && c.status !== "delivered" && <p className="text-sm break-words">{c.last_error}</p>}
                    <p className="font-mono text-xs text-muted-foreground">{c.id}</p>
                  </div>
                  {c.status === "failed" && (
                    <Button size="sm" variant="outline" onClick={() => retry.mutate(c.id)} disabled={retry.isPending}>
                      {retry.isPending && retry.variables === c.id ? <Spinner /> : <RotateCcw />} Send again
                    </Button>
                  )}
                </li>
              );
            })}
          </ul>
        )}
        {retry.isError && <ErrorState error={retry.error} className="mt-3" />}
      </CardContent>
    </Card>
  );
}
