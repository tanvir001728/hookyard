import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api, ApiError } from "./api";

export interface Session {
  actor: string;
}

const sessionKey = ["session"] as const;

/** The current dashboard session, or null when signed out. */
export function useSession() {
  return useQuery({
    queryKey: sessionKey,
    queryFn: async (): Promise<Session | null> => {
      try {
        return await api<Session>("/ui/session");
      } catch (err) {
        if (err instanceof ApiError && err.status === 401) return null;
        throw err;
      }
    },
    staleTime: Infinity,
    retry: false,
  });
}

export function useSignIn() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (token: string) => api<Session>("/ui/session", { method: "POST", body: { token } }),
    onSuccess: (session) => qc.setQueryData(sessionKey, { actor: session.actor }),
  });
}

export function useSignOut() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: () => api<void>("/ui/session", { method: "DELETE" }),
    onSuccess: () => {
      // Update the session the sign-in gate is observing, then drop every other
      // cached response. (qc.clear() would remove the observed query too, and
      // the gate would never see the sign-out.)
      qc.setQueryData(sessionKey, null);
      qc.removeQueries({ predicate: (q) => q.queryKey[0] !== sessionKey[0] });
    },
  });
}
