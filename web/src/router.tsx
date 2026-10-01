import { createRootRoute, createRoute, createRouter, Outlet } from "@tanstack/react-router";
import { AppShell } from "@/components/layout/app-shell";
import { DLQPage } from "@/pages/dlq";
import { NotFoundPage } from "@/pages/not-found";
import { OverviewPage, ranges, type RangeKey } from "@/pages/overview";
import { RequestDetailPage } from "@/pages/request-detail";
import { RequestsPage, validateRequestsSearch } from "@/pages/requests";

const rootRoute = createRootRoute({
  component: () => (
    <AppShell>
      <Outlet />
    </AppShell>
  ),
  notFoundComponent: NotFoundPage,
});

const overviewRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/",
  component: OverviewPage,
  validateSearch: (search: Record<string, unknown>): { range?: RangeKey } =>
    typeof search.range === "string" && search.range in ranges ? { range: search.range as RangeKey } : {},
});

const requestsRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/requests",
  component: RequestsPage,
  validateSearch: validateRequestsSearch,
});

const requestDetailRoute = createRoute({ getParentRoute: () => rootRoute, path: "/requests/$id", component: RequestDetailPage });

const dlqRoute = createRoute({ getParentRoute: () => rootRoute, path: "/dlq", component: DLQPage });

const routeTree = rootRoute.addChildren([overviewRoute, requestsRoute, requestDetailRoute, dlqRoute]);

export const router = createRouter({ routeTree, defaultPreload: "intent" });

declare module "@tanstack/react-router" {
  interface Register {
    router: typeof router;
  }
}
