// @vitest-environment jsdom
// Admin · 新建用户 (讨论串 #44): form rules, the request body, the one-time initial password
// and server refusals shown on the form; the users table shows display names.
import { QueryClient, VueQueryPlugin } from "@tanstack/vue-query";
import { flushPromises, mount } from "@vue/test-utils";
import ElementPlus from "element-plus";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Component } from "vue";
import { ApiError } from "@/api/client";
import type { AdminUserView } from "@/api/views";
import { createUserError, emptyNewUserForm, MIN_PASSWORD_LENGTH, newUserBody, newUserProblem, USER_COLUMNS } from "@/pages/admin/admin";
import CreateUserDialog from "@/pages/admin/CreateUserDialog.vue";
import UsersTable from "@/pages/admin/UsersTable.vue";

type Route = [method: string, path: RegExp, body: unknown, status?: number];

function api(routes: Route[]) {
  return vi.spyOn(globalThis, "fetch").mockImplementation(async (input) => {
    const req = input as Request;
    const path = new URL(req.url).pathname;
    const hit = routes.find(([m, re]) => m === req.method && re.test(path));
    const status = hit ? (hit[3] ?? 200) : 404;
    return new Response(status === 204 ? null : JSON.stringify(hit ? hit[2] : {}), { status, headers: { "Content-Type": "application/json" } });
  });
}
const posts = (spy: ReturnType<typeof api>) =>
  spy.mock.calls.map((c) => c[0] as Request).filter((r) => r.method === "POST" && new URL(r.url).pathname === "/api/admin/users");
const bodyOf = async (r: Request) => JSON.parse(await r.clone().text());

function mountWith(c: Component, props: Record<string, unknown> = {}) {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return mount(c, { props, global: { plugins: [[VueQueryPlugin, { queryClient }], ElementPlus] as never[] }, attachTo: document.body });
}

function input(label: string): HTMLInputElement {
  const l = [...document.body.querySelectorAll("label")].find((x) => x.textContent?.trim() === label && x.htmlFor && document.getElementById(x.htmlFor));
  return document.getElementById(l!.htmlFor) as HTMLInputElement;
}
async function type(label: string, value: string) {
  const el = input(label);
  el.value = value;
  el.dispatchEvent(new Event("input"));
  await flushPromises();
}
const submit = async () => {
  document.body.querySelector<HTMLFormElement>(".el-dialog form")!.dispatchEvent(new Event("submit"));
  await flushPromises();
};
const radio = async (text: string) => {
  [...document.body.querySelectorAll<HTMLElement>(".el-radio-button")].find((b) => b.textContent?.trim() === text)!.querySelector("input")!.click();
  await flushPromises();
};
const checkbox = () => document.body.querySelector<HTMLInputElement>(".el-checkbox input")!;
const dialogTitles = () => [...document.body.querySelectorAll(".el-dialog h2")].filter((h) => (h.closest(".el-overlay") as HTMLElement | null)?.style.display !== "none").map((h) => h.textContent);

beforeEach(() => {
  document.cookie = "agent_teams_csrf=tok";
});
afterEach(() => {
  vi.restoreAllMocks();
  document.body.innerHTML = "";
});

const created = (over: Partial<AdminUserView> = {}): AdminUserView => ({
  id: "u9", email: "new@example.com", display_name: "", role: "user", active: true, max_nodes: null, must_change_password: true,
  created_at: "2026-11-23T00:00:00Z", nodes: 0, running_tasks: 0, max_running_threads: null, last_login_at: null, locale: "", ...over,
});

describe("new user form (pure)", () => {
  it("defaults to a member with a generated password that must be changed", () => {
    const f = emptyNewUserForm();
    expect(f).toMatchObject({ role: "user", passwordMode: "generated", mustChangePassword: true });
    expect(newUserProblem({ ...f, email: "a@b.co" })).toBeNull();
    expect(newUserBody({ ...f, email: " A@b.co ", displayName: "  " })).toEqual({ email: "A@b.co", role: "user", must_change_password: true });
  });
  it("checks the email and a typed password's length like the server", () => {
    const f = { ...emptyNewUserForm(), email: "a@b.co" };
    expect(newUserProblem({ ...f, email: "" })?.field).toBe("email");
    expect(newUserProblem({ ...f, email: "not-an-email" })?.field).toBe("email");
    expect(newUserProblem({ ...f, email: "a@b" })?.field).toBe("email");
    expect(MIN_PASSWORD_LENGTH).toBe(8);
    expect(newUserProblem({ ...f, passwordMode: "manual", password: "1234567" })).toEqual({ field: "password", message: "密码至少 8 位" });
    expect(newUserProblem({ ...f, passwordMode: "manual", password: "12345678" })).toBeNull();
    // A password typed before switching back to generated is not sent.
    expect(newUserBody({ ...f, password: "12345678" }).password).toBeUndefined();
    expect(newUserBody({ ...f, passwordMode: "manual", password: "12345678", mustChangePassword: false, role: "admin", displayName: " 王 " }))
      .toEqual({ email: "a@b.co", role: "admin", must_change_password: false, password: "12345678", display_name: "王" });
  });
  it("maps server refusals to fields", () => {
    expect(createUserError(new ApiError(409, "x", "email_taken"))).toEqual({ field: "email", message: "这个邮箱已经有账号了" });
    expect(createUserError(new ApiError(422, "密码至少 8 位", "weak_password")).field).toBe("password");
    expect(createUserError(new ApiError(422, "bad", "validation", [{ path: "body.display_name", message: "too long" }])).field).toBe("display_name");
    expect(createUserError(new Error("boom"))).toEqual({ field: "", message: "boom" });
  });
});

