// 会话 (multi-repository sessions): the helpers behind the sidebar rows, the new-session form and 工作现场.
import { describe, expect, it } from "vitest";
import { contextTotal, homeOf, isSession, parseDefaults, repoDir, reposLabel, sessionMark, sessionReposPayload, sessionText, usableNode, withBranches, zhTokens, fixedParts } from "@/components/app/session/session";
import { projectGroups } from "@/pages/projects";

describe("sessions", () => {
  it("routes a session to its own page and a project to its board", () => {
    expect(isSession({ kind: "session" })).toBe(true);
    expect(isSession({ kind: "project" })).toBe(false);
    expect(homeOf({ id: "p1", kind: "session" })).toBe("/sessions/p1");
    expect(homeOf({ id: "p2", kind: "project" })).toBe("/projects/p2");
  });
  it("names each repository's folder like the node does", () => {
    expect(repoDir("org/web")).toBe("org__web");
    expect(repoDir("git.example.com:2222/acme/api")).toBe("git.example.com_2222__acme__api");
  });
  it("sends each repository with its branch, the default one as none", () => {
    expect(
      sessionReposPayload([
        { repo: "https://github.com/org/web.git", git_connection_id: "c1", branch: " develop " },
        { repo: "git.example.com/acme/api", git_connection_id: null, git_repo_id: "gr1", branch: "" },
        { repo: "", git_connection_id: "c1", branch: "x" },
      ]),
    ).toEqual([
      { repo: "org/web", git_connection_id: "c1", branch: "develop" },
      { repo: "git.example.com/acme/api", git_connection_id: null, git_repo_id: "gr1", branch: undefined },
    ]);
  });
  it("keeps the branches already picked when repositories are added", () => {
    const prev = [{ repo: "org/web", git_connection_id: "c1", branch: "v2" }];
    expect(withBranches([{ repo: "org/Web", git_connection_id: "c1" }, { repo: "org/api", git_connection_id: "c1" }], prev)).toEqual([
      { repo: "org/Web", git_connection_id: "c1", branch: "v2" },
      { repo: "org/api", git_connection_id: "c1", branch: "" },
    ]);
  });
  it("says 员工 and 会话 instead of the thread words", () => {
    expect(sessionText("组长正在阅读任务…")).toBe("员工正在阅读任务…");
    expect(sessionText("发送消息会重新打开讨论串")).toBe("发送消息会重新打开会话");
  });
});

describe("new session box and projects page", () => {
  it("labels the repositories chip", () => {
    expect(reposLabel([])).toBe("选择仓库");
    expect(reposLabel([{ repo: "o/web", git_connection_id: "c", branch: "develop" }])).toBe("o/web · develop");
    expect(reposLabel([{ repo: "o/web", git_connection_id: "c", branch: "" }, { repo: "o/api", git_connection_id: "c", branch: "" }])).toBe("o/web +1");
  });
  it("remembers the last choice, ignoring anything malformed", () => {
    expect(parseDefaults(null)).toBeNull();
    expect(parseDefaults("{bad")).toBeNull();
    expect(parseDefaults(JSON.stringify({ repos: [{ repo: "o/web", git_connection_id: "c" }, { nope: 1 }], node: "n1", role: 3 }))).toEqual({
      repos: [{ repo: "o/web", git_connection_id: "c", branch: "" }],
      node: "n1",
      role: "",
      agent: "",
    });
  });
  it("lists projects only on the projects page: pinned, then the others, archived last", () => {
    const p = (id: string, extra: object = {}) =>
      ({ id, name: id, description: "", repos: [], kind: "project", pinned: false, archived: false, updated_at: "2026-11-01T00:00:00Z", ...extra }) as never;
    const groups = projectGroups([p("old"), p("s", { kind: "session" }), p("pin", { pinned: true }), p("arch", { archived: true, updated_at: "2026-11-20T00:00:00Z" }), p("new", { updated_at: "2026-11-10T00:00:00Z" })]);
    expect(groups.map((g) => [g.title, g.items.map((x: { id: string }) => x.id)])).toEqual([
      ["已置顶", ["pin"]],
      ["其他项目", ["new", "old", "arch"]],
    ]);
    expect(projectGroups([p("web")], "zz")).toEqual([]);
  });
});

