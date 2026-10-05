import { useState, type ReactNode } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { getRouteApi, Link } from "@tanstack/react-router";
import { ArrowLeft, Ban, CheckCircle2, ChevronDown, Clock, HelpCircle, RotateCcw, XCircle } from "lucide-react";
import { CallbacksCard } from "@/components/callbacks-card";
import { PageHeader } from "@/components/layout/app-shell";
import { ResolveDialog, type Resolution } from "@/components/resolve-dialog";
import { finalStatuses, StatusBadge } from "@/components/status-badge";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { CodeBlock, CopyButton } from "@/components/ui/copy-button";
import { EmptyState, ErrorState, Skeleton, Spinner } from "@/components/ui/feedback";
import { api, ApiError, type Attempt, type HookyardRequest } from "@/lib/api";
import { toCurl, toSdk } from "@/lib/copy-as";
import { formatDateTime, formatMs, formatRelative } from "@/lib/format";
import { redactHeaders } from "@/lib/redact";
import { cn } from "@/lib/utils";

const route = getRouteApi("/requests/$id");
const cancelable = ["scheduled", "pending", "failed"];

function Field({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className="min-w-0">
      <dt className="text-xs text-muted-foreground">{label}</dt>
      <dd className="mt-0.5 text-sm break-words">{children}</dd>
    </div>
  );
}

function prettyBody(body: unknown): string {
  if (body === null || body === undefined) return "";
  if (typeof body === "string") return body;
  return JSON.stringify(body, null, 2);
}

/** Pretty-prints a response body if it is JSON. */
function prettyResponse(text: string): string {
  try {
    return JSON.stringify(JSON.parse(text), null, 2);
  } catch {
    return text;
  }
}

function HeaderTable({ headers }: { headers: Record<string, string> }) {
  const entries = Object.entries(redactHeaders(headers)).sort(([a], [b]) => a.localeCompare(b));
  if (entries.length === 0) return <p className="text-sm text-muted-foreground">None</p>;
  return (
    <dl className="grid grid-cols-[minmax(8rem,auto)_1fr] gap-x-4 gap-y-1 font-mono text-xs">
      {entries.map(([k, v]) => (
        <div key={k} className="contents">
          <dt className="text-muted-foreground">{k}</dt>
          <dd className="break-all">{v}</dd>
        </div>
      ))}
    </dl>
  );
}

const outcomeStyle = {
  success: { tone: "success", label: "Success" },
  retryable_failure: { tone: "warning", label: "Retryable failure" },
  permanent_failure: { tone: "danger", label: "Permanent failure" },
  unknown: { tone: "warning", label: "No response" },
} as const;

