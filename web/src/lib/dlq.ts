import { useQuery } from "@tanstack/react-query";
import { api, type DLQSummary } from "./api";

export const dlqSummaryKey = (upstream?: string) => ["dlq", "summary", upstream ?? ""] as const;

/** The dead-letter queue summary, refreshed periodically. */
export function useDLQSummary(upstream?: string, refetchInterval = 15_000) {
  return useQuery({
    queryKey: dlqSummaryKey(upstream),
    queryFn: () => api<DLQSummary>("/v1/dlq", { query: { upstream } }),
    refetchInterval,
  });
}

/** Human-readable failure reason for a DLQ group. */
export function reasonLabel(errorCode: string, statusCode: number | null | undefined): string {
  switch (errorCode) {
    case "http_status":
      return statusCode ? `HTTP ${statusCode}` : "HTTP error";
    case "timeout":
      return "Timed out";
    case "connection":
      return "Connection failed";
    case "max_age_exceeded":
      return "Retry budget (max age) ran out";
    case "canceled":
      return "Canceled";
    default:
      return "Internal error";
  }
}
