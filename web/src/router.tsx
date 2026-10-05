import { createRootRoute, createRoute, createRouter, Outlet } from "@tanstack/react-router";
import { AppShell } from "@/components/layout/app-shell";
import { DLQPage } from "@/pages/dlq";
import { NotFoundPage } from "@/pages/not-found";
import { validateRangeSearch } from "@/components/range-picker";
import { OverviewPage } from "@/pages/overview";
import { RequestDetailPage } from "@/pages/request-detail";
import { RequestsPage, validateRequestsSearch } from "@/pages/requests";
import { LivePage, validateLiveSearch } from "@/pages/live";
import { UnknownPage, validateUnknownSearch } from "@/pages/unknown";
import { UpstreamDetailPage } from "@/pages/upstream-detail";

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
  validateSearch: validateRangeSearch,
});

const requestsRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/requests",
  component: RequestsPage,
  validateSearch: validateRequestsSearch,
});

const requestDetailRoute = createRoute({ getParentRoute: () => rootRoute, path: "/requests/$id", component: RequestDetailPage });

const upstreamDetailRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/upstreams/$name",
  component: UpstreamDetailPage,
  validateSearch: validateRangeSearch,
});

const liveRoute = createRoute({ getParentRoute: () => rootRoute, path: "/live", component: LivePage, validateSearch: validateLiveSearch });

const unknownRoute = createRoute({ getParentRoute: () => rootRoute, path: "/unknown", component: UnknownPage, validateSearch: validateUnknownSearch });

const dlqRoute = createRoute({ getParentRoute: () => rootRoute, path: "/dlq", component: DLQPage });

const routeTree = rootRoute.addChildren([overviewRoute, requestsRoute, requestDetailRoute, upstreamDetailRoute, liveRoute, unknownRoute, dlqRoute]);

export const router = createRouter({ routeTree, defaultPreload: "intent" });

declare module "@tanstack/react-router" {
  interface Register {
    router: typeof router;
  }
}
