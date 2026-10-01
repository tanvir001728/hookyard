import { useQuery } from "@tanstack/react-query";
import { Server } from "lucide-react";
import { PageHeader } from "@/components/layout/app-shell";
import { Badge } from "@/components/ui/badge";
import { Card } from "@/components/ui/card";
import { EmptyState, ErrorState, Skeleton } from "@/components/ui/feedback";
import { Table, TBody, TD, TH, THead, TR } from "@/components/ui/table";
import { api, type Upstream } from "@/lib/api";

export function OverviewPage() {
  const upstreams = useQuery({
    queryKey: ["upstreams"],
    queryFn: () => api<{ data: Upstream[] }>("/v1/upstreams"),
  });

  return (
    <>
      <PageHeader title="Overview" description="The third-party APIs this Hookyard delivers to." />
      {upstreams.isError ? (
        <ErrorState error={upstreams.error} />
      ) : (
        <Card>
          {upstreams.isPending ? (
            <div className="space-y-2 p-5">
              <Skeleton className="h-5 w-1/3" />
              <Skeleton className="h-5 w-1/2" />
            </div>
          ) : upstreams.data.data.length === 0 ? (
            <EmptyState icon={<Server />} title="No upstreams configured">
              Add upstreams to <code className="font-mono">hookyard.yaml</code> and restart Hookyard.
            </EmptyState>
          ) : (
            <Table>
              <THead>
                <TR>
                  <TH>Upstream</TH>
                  <TH>Base URL</TH>
                  <TH>Timeout</TH>
                  <TH>Retry</TH>
                </TR>
              </THead>
              <TBody>
                {upstreams.data.data.map((u) => (
                  <TR key={u.name}>
                    <TD className="font-medium">{u.name}</TD>
                    <TD className="font-mono text-xs text-muted-foreground">{u.base_url}</TD>
                    <TD>{u.timeout}</TD>
                    <TD>
                      <Badge>{u.retry.preset ?? "custom"}</Badge>{" "}
                      <span className="text-xs text-muted-foreground">{u.retry.max_attempts} attempts</span>
                    </TD>
                  </TR>
                ))}
              </TBody>
            </Table>
          )}
        </Card>
      )}
    </>
  );
}
