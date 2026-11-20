// @vitest-environment jsdom
// Remote server screens (讨论串 #38): the add / edit dialog (write-only credential), the test
// connection result with host key reset, and project grants with disabled lead / reviewer roles.
import { QueryClient, VueQueryPlugin } from "@tanstack/vue-query";
import { flushPromises, mount } from "@vue/test-utils";
import ElementPlus from "element-plus";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Component } from "vue";
import { createMemoryHistory, createRouter } from "vue-router";
import type { ProjectServersView, ServerView } from "@/api/views";
import ServerTestPanel from "@/components/app/servers/ServerTestPanel.vue";
import ServersSection from "@/pages/projectSettings/ServersSection.vue";
import ServerDialog from "@/pages/settings/servers/ServerDialog.vue";
import Servers from "@/pages/settings/Servers.vue";

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
const calls = (spy: ReturnType<typeof api>, method: string, re: RegExp) =>
  spy.mock.calls.map((c) => c[0] as Request).filter((r) => r.method === method && re.test(new URL(r.url).pathname));
const bodyOf = async (r: Request) => JSON.parse(await r.clone().text());

function mountWith(c: Component, props: Record<string, unknown> = {}) {
  const router = createRouter({ history: createMemoryHistory(), routes: [{ path: "/:p(.*)*", component: { render: () => null } }] });
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return mount(c, { props, global: { plugins: [[VueQueryPlugin, { queryClient }], router, ElementPlus] as never[] }, attachTo: document.body });
}

beforeEach(() => {
  document.cookie = "agent_teams_csrf=tok";
});
afterEach(() => {
  vi.restoreAllMocks();
  document.body.innerHTML = "";
});

const prod: ServerView = {
  id: "srv_1", name: "prod", host: "203.0.113.10", port: 22, username: "deploy", auth_kind: "password", has_passphrase: false, key_fingerprint: "",
  host_key_fp: "SHA256:pinned", host_key_confirmed_at: "2026-11-01T00:00:00Z", cred_updated_at: "2026-11-01T00:00:00Z", created_at: "2026-11-01T00:00:00Z",
  updated_at: "2026-11-01T00:00:00Z", projects: [{ id: "p1", name: "agent team" }],
};

function input(label: string): HTMLInputElement | HTMLTextAreaElement {
  const l = [...document.body.querySelectorAll("label")].find((x) => x.textContent?.trim() === label && x.htmlFor && document.getElementById(x.htmlFor));
  return document.getElementById(l!.htmlFor) as HTMLInputElement | HTMLTextAreaElement;
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
  const el = [...document.body.querySelectorAll<HTMLElement>(".el-radio-button")].find((b) => b.textContent?.trim() === text)!;
  el.querySelector("input")!.click();
  await flushPromises();
};

describe("ServerDialog", () => {
  it("adds a password server and a private key server with a passphrase", async () => {
    const spy = api([["POST", /\/api\/servers$/, prod]]);
    mountWith(ServerDialog, { open: true, server: null });
    await flushPromises();
    await type("名称", "prod");
    await type("主机", "203.0.113.10");
    await type("用户", "deploy");
    await submit();
    expect(calls(spy, "POST", /\/api\/servers$/)).toHaveLength(0);
    expect(document.body.textContent).toContain("请填写密码");
    await type("密码", "s3cret");
    await submit();
    expect(await bodyOf(calls(spy, "POST", /\/api\/servers$/)[0])).toEqual({ name: "prod", host: "203.0.113.10", port: 22, username: "deploy", auth: { kind: "password", password: "s3cret" } });

    document.body.innerHTML = "";
    mountWith(ServerDialog, { open: true, server: null });
    await flushPromises();
    await type("名称", "key-box");
    await type("主机", "box.example.com");
    await type("用户", "ops");
    await radio("SSH 私钥");
    expect(document.body.textContent).toContain("私钥口令（可选）");
    await type("私钥", "-----BEGIN OPENSSH PRIVATE KEY-----\nabc\n-----END OPENSSH PRIVATE KEY-----");
    await type("私钥口令（可选）", "pp");
    await submit();
    expect((await bodyOf(calls(spy, "POST", /\/api\/servers$/)[1])).auth).toEqual({ kind: "private_key", private_key: "-----BEGIN OPENSSH PRIVATE KEY-----\nabc\n-----END OPENSSH PRIVATE KEY-----", passphrase: "pp" });
  });

  it("keeps the saved credential when left blank, and asks for it after a host change", async () => {
    const spy = api([["PATCH", /\/api\/servers\/srv_1$/, prod]]);
    mountWith(ServerDialog, { open: true, server: prod });
    await flushPromises();
    expect(input("密码").getAttribute("placeholder")).toBe("已保存，留空不修改");
    expect(input("密码").value).toBe("");
    await type("名称", "prod2");
    await submit();
    expect(await bodyOf(calls(spy, "PATCH", /srv_1$/)[0])).toEqual({ name: "prod2" });

    document.body.innerHTML = "";
    mountWith(ServerDialog, { open: true, server: prod });
    await flushPromises();
    await type("主机", "198.51.100.1");
    expect(document.body.textContent).toContain("旧凭证随之作废");
    expect(input("密码").getAttribute("placeholder")).toBe("");
    await submit();
    expect(calls(spy, "PATCH", /srv_1$/)).toHaveLength(1);
    expect(document.body.textContent).toContain("要重新填写凭证");
    await type("密码", "new");
    await submit();
    expect(await bodyOf(calls(spy, "PATCH", /srv_1$/)[1])).toEqual({ host: "198.51.100.1", auth: { kind: "password", password: "new" } });
  });
});