describe("会话信息 and the sidebar mark (#53)", () => {
  it("formats the context total against the runtime's window, hidden without data", () => {
    expect(zhTokens(8123)).toBe("8,123");
    expect(zhTokens(77_000)).toBe("7.7万");
    expect(zhTokens(1_000_000)).toBe("100万");
    expect(zhTokens(200_000)).toBe("20万");
    expect(contextTotal({ context_used: 77_000, context_max: 1_000_000 })).toMatchObject({ pct: 7.7, text: "已使用 8% · 7.7万 / 100万" });
    expect(contextTotal({ context_used: 1_500, context_max: 200_000 })?.text).toBe("已使用 <1% · 1,500 / 20万");
    expect(contextTotal({ context_used: 300_000, context_max: 200_000 })?.pct).toBe(100);
    // Antigravity (no use reported), no turn yet, or an older server: no block at all.
    expect(contextTotal({ context_used: null, context_max: null })).toBeNull();
    expect(contextTotal({ context_used: 10, context_max: 0 })).toBeNull();
    expect(contextTotal(null)).toBeNull();
    expect(contextTotal(undefined)).toBeNull();
  });

  it("marks sessions in the sidebar: 进行中, 已完成, 已解决; cards show as the count instead", () => {
    const s = (session_status: string, patch: object = {}) => ({ kind: "session", waiting: 0, archived: false, session_status, ...patch }) as never;
    expect(sessionMark(s("working"))).toBe("working");
    expect(sessionMark(s("finished"))).toBe("finished");
    expect(sessionMark(s("resolved"))).toBe("resolved");
    expect(sessionMark(s("queued"))).toBe("queued");
    expect(sessionMark(s("failed"))).toBe("failed");
    // Node offline, session crashed, no node can run it (server: attention): 等你处理.
    expect(sessionMark(s("attention"))).toBe("attention");
    expect(sessionMark(s("finished", { waiting: 1 }))).toBeNull();
    expect(sessionMark(s("resolved", { archived: true }))).toBeNull();
    expect(sessionMark(s("", {}))).toBeNull();
    expect(sessionMark({ kind: "project", waiting: 0, archived: false, session_status: "" } as never)).toBeNull();
  });
});

describe("new session node (#53 t25)", () => {
  const nodes = [{ id: "n1", online: true }, { id: "n2", online: false }];
  it("keeps a remembered node that is online, else falls back to 自动选择", () => {
    expect(usableNode("n1", nodes, "auto")).toBe("n1");
    expect(usableNode("n2", nodes, "auto")).toBe("auto");
    expect(usableNode("gone", nodes, "auto")).toBe("auto");
    expect(usableNode("auto", nodes, "auto")).toBe("auto");
  });
});

describe("agent pick and fixed picks (#53 t46)", () => {
  it("lists a role's agents (its runtimes, first first) with the model for each", async () => {
    const { runtimesOf, scopedModel } = await import("@/lib/models");
    const role = { tier: "member", runtime: "claude", runtimes: ["claude", "codex", "antigravity"], model: "claude-opus-5-5", models: { codex: "gpt-5.5-codex" } };
    expect(runtimesOf(role)).toEqual(["claude", "codex", "antigravity"]);
    expect(scopedModel(role, "codex")).toBe("gpt-5.5-codex");
    expect(scopedModel(role, "claude")).toBe("claude-opus-5-5");
    expect(runtimesOf({ tier: "member", runtime: "codex" })).toEqual(["codex"]);
  });
  it("remembers the agent with the other picks", () => {
    expect(parseDefaults(JSON.stringify({ repos: [], node: "n1", role: "dev", agent: "codex" }))).toEqual({ repos: [], node: "n1", role: "dev", agent: "codex" });
    expect(parseDefaults(JSON.stringify({ repos: [], node: "n1", role: "dev" }))?.agent).toBe("");
  });
  it("shows a started session's node, role and agent read-only", () => {
    const label = (id: string) => ({ claude: "Claude Code", codex: "Codex" })[id] ?? id;
    const lead = { name: "dev-1", title: "开发", role: "dev", runtime: "claude", node: { name: "rc-erio" } };
    expect(fixedParts(lead, "codex", label)).toEqual({ node: "rc-erio", role: "开发", agent: "Codex" });
    expect(fixedParts({ ...lead, node: null }, null, label)).toEqual({ node: "", role: "开发", agent: "Claude Code" });
    expect(fixedParts(undefined, "codex", label)).toEqual({ node: "", role: "", agent: "" });
  });
});
