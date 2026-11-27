// @vitest-environment jsdom
// The admin console routes (/admin/*): who gets in, the 404 for everyone else, the moved
// addresses, and the two sidebars (no admin entry on the user side).
import { QueryClient, VueQueryPlugin } from "@tanstack/vue-query";
import { mount } from "@vue/test-utils";
import { describe, expect, it } from "vitest";
import { createMemoryHistory } from "vue-router";
import { keys } from "@/api/queries";
import AdminNav from "@/components/shell/AdminNav.vue";
import SettingsNav from "@/components/shell/SettingsNav.vue";
import { makeRouter } from "@/router";

const user = (role: "admin" | "member") => ({ id: "u", email: "ann@b.c", role, display_name: "Ann" }) as never;

async function visit(role: "admin" | "member", url: string) {
  const qc = new QueryClient();
  qc.setQueryData(keys.me, user(role));
  const router = makeRouter(qc, createMemoryHistory());
  await router.push(url);
  return { router, route: router.currentRoute.value };
}

const ADMIN_PAGES = ["/admin/users", "/admin/platform", "/admin/library", "/admin/feedback", "/admin/github-app", "/admin/audit"];

// The first visits import the real (lazy) page modules, which takes a few seconds cold.
describe("admin console routes", { timeout: 30_000 }, () => {
  it("opens every admin page for admins, inside the admin shell", async () => {
    for (const path of ADMIN_PAGES) {
      const { route } = await visit("admin", path);
      expect(route.path).toBe(path);
      expect(route.name).not.toBe("not-found");
      expect(route.matched[0].path).toBe("/admin");
    }
  });
  it("sends /admin to the users page, keeping the query", async () => {
    expect((await visit("admin", "/admin?x=1")).route.fullPath).toBe("/admin/users?x=1");
  });
  it("shows members the 404 page at the address they asked for", async () => {
    for (const url of [...ADMIN_PAGES, "/admin", "/admin/users?x=1", "/admin/nope"]) {
      const { route } = await visit("member", url);
      expect(route.name).toBe("not-found");
      expect(route.fullPath).toBe(url);
      expect(route.matched.map((r) => r.path)).toEqual(["/:rest(.*)*"]);
    }
  });
  it("has no models page any more (each user keeps their own list in 设置 → 团队)", async () => {
    for (const role of ["admin", "member"] as const) {
      const { route } = await visit(role, "/admin/models");
      expect([route.name, route.fullPath]).toEqual(["not-found", "/admin/models"]);
    }
  });
  it("404s unknown addresses for everyone", async () => {
    expect((await visit("admin", "/no/such/page")).route.name).toBe("not-found");
    expect((await visit("member", "/no/such/page")).route.fullPath).toBe("/no/such/page");
  });
  it("redirects the old addresses, keeping the query", async () => {
    expect((await visit("admin", "/settings/admin")).route.fullPath).toBe("/admin/users");
    expect((await visit("admin", "/settings/admin?a=1")).route.fullPath).toBe("/admin/users?a=1");
    expect((await visit("admin", "/setup/github?github_app=my-app")).route.fullPath).toBe("/admin/github-app?github_app=my-app");
    expect((await visit("admin", "/setup/github?error=denied")).route.fullPath).toBe("/admin/github-app?error=denied");
    const member = (await visit("member", "/setup/github?github_app=x")).route;
    expect([member.name, member.fullPath]).toEqual(["not-found", "/admin/github-app?github_app=x"]);
  });
  it("lets admins use the user side as usual", async () => {
    for (const path of ["/projects", "/settings/team", "/settings/git"]) expect((await visit("admin", path)).route.path).toBe(path);
  });
});

describe("sidebars", { timeout: 30_000 }, () => {
  const mountNav = async (c: typeof SettingsNav | typeof AdminNav, role: "admin" | "member") => {
    const { router } = await visit(role, "/settings/team");
    return mount(c, { props: { me: user(role) }, global: { plugins: [[VueQueryPlugin, { queryClient: new QueryClient() }], router] as never[] } });
  };
  it("has no admin entry on the user side, even for admins", async () => {
    for (const role of ["admin", "member"] as const) {
      const w = await mountNav(SettingsNav, role);
      const hrefs = w.findAll("a").map((a) => a.attributes("href"));
      expect(hrefs).toEqual(["/projects", "/settings/team", "/settings/nodes", "/settings/git", "/settings/servers", "/settings/storage", "/settings/memory", "/settings/appearance"]);
      expect(w.findAll("a").map((a) => a.text()).join(" ")).not.toContain("管理");
    }
  });
  it("lists the admin pages and the way back", async () => {
    const w = await mountNav(AdminNav, "admin");
    expect(w.text()).toContain("管理后台");
    const links = w.findAll("a");
    expect(links[0].text()).toBe("返回用户端");
    expect(links.map((a) => a.attributes("href"))).toEqual(["/projects", ...ADMIN_PAGES]);
    expect(w.text()).not.toContain("模型");
  });
});
