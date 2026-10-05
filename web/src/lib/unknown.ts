import { useQuery } from "@tanstack/react-query";
import { api, type RequestList } from "./api";

// Under ["requests"], so actions that invalidate request lists (resolve, replay) refresh these too.
export const unknownCountKey = ["requests", "unknown", "count"] as const;

/** How many requests are waiting for someone to settle them. */
export function useUnknownCount(refetchInterval = 30_000) {
  return useQuery({
    queryKey: unknownCountKey,
    queryFn: async () => (await api<RequestList>("/v1/requests", { query: { status: "unknown", count: true, limit: 1 } })).total ?? 0,
    refetchInterval,
  });
}
