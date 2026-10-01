import { useEffect } from "react";
import { QueryClient, QueryClientProvider, useQueryClient } from "@tanstack/react-query";
import { RouterProvider } from "@tanstack/react-router";
import { ErrorState, Spinner } from "@/components/ui/feedback";
import { ApiError, setUnauthorizedHandler } from "@/lib/api";
import { useSession } from "@/lib/session";
import { SignInPage } from "@/pages/sign-in";
import { router } from "@/router";

export function createQueryClient(): QueryClient {
  return new QueryClient({
    defaultOptions: {
      queries: {
        staleTime: 5_000,
        refetchOnWindowFocus: true,
        // Don't retry client errors such as 404 or 422.
        retry: (count, err) => !(err instanceof ApiError && err.status >= 400 && err.status < 500) && count < 2,
      },
    },
  });
}

function Gate() {
  const qc = useQueryClient();
  const session = useSession();

  // An expired session anywhere in the app returns to the sign-in page.
  useEffect(() => {
    setUnauthorizedHandler(() => qc.setQueryData(["session"], null));
  }, [qc]);

  if (session.isPending) {
    return (
      <div className="flex min-h-dvh items-center justify-center text-muted-foreground">
        <Spinner className="size-5" />
      </div>
    );
  }
  if (session.isError) {
    return (
      <div className="mx-auto max-w-md p-8">
        <ErrorState error={session.error} />
      </div>
    );
  }
  if (!session.data) return <SignInPage />;
  return <RouterProvider router={router} />;
}

export function App({ queryClient = createQueryClient() }: { queryClient?: QueryClient }) {
  return (
    <QueryClientProvider client={queryClient}>
      <Gate />
    </QueryClientProvider>
  );
}
