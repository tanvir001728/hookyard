import type { UpstreamStats } from "./api";

export type Health = "idle" | "healthy" | "degraded" | "failing";

/** Requests waiting longer than this suggest a backlog. */
export const BACKLOG_SECONDS = 300;

/**
 * Summarizes an upstream's state over the selected window:
 * - idle: no traffic and nothing waiting
 * - failing: fewer than 90% of finished requests were delivered
 * - degraded: fewer than 99% delivered, or requests waiting over 5 minutes
 * - healthy: otherwise
 */
export function upstreamHealth(s: UpstreamStats): Health {
  const traffic = s.succeeded + s.dead + s.failed_attempts;
  if (traffic === 0 && s.queue_depth === 0) return "idle";
  if (s.success_rate !== null && s.success_rate < 0.9) return "failing";
  if ((s.success_rate !== null && s.success_rate < 0.99) || (s.oldest_pending_age_seconds ?? 0) > BACKLOG_SECONDS) return "degraded";
  return "healthy";
}

export const healthLabel: Record<Health, string> = {
  idle: "No traffic",
  healthy: "Healthy",
  degraded: "Degraded",
  failing: "Failing",
};