describe("CreateUserDialog", () => {
  it("creates with a generated password and shows it once", async () => {
    const spy = api([["POST", /^\/api\/admin\/users$/, { user: created(), temp_password: "init-ab12-cd34" }, 201]]);
    const w = mountWith(CreateUserDialog, { open: true });
    await flushPromises();
    expect(checkbox().checked).toBe(true);
    await submit();
    expect(posts(spy)).toHaveLength(0);
    expect(document.body.querySelector("[data-error=email]")?.textContent).toBe("请填写邮箱");
    await type("邮箱", "bad");
    await submit();
    expect(document.body.querySelector("[data-error=email]")?.textContent).toBe("请输入有效的邮箱地址");
    await type("邮箱", "New@Example.com");
    await type("显示名", "新同事");
    await submit();
    const reqs = posts(spy);
    expect(reqs).toHaveLength(1);
    expect(await bodyOf(reqs[0]!)).toEqual({ email: "New@Example.com", display_name: "新同事", role: "user", must_change_password: true });
    expect(w.emitted("update:open")?.at(-1)).toEqual([false]);
    await w.setProps({ open: false });
    await flushPromises();
    expect(document.body.textContent).toContain("init-ab12-cd34");
    expect(document.body.textContent).toContain("只显示这一次");
    // Closing the info dialog drops the password.
    [...document.body.querySelectorAll<HTMLButtonElement>(".el-dialog button")].find((b) => b.textContent?.trim() === "完成")!.click();
    await flushPromises();
    await new Promise((r) => setTimeout(r, 400));
    expect(document.body.textContent).not.toContain("init-ab12-cd34");
  });

  it("sends a typed password and the admin role without showing a password afterwards", async () => {
    const spy = api([["POST", /^\/api\/admin\/users$/, { user: created({ role: "admin", must_change_password: false }) }, 201]]);
    const w = mountWith(CreateUserDialog, { open: true });
    await flushPromises();
    await type("邮箱", "boss@example.com");
    await radio("管理员");
    await radio("手动填写");
    await type("密码", "short");
    await submit();
    expect(posts(spy)).toHaveLength(0);
    expect(document.body.querySelector("[data-error=password]")?.textContent).toBe("密码至少 8 位");
    await type("密码", "chosen-pass-1");
    checkbox().click();
    await flushPromises();
    await submit();
    expect(await bodyOf(posts(spy)[0]!)).toEqual({ email: "boss@example.com", role: "admin", must_change_password: false, password: "chosen-pass-1" });
    await w.setProps({ open: false });
    await flushPromises();
    expect(dialogTitles()).not.toContain("初始密码");
    expect(document.body.textContent).not.toContain("chosen-pass-1");
  });

  it("shows a taken email on the form and keeps the dialog open", async () => {
    api([["POST", /^\/api\/admin\/users$/, { detail: "这个邮箱已经注册过了", code: "email_taken" }, 409]]);
    const w = mountWith(CreateUserDialog, { open: true });
    await flushPromises();
    await type("邮箱", "dev@example.com");
    await submit();
    expect(document.body.querySelector("[data-error=email]")?.textContent).toBe("这个邮箱已经有账号了");
    expect(w.emitted("update:open")).toBeUndefined();
    await type("邮箱", "dev2@example.com");
    expect(document.body.querySelector("[data-error=email]")).toBeNull();
  });
});

describe("UsersTable", () => {
  it("shows the display name column, — when empty, and no menu on your own row", async () => {
    expect(USER_COLUMNS).toContain("显示名");
    api([
      ["GET", /^\/api\/me$/, created({ id: "u1", email: "me@example.com", role: "admin" })],
      ["GET", /^\/api\/admin\/users$/, [created({ id: "u1", email: "me@example.com", role: "admin" }), created({ id: "u2", email: "w@example.com", display_name: "小王" })]],
    ]);
    mountWith(UsersTable);
    await flushPromises();
    const rows = [...document.body.querySelectorAll("tbody tr")].map((r) => [...r.querySelectorAll("td")].map((td) => td.textContent?.trim()));
    expect(rows[0]![1]).toBe("—");
    expect(rows[1]![1]).toBe("小王");
    expect(document.body.querySelector('[aria-label="me@example.com 的操作"]')).toBeNull();
    expect(document.body.querySelector('[aria-label="w@example.com 的操作"]')).not.toBeNull();
  });
});