function AttemptItem({ attempt, last }: { attempt: Attempt; last: boolean }) {
  const [open, setOpen] = useState(last);
  const style = outcomeStyle[attempt.outcome];
  const waitMs = attempt.retry_at ? new Date(attempt.retry_at).getTime() - new Date(attempt.started_at).getTime() - attempt.duration_ms : null;

  return (
    <li className="relative pb-6 pl-8 last:pb-0">
      {!last && <span className="absolute top-6 bottom-0 left-[11px] w-px bg-border" aria-hidden />}
      <span
        className={cn(
          "absolute top-1 left-0 flex size-6 items-center justify-center rounded-full border bg-card text-xs font-semibold tabular-nums",
          attempt.outcome === "success" ? "border-emerald-500/50" : attempt.outcome === "permanent_failure" ? "border-red-500/50" : "border-amber-500/50",
        )}
      >
        {attempt.number}
      </span>
      <div className="flex flex-wrap items-center gap-2">
        <Badge tone={style.tone}>{style.label}</Badge>
        {attempt.status_code !== null && attempt.status_code !== undefined && <span className="font-mono text-sm font-medium">{attempt.status_code}</span>}
        <span className="text-sm text-muted-foreground">
          {formatMs(attempt.duration_ms)} · <span title={formatDateTime(attempt.started_at)}>{formatRelative(attempt.started_at)}</span>
        </span>
      </div>
      {attempt.error && <p className="mt-1 text-sm">{attempt.error.message}</p>}
      {attempt.classified_by && (
        <p className="mt-1 text-xs text-muted-foreground">
          Classified by the upstream's rule <span className="font-medium text-foreground">{attempt.classified_by}</span>
        </p>
      )}
      {attempt.retry_at && (
        <p className="mt-1 flex items-center gap-1.5 text-xs text-muted-foreground">
          <Clock className="size-3.5" aria-hidden />
          Retry {new Date(attempt.retry_at).getTime() > Date.now() ? "scheduled" : "was scheduled"} {formatRelative(attempt.retry_at)}
          {waitMs !== null && waitMs > 0 && ` (backoff ${formatMs(waitMs)})`}
        </p>
      )}
      {attempt.response && (
        <div className="mt-2">
          <button
            type="button"
            onClick={() => setOpen((v) => !v)}
            aria-expanded={open}
            className="flex items-center gap-1 text-xs font-medium text-muted-foreground hover:text-foreground"
          >
            <ChevronDown className={cn("size-3.5 transition-transform", !open && "-rotate-90")} aria-hidden />
            Response
            {attempt.response.body_truncated && <span className="font-normal">(truncated)</span>}
          </button>
          {open && (
            <div className="mt-2 space-y-3 rounded-lg border bg-muted/30 p-3">
              <HeaderTable headers={attempt.response.headers} />
              {attempt.response.body && (
                <pre className="max-h-72 overflow-auto rounded-md bg-card p-3 font-mono text-xs">{prettyResponse(attempt.response.body)}</pre>
              )}
            </div>
          )}
        </div>
      )}
    </li>
  );
}