describe("ServerTestPanel", () => {
  it("shows the pinned fingerprint with the command to check it", () => {
    const w = mountWith(ServerTestPanel, { result: { ok: true, host_key_pinned: true, host_key_fp: "SHA256:first", host_key_type: "ssh-ed25519", latency_ms: 88 } });
    expect(w.text()).toContain("连接成功 · 88 ms");
    expect(w.text()).toContain("SHA256:first");
    expect(w.text()).toContain("ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub");
    expect(w.text()).not.toContain("重置主机密钥");
  });
  it("offers 重置主机密钥 on a mismatch", async () => {
    const w = mountWith(ServerTestPanel, {
      result: { ok: false, error: "host_key_mismatch", message: "不一样", host_key_fp: "SHA256:seen", expected_host_key_fp: "SHA256:old", host_key_type: "ssh-ed25519", host_key_pinned: false, latency_ms: 5 },
    });
    expect(w.text()).toContain("SHA256:old");
    expect(w.text()).toContain("SHA256:seen");
    await w.findAll("button").find((b) => b.text().includes("重置主机密钥"))!.trigger("click");
    await flushPromises();
    [...document.body.querySelectorAll<HTMLButtonElement>(".el-dialog button")].find((b) => b.textContent?.trim() === "重置")!.click();
    await flushPromises();
    expect(w.emitted("reset")).toHaveLength(1);
  });
});

describe("Servers page", () => {
  it("lists servers and tests one", async () => {
    const spy = api([
      ["GET", /\/api\/servers$/, [prod]],
      ["POST", /\/api\/servers\/srv_1\/test$/, { ok: false, error: "auth_failed", message: "认证失败：密码或私钥不对", host_key_fp: "SHA256:pinned", host_key_type: "ssh-ed25519", host_key_pinned: false, latency_ms: 30 }],
    ]);
    const w = mountWith(Servers);
    await flushPromises();
    expect(w.text()).toContain("deploy@203.0.113.10");
    expect(w.text()).toContain("SHA256:pinned");
    expect(w.text()).toContain("agent team");
    expect(w.text()).not.toMatch(/password|s3cret/);
    await w.findAll("button").find((b) => b.text() === "测试连接")!.trigger("click");
    await flushPromises();
    expect(calls(spy, "POST", /test$/)).toHaveLength(1);
    expect(w.get("[data-test-result]").text()).toContain("认证失败");
  });
});

describe("ServersSection", () => {
  const view: ProjectServersView = {
    servers: [{ server: prod, roles: ["dev"] }],
    available: [prod, { ...prod, id: "srv_2", name: "staging", projects: [] }],
    roles: [
      { role: "coordinator", title: "统筹", tier: "coordinator", grantable: false, reason: "coordinator" },
      { role: "lead", title: "组长", tier: "lead", grantable: false, reason: "lead" },
      { role: "dev", title: "开发", tier: "member", grantable: true },
      { role: "qa", title: "测试", tier: "member", grantable: true },
      { role: "review", title: "审查", tier: "member", grantable: false, reason: "read_only" },
    ],
  };
  it("disables the coordinator, lead and read-only roles and saves ticks", async () => {
    const spy = api([["GET", /\/api\/projects\/p1\/servers$/, view], ["PUT", /\/api\/projects\/p1\/servers$/, view], ["GET", /server-audit$/, { items: [], next_cursor: null }], ["GET", /threads$/, []]]);
    const w = mountWith(ServersSection, { projectId: "p1" });
    await flushPromises();
    const box = (role: string) => w.get(`[data-project-server='prod'] [data-role='${role}'] input`);
    for (const r of ["coordinator", "lead", "review"]) expect(box(r).attributes("disabled")).toBeDefined();
    expect(box("dev").attributes("disabled")).toBeUndefined();
    expect((box("dev").element as HTMLInputElement).checked).toBe(true);
    expect(w.find("[data-project-server='staging'] [data-role]").exists()).toBe(false);
    await box("qa").setValue(true);
    await flushPromises();
    expect(await bodyOf(calls(spy, "PUT", /servers$/)[0])).toEqual({ servers: [{ server_id: "srv_1", roles: ["dev", "qa"] }] });
    await w.get("[data-project-server='staging'] .el-checkbox input").setValue(true);
    await flushPromises();
    expect(await bodyOf(calls(spy, "PUT", /servers$/)[1])).toEqual({ servers: [{ server_id: "srv_1", roles: ["dev"] }, { server_id: "srv_2", roles: [] }] });
  });
});
