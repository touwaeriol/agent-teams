// v2 routes (docs/v2/api.md §4), the same paths as web/src/App.tsx. Pages not ported yet use
// PendingPort with `meta.port` naming the React source. Auth: the `me` query decides between
// the login page and the app (guard below + App.vue).
import type { QueryClient } from "@tanstack/vue-query";
import { createRouter, createWebHistory, type RouteRecordRaw } from "vue-router";
import { client, unwrap } from "@/api/client";
import { keys } from "@/api/queries";
import type { UserView } from "@/api/views";

const pending = (port: string): Partial<RouteRecordRaw> => ({ component: () => import("@/pages/PendingPort.vue"), meta: { port } });

export const projectRoutes: RouteRecordRaw[] = [
  { path: "", redirect: "/projects" },
  { path: "/projects", component: () => import("@/pages/ProjectsIndex.vue") },
  { path: "/projects/new", ...pending("web/src/pages/NewProject.tsx") } as RouteRecordRaw,
  { path: "/projects/:pid", component: () => import("@/pages/Main.vue") },
  { path: "/projects/:pid/overview", ...pending("web/src/pages/Overview.tsx") } as RouteRecordRaw,
  { path: "/projects/:pid/threads/:no", component: () => import("@/pages/Main.vue"), props: (r) => ({ openNo: Number(r.params.no) || null }) },
  { path: "/projects/:pid/threads/:no/full", ...pending("web/src/pages/Thread.tsx") } as RouteRecordRaw,
  { path: "/projects/:pid/threads/:no/:tab", component: () => import("@/pages/Main.vue"), props: (r) => ({ openNo: Number(r.params.no) || null }) },
  { path: "/projects/:pid/deliverables", ...pending("web/src/pages/Deliverables.tsx") } as RouteRecordRaw,
  { path: "/projects/:pid/settings", component: () => import("@/pages/Main.vue") },
  { path: "/projects/:pid/settings/:section", component: () => import("@/pages/Main.vue") },
  { path: "/projects/:pid/legacy/:cid", ...pending("web/src/pages/Legacy.tsx") } as RouteRecordRaw,
];

export const settingsRoutes: RouteRecordRaw[] = [
  { path: "/settings", redirect: "/settings/team" },
  { path: "/settings/team", ...pending("web/src/pages/settings/Team.tsx") } as RouteRecordRaw,
  { path: "/settings/nodes", ...pending("web/src/pages/settings/Nodes.tsx") } as RouteRecordRaw,
  { path: "/settings/git", ...pending("web/src/pages/settings/GitHub.tsx") } as RouteRecordRaw,
  { path: "/settings/appearance", component: () => import("@/pages/settings/Appearance.vue") },
  { path: "/settings/admin", meta: { admin: true }, ...pending("web/src/pages/settings/Admin.tsx") } as RouteRecordRaw,
  { path: "/setup/github", meta: { admin: true }, ...pending("web/src/pages/settings/SetupGitHub.tsx") } as RouteRecordRaw,
];

export const routes: RouteRecordRaw[] = [
  { path: "/login", component: () => import("@/pages/Login.vue"), meta: { public: true } },
  { path: "/projects/:pid/office", ...pending("web/src/pages/Office.tsx") } as RouteRecordRaw,
  { path: "/projects/:pid/deliverables/:id", ...pending("web/src/pages/DesignViewer.tsx") } as RouteRecordRaw,
  { path: "/", component: () => import("@/components/shell/ProjectShellRoute.vue"), children: projectRoutes },
  { path: "/", component: () => import("@/components/shell/SettingsShellRoute.vue"), children: settingsRoutes },
  { path: "/:rest(.*)*", redirect: "/projects" },
];

/** Where a request for `to` goes given the signed-in user (null = signed out). Pure, tested. */
export function guard(to: { path: string; meta: Record<string, unknown>; query: Record<string, unknown> }, me: UserView | null): string | true {
  if (!me) return to.meta.public ? true : `/login?from=${encodeURIComponent(to.path)}`;
  if (to.path === "/login") {
    const from = typeof to.query.from === "string" ? to.query.from : "";
    return from && from !== "/login" ? from : "/projects";
  }
  if (to.meta.admin && me.role !== "admin") return "/projects";
  return true;
}

async function currentUser(qc: QueryClient): Promise<UserView | null> {
  try {
    return (await qc.fetchQuery({ queryKey: keys.me, queryFn: () => unwrap(client.GET("/api/me")), staleTime: 60_000, retry: false })) ?? null;
  } catch {
    return null;
  }
}

export function makeRouter(qc: QueryClient) {
  const router = createRouter({ history: createWebHistory(), routes });
  router.beforeEach(async (to) => guard(to, qc.getQueryData<UserView | null>(keys.me) ?? (await currentUser(qc))));
  return router;
}