export function RequestDetailPage() {
  const { id } = route.useParams();
  const qc = useQueryClient();
  const [copyAs, setCopyAs] = useState<"curl" | "sdk">("curl");
  const [resolution, setResolution] = useState<Resolution | null>(null);

  const request = useQuery({
    queryKey: ["request", id],
    queryFn: () => api<HookyardRequest>(`/v1/requests/${id}`),
    // Follow a request live until it reaches a final state.
    refetchInterval: (q) => (q.state.data && finalStatuses.includes(q.state.data.status) ? false : 2000),
  });
  const attempts = useQuery({
    queryKey: ["request", id, "attempts"],
    queryFn: () => api<{ data: Attempt[] }>(`/v1/requests/${id}/attempts`),
    refetchInterval: request.data && finalStatuses.includes(request.data.status) ? false : 2000,
  });

  const action = useMutation({
    mutationFn: (kind: "replay" | "cancel") => api<HookyardRequest>(`/v1/requests/${id}/${kind}`, { method: "POST" }),
    onSuccess: (data) => {
      qc.setQueryData(["request", id], data);
      void qc.invalidateQueries({ queryKey: ["request", id, "attempts"] });
      void qc.invalidateQueries({ queryKey: ["requests"] });
    },
  });

  if (request.isError) {
    const notFound = request.error instanceof ApiError && request.error.status === 404;
    return (
      <>
        <BackLink />
        {notFound ? (
          <EmptyState title="Request not found">
            No request has the ID <code className="font-mono">{id}</code>. It may have been removed by the retention policy.
          </EmptyState>
        ) : (
          <ErrorState error={request.error} />
        )}
      </>
    );
  }
  if (request.isPending) {
    return (
      <div className="space-y-4">
        <Skeleton className="h-8 w-1/2" />
        <Skeleton className="h-40" />
        <Skeleton className="h-64" />
      </div>
    );
  }

  const r = request.data;
  const replayable = finalStatuses.includes(r.status);
  const origin = window.location.origin;
  const list = attempts.data?.data ?? [];

  return (
    <>
      <BackLink />
      <PageHeader
        title={`${r.method} ${r.path}`}
        className="[&_h1]:font-mono [&_h1]:text-lg [&_h1]:break-all"
        description={
          <span className="flex flex-wrap items-center gap-2">
            <StatusBadge status={r.status} />
            <span>to {r.upstream}</span>
            <span aria-hidden>·</span>
            <span className="font-mono text-xs">{r.id}</span>
            <CopyButton text={r.id} label="Copy ID" />
          </span>
        }
        actions={
          <>
            {r.status === "unknown" && (
              <>
                <Button onClick={() => setResolution("succeeded")}>
                  <CheckCircle2 /> Mark as delivered
                </Button>
                <Button variant="outline" onClick={() => setResolution("dead")}>
                  <XCircle /> Mark as failed
                </Button>
              </>
            )}
            {replayable && r.status !== "unknown" && (
              <Button onClick={() => action.mutate("replay")} disabled={action.isPending}>
                {action.isPending && action.variables === "replay" ? <Spinner /> : <RotateCcw />} Replay
              </Button>
            )}
            {cancelable.includes(r.status) && (
              <Button variant="outline" onClick={() => action.mutate("cancel")} disabled={action.isPending}>
                {action.isPending && action.variables === "cancel" ? <Spinner /> : <Ban />} Cancel
              </Button>
            )}
          </>
        }
      />
      {action.isError && <ErrorState error={action.error} className="mb-4" />}
      {r.status === "unknown" && (
        <div role="note" className="mb-4 flex gap-3 rounded-lg border border-amber-500/30 bg-amber-500/5 p-4 text-sm">
          <HelpCircle className="mt-0.5 size-4 shrink-0 text-amber-600 dark:text-amber-400" aria-hidden />
          <div>
            <p className="font-medium">Did this go through? Hookyard can't tell.</p>
            <p className="mt-1 text-muted-foreground">
              The request was sent to {r.upstream}, but no response arrived, so it may or may not have been processed. To avoid a
              duplicate it wasn't retried. Check with {r.upstream}, then mark it as delivered or as failed.
            </p>
            <Link to="/unknown" className="mt-2 inline-block text-primary hover:underline">
              See every request with an unknown outcome
            </Link>
          </div>
        </div>
      )}
      <ResolveDialog request={r} resolution={resolution} onClose={() => setResolution(null)} />
      {action.isSuccess && (
        <p role="status" className="mb-4 rounded-lg border border-emerald-500/30 bg-emerald-500/5 p-3 text-sm">
          {action.variables === "replay" ? "Queued for delivery again with a fresh retry budget." : "Request canceled."}
        </p>
      )}

      <div className="grid gap-6 lg:grid-cols-[1fr_22rem]">
        <div className="min-w-0 space-y-6">
          {r.last_error && r.status !== "succeeded" && (
            <div className="rounded-lg border border-red-500/30 bg-red-500/5 p-4 text-sm">
              <p className="font-medium">
                Last error <span className="font-mono text-xs text-muted-foreground">({r.last_error.code})</span>
              </p>
              <p className="mt-1 text-muted-foreground">{r.last_error.message}</p>
            </div>
          )}

          <Card>
            <CardHeader>
              <CardTitle>Attempts</CardTitle>
            </CardHeader>
            <CardContent>
              {attempts.isError ? (
                <ErrorState error={attempts.error} />
              ) : list.length === 0 ? (
                <p className="text-sm text-muted-foreground">
                  {r.status === "scheduled" ? `Scheduled for ${formatDateTime(r.next_attempt_at)}.` : "No attempts yet."}
                </p>
              ) : (
                <ol>
                  {list.map((a, i) => (
                    <AttemptItem key={a.number} attempt={a} last={i === list.length - 1} />
                  ))}
                </ol>
              )}
            </CardContent>
          </Card>

          {r.callback_url && <CallbacksCard requestId={r.id} url={r.callback_url} finished={replayable} />}

          <Card>
            <CardHeader>
              <CardTitle>Request</CardTitle>
            </CardHeader>
            <CardContent className="space-y-4">
              <div>
                <h4 className="mb-2 text-xs font-medium text-muted-foreground">Headers</h4>
                <HeaderTable headers={r.headers ?? {}} />
              </div>
              <div>
                <h4 className="mb-2 text-xs font-medium text-muted-foreground">Body</h4>
                {r.body === null || r.body === undefined ? (
                  <p className="text-sm text-muted-foreground">No body</p>
                ) : (
                  <pre className="max-h-96 overflow-auto rounded-md border bg-muted/30 p-3 font-mono text-xs">{prettyBody(r.body)}</pre>
                )}
              </div>
            </CardContent>
          </Card>

          <Card>
            <CardHeader className="flex-row items-center justify-between">
              <CardTitle>Send it again from code</CardTitle>
              <div role="tablist" className="inline-flex rounded-lg border p-0.5">
                {(["curl", "sdk"] as const).map((k) => (
                  <button
                    key={k}
                    type="button"
                    role="tab"
                    aria-selected={copyAs === k}
                    onClick={() => setCopyAs(k)}
                    className={cn("rounded-md px-2.5 py-0.5 text-xs font-medium", copyAs === k ? "bg-muted" : "text-muted-foreground")}
                  >
                    {k === "curl" ? "curl" : "TypeScript SDK"}
                  </button>
                ))}
              </div>
            </CardHeader>
            <CardContent>
              <CodeBlock code={copyAs === "curl" ? toCurl(r, origin) : toSdk(r)} />
              <p className="mt-2 text-xs text-muted-foreground">Sensitive header values are replaced with •••••• and need to be filled in.</p>
            </CardContent>
          </Card>
        </div>

        <Card className="h-fit">
          <CardContent className="pt-5">
            <dl className="grid gap-4">
              <Field label="Upstream">{r.upstream}</Field>
              <Field label="Created">
                <span title={formatDateTime(r.created_at)}>{formatRelative(r.created_at)}</span>
              </Field>
              {r.completed_at && (
                <Field label="Finished">
                  <span title={formatDateTime(r.completed_at)}>{formatRelative(r.completed_at)}</span>
                </Field>
              )}
              {r.next_attempt_at && !replayable && (
                <Field label="Next attempt">
                  <span title={formatDateTime(r.next_attempt_at)}>{formatRelative(r.next_attempt_at)}</span>
                </Field>
              )}
              <Field label="Attempts">
                {r.attempt_count} <span className="text-muted-foreground">(up to {r.retry.max_attempts} per run; a replay starts a new run)</span>
              </Field>
              <Field label="Retry policy">
                {r.retry.preset ?? "custom"} · {r.retry.initial_interval}–{r.retry.max_interval}, ×{r.retry.multiplier}, max age {r.retry.max_age}
              </Field>
              <Field label="Timeout">{r.timeout}</Field>
              {r.on_result && (
                <Field label="On result">
                  <span className="font-mono text-xs">{r.on_result}</span>
                </Field>
              )}
              <Field label="Dedupe key">{r.dedupe_key ? <span className="font-mono text-xs">{r.dedupe_key}</span> : "—"}</Field>
              <Field label="Tags">
                {Object.keys(r.tags).length ? (
                  <span className="flex flex-wrap gap-1">
                    {Object.entries(r.tags).map(([k, v]) => (
                      <Badge key={k}>
                        {k}: {v}
                      </Badge>
                    ))}
                  </span>
                ) : (
                  "—"
                )}
              </Field>
            </dl>
          </CardContent>
        </Card>
      </div>
    </>
  );
}

function BackLink() {
  return (
    <Link to="/requests" className="mb-4 inline-flex items-center gap-1 text-sm text-muted-foreground hover:text-foreground">
      <ArrowLeft className="size-4" aria-hidden /> Requests
    </Link>
  );
}
