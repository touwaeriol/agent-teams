// v2 routes (docs/v2/api.md §4). Auth: the `me` query decides between
// the login page and the app (guard below + App.vue).
import type { QueryClient } from "@tanstack/vue-query";
import { createRouter, createWebHistory, type RouteLocationNormalized, type RouteLocationRaw, type RouteRecordRaw, type RouteRecordRedirectOption, type RouterHistory } from "vue-router";
import { client, unwrap } from "@/api/client";
import { keys } from "@/api/queries";
import type { UserView } from "@/api/views";

export const projectRoutes: RouteRecordRaw[] = [
  { path: "", redirect: "/projects" },
  { path: "/projects", component: () => import("@/pages/ProjectsIndex.vue") },
  { path: "/projects/new", component: () => import("@/pages/NewProject.vue") },
  { path: "/projects/:pid", component: () => import("@/pages/Main.vue") },
  { path: "/projects/:pid/overview", component: () => import("@/pages/Overview.vue") },
  { path: "/projects/:pid/threads/:no", component: () => import("@/pages/ThreadRoute.vue") },
  { path: "/projects/:pid/threads/:no/full", component: () => import("@/pages/ThreadPage.vue") },
  { path: "/projects/:pid/threads/:no/:tab", component: () => import("@/pages/ThreadRoute.vue") },
  { path: "/projects/:pid/deliverables", component: () => import("@/pages/Deliverables.vue") },
  { path: "/projects/:pid/settings", component: () => import("@/pages/Main.vue") },
  { path: "/projects/:pid/settings/:section", component: () => import("@/pages/Main.vue") },
];

export const settingsRoutes: RouteRecordRaw[] = [
  { path: "/settings", redirect: "/settings/team" },
  { path: "/settings/team", component: () => import("@/pages/settings/Team.vue") },
  { path: "/settings/nodes", component: () => import("@/pages/settings/Nodes.vue") },
  { path: "/settings/git", component: () => import("@/pages/settings/GitHub.vue") },
  { path: "/settings/appearance", component: () => import("@/pages/settings/Appearance.vue") },
];

/** The admin console (/admin/*). Admin-only: the guard shows non-admins the 404 page instead. */
export const adminRoutes: RouteRecordRaw[] = [
  // Not a plain `redirect`: those resolve before the guard, so a non-admin's 404 would show /admin/users.
  { path: "", component: () => import("@/pages/admin/Users.vue"), beforeEnter: (to) => ({ path: "/admin/users", query: to.query, hash: to.hash, replace: true }) },
  { path: "users", component: () => import("@/pages/admin/Users.vue") },
  { path: "platform", component: () => import("@/pages/admin/Platform.vue") },
  { path: "models", component: () => import("@/pages/admin/Models.vue") },
  { path: "library", component: () => import("@/pages/admin/Library.vue") },
  { path: "feedback", component: () => import("@/pages/admin/Feedback.vue") },
  { path: "github-app", component: () => import("@/pages/admin/GitHubApp.vue") },
  { path: "audit", component: () => import("@/pages/admin/Audit.vue") },
];

/** Addresses that moved to the admin console; the query is kept (GitHub's App callback uses it). */
const moved = (path: string): RouteRecordRedirectOption => (to) => ({ path, query: to.query, hash: to.hash });

export const routes: RouteRecordRaw[] = [
  { path: "/login", component: () => import("@/pages/Login.vue"), meta: { public: true } },
  { path: "/projects/:pid/office", component: () => import("@/pages/Office.vue") },
  { path: "/projects/:pid/deliverables/:id", component: () => import("@/pages/DesignViewer.vue") },
  { path: "/", component: () => import("@/components/shell/ProjectShellRoute.vue"), children: projectRoutes },
  { path: "/", component: () => import("@/components/shell/SettingsShellRoute.vue"), children: settingsRoutes },
  { path: "/admin", component: () => import("@/components/shell/AdminShellRoute.vue"), meta: { admin: true }, children: adminRoutes },
  { path: "/settings/admin", redirect: moved("/admin") },
  { path: "/setup/github", redirect: moved("/admin/github-app") },
  { path: "/:rest(.*)*", name: "not-found", component: () => import("@/pages/NotFound.vue") },
];

/** `from` of the login page, if it is a path on this site (not `//host` or `/\\host`, which browsers read as another site). */
export function safeFrom(from: unknown): string | null {
  if (typeof from !== "string" || !from.startsWith("/") || from.startsWith("//") || from.startsWith("/\\")) return null;
  // Browsers drop tabs and newlines from URLs, so "/\t/host" would become "//host".
  // eslint-disable-next-line no-control-regex
  if (/[\u0000-\u001f\u007f]/.test(from)) return null;
  if (from === "/login" || from.startsWith("/login?") || from.startsWith("/login#")) return null;
  return from;
}

const decodeSegment = (s: string) => {
  try {
    return decodeURIComponent(s);
  } catch {
    return s;
  }
};

type GuardTarget = Pick<RouteLocationNormalized, "path" | "meta" | "query"> & Partial<Pick<RouteLocationNormalized, "fullPath" | "hash">>;

/** Where a request for `to` goes given the signed-in user (null = signed out). Pure, tested. */
export function guard(to: GuardTarget, me: UserView | null): RouteLocationRaw | true {
  if (!me) return to.meta.public ? true : `/login?from=${encodeURIComponent(to.fullPath ?? to.path)}`;
  if (to.path === "/login") return safeFrom(to.query.from) ?? "/projects";
  // Non-admins get the plain 404 page at the same address: no admin shell, no admin requests.
  if (to.meta.admin && me.role !== "admin") return { name: "not-found", params: { rest: to.path.slice(1).split("/").map(decodeSegment) }, query: to.query, hash: to.hash, replace: true };
  return true;
}

async function currentUser(qc: QueryClient): Promise<UserView | null> {
  try {
    return (await qc.fetchQuery({ queryKey: keys.me, queryFn: () => unwrap(client.GET("/api/me")), staleTime: 60_000, retry: false })) ?? null;
  } catch {
    return null;
  }
}

export function makeRouter(qc: QueryClient, history: RouterHistory = createWebHistory()) {
  const router = createRouter({ history, routes });
  router.beforeEach(async (to) => guard(to, qc.getQueryData<UserView | null>(keys.me) ?? (await currentUser(qc))));
  return router;
}
