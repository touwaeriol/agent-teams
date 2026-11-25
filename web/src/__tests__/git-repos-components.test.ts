// @vitest-environment jsdom
// 设置 → Git 仓库 (docs/v2/git-repos.md): the page lists standalone repositories and credentials;
// 添加仓库 sends a new token credential with the repository, generates an SSH key pair (only the
// public key comes back) and tests a draft before saving.
import { QueryClient, VueQueryPlugin } from "@tanstack/vue-query";
import { flushPromises, mount } from "@vue/test-utils";
import ElementPlus from "element-plus";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Component } from "vue";
import { createMemoryHistory, createRouter } from "vue-router";
import type { GitCredentialView, GitRepoView } from "@/api/views";
import GitRepos from "@/pages/settings/GitRepos.vue";
import AddRepoDialog from "@/pages/settings/gitRepos/AddRepoDialog.vue";
import CredentialDialog from "@/pages/settings/gitRepos/CredentialDialog.vue";
import { CREDENTIAL_WARNING } from "@/lib/gitRepos";

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

const cred: GitCredentialView = {
  id: "gc_1", name: "GitLab 令牌", host: "gitlab.com", kind: "https_token", username: "oauth2", expires_at: null, generated: false, has_passphrase: false,
  key_fingerprint: "", public_key: "", repos: 1, created_at: "2026-11-01T00:00:00Z", cred_updated_at: "2026-11-01T00:00:00Z",
};
const repo: GitRepoView = {
  id: "gr_1", slug: "gitlab.com/acme/web", name: "", url: "https://gitlab.com/acme/web.git", transport: "https", host: "gitlab.com", credential_id: "gc_1",
  provider: "gitlab", web_url: "https://gitlab.com/acme/web", default_branch: "main", access: "write", host_key_fp: "", host_key_confirmed_at: null,
  last_checked_at: "2026-11-20T00:00:00Z", last_error: "", projects: [{ id: "p1", name: "agent team" }], created_at: "2026-11-01T00:00:00Z",
};

describe("Git 仓库 page", () => {
  it("lists repositories, credentials and GitHub connections", async () => {
    api([
      ["GET", /\/api\/git\/repos$/, [repo]],
      ["GET", /\/api\/git\/credentials$/, [cred]],
      ["GET", /\/api\/git\/connections$/, []],
      ["GET", /\/api\/git\/github-app$/, { configured: false }],
      ["GET", /\/api\/me$/, { id: "u", role: "user" }],
    ]);
    mountWith(GitRepos);
    await flushPromises();
    const text = document.body.textContent ?? "";
    expect(text).toContain("单独添加的仓库 · 1");
    expect(document.body.querySelector("[data-git-repo='gitlab.com/acme/web']")?.textContent).toContain("HTTPS 令牌 · GitLab 令牌");
    expect(text).toContain("agent team");
    expect(document.body.querySelector("[data-git-credential='GitLab 令牌']")?.textContent).toContain("1 个仓库在用");
    expect(text).toContain("还没有连接 GitHub");
  });
});

describe("AddRepoDialog", () => {
  it("adds a repository with a new token credential, tested first", async () => {
    const spy = api([
      ["POST", /\/api\/git\/repos\/test$/, { ok: true, access: "write", default_branch: "main", branches: 2, host_key_pinned: false, latency_ms: 5 }],
      ["POST", /\/api\/git\/repos$/, repo],
    ]);
    mountWith(AddRepoDialog, { open: true, repo: null, credentials: [] });
    await flushPromises();
    await type("仓库地址", "https://gitlab.example.com/acme/web.git");
    expect(document.body.textContent).toContain("HTTPS · gitlab.example.com · acme/web · 识别为 自建 Git");
    await submit();
    expect(calls(spy, "POST", /\/api\/git\/repos$/)).toHaveLength(0);
    expect(document.body.textContent).toContain("请填令牌");
    await type("用户名", "oauth2");
    await type("令牌", "glpat-secret");
    [...document.body.querySelectorAll<HTMLButtonElement>("button")].find((b) => b.textContent?.trim() === "测试连接")!.click();
    await flushPromises();
    expect(await bodyOf(calls(spy, "POST", /\/test$/)[0])).toEqual({ url: "https://gitlab.example.com/acme/web.git", credential: { kind: "https_token", username: "oauth2", token: "glpat-secret" } });
    expect(document.body.querySelector("[data-git-test]")?.textContent).toContain("连接成功 · 可读写");
    await submit();
    expect(await bodyOf(calls(spy, "POST", /\/api\/git\/repos$/)[0])).toEqual({ url: "https://gitlab.example.com/acme/web.git", credential: { kind: "https_token", username: "oauth2", token: "glpat-secret" } });
  });

  it("generates an SSH key pair and shows only its public key", async () => {
    const gen = { ...cred, id: "gc_9", kind: "ssh_key", host: "git.example.com:2222", username: "git", generated: true, public_key: "ssh-ed25519 AAAA agent-teams" };
    const spy = api([["POST", /\/api\/git\/credentials$/, gen], ["POST", /\/api\/git\/repos$/, repo]]);
    mountWith(AddRepoDialog, { open: true, repo: null, credentials: [] });
    await flushPromises();
    await type("仓库地址", "ssh://git@git.example.com:2222/infra/deploy.git");
    [...document.body.querySelectorAll<HTMLButtonElement>("button")].find((b) => b.textContent?.trim() === "生成密钥对")!.click();
    await flushPromises();
    expect(await bodyOf(calls(spy, "POST", /\/credentials$/)[0])).toEqual({ name: "deploy 部署密钥", host: "git.example.com:2222", kind: "ssh_key", username: "git", generate: true });
    expect(document.body.textContent).toContain("ssh-ed25519 AAAA agent-teams");
    await submit();
    expect(await bodyOf(calls(spy, "POST", /\/api\/git\/repos$/)[0])).toEqual({ url: "ssh://git@git.example.com:2222/infra/deploy.git", credential_id: "gc_9" });
  });
});

describe("standalone credential warning (#55 M7)", () => {
  it("warns in 添加仓库 while a token or SSH key is used, not for a public repository", async () => {
    api([]);
    const w = mountWith(AddRepoDialog, { open: true, repo: null, credentials: [] });
    await flushPromises();
    await type("仓库地址", "https://gitlab.example.com/acme/web.git");
    expect(document.body.querySelector("[data-cred-warning]")?.textContent?.trim()).toBe(CREDENTIAL_WARNING);
    [...document.body.querySelectorAll<HTMLElement>(".el-radio-button")].find((b) => b.textContent?.trim() === "公开仓库（只读）")!.querySelector("input")!.click();
    await flushPromises();
    expect(document.body.querySelector("[data-cred-warning]")).toBeNull();
    w.unmount();
  });

  it("warns when replacing a credential", async () => {
    api([]);
    mountWith(CredentialDialog, { open: true, credential: cred });
    await flushPromises();
    expect(document.body.querySelector("[data-cred-warning]")?.textContent?.trim()).toBe(CREDENTIAL_WARNING);
  });
});
