// Settings, team, nodes, GitHub, admin and project-settings helpers (ported from the React page
// tests plus the Vue-side helpers that replaced inline code).
import { describe, expect, it } from "vitest";
import type { RoleView } from "@/api/views";
import { pickedSkillFiles, toBase64 } from "@/components/app/library/upload";
import { addRepo, matchesRepo, moveRepo, normalizeRepo, removeRepo, repoOptions, reposPayload, sortRepos } from "@/components/app/repo/repo";
import { draftValid, emptyDraft, scheduleBody } from "@/components/app/schedules/schedule";
import { parseSetting } from "@/pages/admin/admin";
import { connectionKind, usedText } from "@/pages/settings/github/github";
import { nodeMeta, nodeStats, runtimeLine, settingsPatch, slotPct } from "@/pages/settings/nodes/nodes";
import { isDirty, nodePickerText, ordered, patchAt, removeAt, toggleId, versionText } from "@/pages/settings/team/team";
import { employeeRows, fromChoice, inlineCommit, leadTimeoutLabel, memoryFileName, memoryWriter, repoRefs, splitMemory, threadRows, toChoice } from "@/pages/projectSettings/logic";
import { newRole } from "@/lib/roles";

const repo = (full_name: string, used: { id: string; name: string }[] = [], extra: object = {}) =>
  ({ full_name, used_by_projects: used, pushed_at: "2026-01-01", private: false, default_branch: "main", description: "", html_url: "", ...extra }) as never;

describe("repositories", () => {
  it("normalizes, adds, moves, removes and builds the payload", () => {
    expect(normalizeRepo("https://github.com/a/b.git")).toBe("a/b");
    expect(normalizeRepo("git@github.com:a/b.git")).toBe("a/b");
    expect(reposPayload([{ git_connection_id: null, repo: "a/b" }, { git_connection_id: "g", repo: "https://github.com/a/c.git" }])).toEqual([{ repo: "a/c", git_connection_id: "g" }]);
    const one = addRepo([], { repo: "https://github.com/o/api.git", git_connection_id: "g" });
    expect(addRepo(one, { repo: "O/API", git_connection_id: "g" })).toBe(one);
    expect(addRepo(one, { repo: "o/web", git_connection_id: null })).toBe(one);
    const two = addRepo(one, { repo: "o/web", git_connection_id: "g" });
    expect(moveRepo(two, 1, -1).map((r) => r.repo)).toEqual(["o/web", "o/api"]);
    expect(moveRepo(two, 1, 1)).toBe(two);
    expect(removeRepo(two, 0).map((r) => r.repo)).toEqual(["o/web"]);
  });
  it("picker rows leave out listed repos and name other projects using one", () => {
    const items = [repo("o/api", [{ id: "p1", name: "本项目" }]), repo("o/web", [{ id: "p2", name: "官网" }, { id: "p3", name: "后台" }]), repo("o/sec", [], { private: true, description: "Secrets vault" })];
    const rows = repoOptions(items, [{ repo: "O/API", git_connection_id: "g" }], "p1");
    expect(rows.map((r) => [r.value, r.usedBy, r.private])).toEqual([
      ["o/web", "已被 官网、后台 使用", false],
      ["o/sec", "", true],
    ]);
    expect(repoOptions([items[0]], [], "p1")[0].usedBy).toBe("");
    expect(matchesRepo(rows[1], "VAULT")).toBe(true);
    expect(matchesRepo(rows[1], "web")).toBe(false);
    expect(matchesRepo(rows[1], "  ")).toBe(true);
  });
  it("sorts used repos first, then by last push", () => {
    const r = (n: string, used: boolean, pushed: string) => repo(n, used ? [{ id: "p", name: "p" }] : [], { pushed_at: pushed });
    expect(sortRepos([r("a", false, "2026-02-01"), r("b", true, "2025-01-01"), r("c", false, "2026-03-01")]).map((x: { full_name: string }) => x.full_name)).toEqual(["b", "c", "a"]);
  });
});

describe("skills and plugins upload", () => {
  it("leaves hidden files out and base64-encodes large zips in chunks", () => {
    expect(pickedSkillFiles([{ path: "pitch/SKILL.md", text: "x" }, { path: "pitch/.DS_Store", text: "" }, { path: "__MACOSX/a", text: "" }])).toEqual([{ path: "pitch/SKILL.md", content: "x" }]);
    expect(toBase64(new TextEncoder().encode("PK\u0003\u0004"))).toBe("UEsDBA==");
    expect(atob(toBase64(new Uint8Array(100_000))).length).toBe(100_000);
  });
});

describe("schedules", () => {
  it("validates the draft and builds the body", () => {
    const d = emptyDraft();
    expect(d.cron).toBe("0 9 * * *");
    expect(draftValid(d)).toBe(false);
    const ok = { ...d, name: " 日报 ", message: " 汇总 ", cron: " 0 9 * * 1-5 " };
    expect(draftValid(ok)).toBe(true);
    expect(draftValid({ ...ok, cron: "every day" })).toBe(false);
    expect(scheduleBody(ok, "Asia/Shanghai")).toEqual({ name: "日报", cron: "0 9 * * 1-5", message: "汇总", timezone: "Asia/Shanghai" });
  });
});

describe("team", () => {
  const base = newRole([], "#000");
  it("edits the draft and orders it for saving", () => {
    const list: RoleView[] = [{ ...base, role: "a" }, { ...base, role: "b" }];
    expect(patchAt(list, 1, { title: "B" })[1].title).toBe("B");
    expect(patchAt(list, 1, { title: "B" })[0]).toBe(list[0]);
    expect(removeAt(list, 0).map((r) => r.role)).toEqual(["b"]);
    expect(ordered([...list].reverse()).map((r) => [r.role, r.sort])).toEqual([["b", 0], ["a", 1]]);
    expect(isDirty(list, [...list])).toBe(false);
    expect(isDirty(patchAt(list, 0, { count: 2 }), list)).toBe(true);
  });
  it("summarizes the node picker and toggles ids", () => {
    const nodes = [{ id: "n1", name: "rc" }, { id: "n2", name: "ovh" }];
    expect(nodePickerText([], nodes)).toBe("任意节点");
    expect(nodePickerText(["n2", "n1"], nodes)).toBe("rc、ovh");
    expect(nodePickerText(["n1", "gone", "gone2"], nodes)).toBe("rc、2 个已删除");
    expect(toggleId(["a"], "b")).toEqual(["a", "b"]);
    expect(toggleId(["a", "b"], "a")).toEqual(["b"]);
  });
  it("labels plugin versions", () => {
    expect(versionText(0, 3, false)).toBe("最新 v3");
    expect(versionText(2, 3, false)).toBe("v2");
    expect(versionText(1, 3, true)).toBe("v1（已清理）");
  });
});

describe("nodes", () => {
  const node = (patch: object) => ({ online: true, active_sessions: 1, max_active_sessions: 4, capacity: {}, queued: 0, parked_sessions: 2, version: "0.3.1", isolation: "agent_user", since: null, last_seen_at: null, ...patch }) as never;
  it("runtime chips", () => {
    expect(runtimeLine("codex", undefined)).toEqual({ ver: "未安装", login: "安装后登录或设置 CODEX_API_KEY", ok: false });
    expect(runtimeLine("claude", { cli: "2.1", auth: "none" }).login).toBe("未登录");
    expect(runtimeLine("claude", { cli: "2.1", auth: "api_key" })).toEqual({ ver: "2.1", login: "已配置 API Key", ok: true });
  });
  it("meta line, slot bar, totals and the settings patch", () => {
    expect(nodeMeta(node({}))).toBe("v0.3.1 · 独立系统用户 · 从未连接");
    expect(nodeMeta(node({ version: "", isolation: "x", online: false, last_seen_at: new Date().toISOString() }))).toMatch(/^最后在线 /);
    expect(slotPct(node({ active_sessions: 3 }))).toBe("75%");
    expect(slotPct(node({ active_sessions: 9 }))).toBe("100%");
    expect(slotPct(node({ max_active_sessions: null }))).toBe("0%");
    expect(nodeStats([node({}), node({ online: false })]).map((s) => s.v)).toEqual(["2（在线 1）", "1 / 4", "0", "4"]);
    expect(settingsPatch("", "120", true, ["gpu"])).toEqual({ clear_max_active_sessions: true, park_after_s: 120, isolated: true, labels: ["gpu"] });
    expect(settingsPatch("3", "", false, [])).toEqual({ max_active_sessions: 3, clear_park_after_s: true, isolated: false, labels: [] });
  });
});

describe("GitHub and admin", () => {
  it("connection kind and repo usage", () => {
    expect(connectionKind({ kind: "app", installation_settings_url: "https://github.com/organizations/x/settings/installations/1" })).toBe("组织");
    expect(connectionKind({ kind: "app", installation_settings_url: "https://github.com/settings/installations/1" })).toBe("个人账号");
    expect(connectionKind({ kind: "token", installation_settings_url: null })).toBe("个人账号");
    expect(usedText({ used_by_projects: [{ id: "a", name: "A" }, { id: "b", name: "B" }] })).toBe("项目 A、B");
    expect(usedText({ used_by_projects: [] })).toBe("未使用");
  });
  it("number settings commit whole numbers that changed", () => {
    expect(parseSetting("5", 3)).toBe(5);
    expect(parseSetting("3", 3)).toBeNull();
    expect(parseSetting("-1", 3)).toBeNull();
    expect(parseSetting("2.5", 3)).toBeNull();
    expect(parseSetting("abc", 3)).toBeNull();
  });
});

describe("project settings", () => {
  it("memory files, lead timeouts, model choices", () => {
    const f = (name: string) => ({ name, description: "", size: 0, updated_by: "user", updated_at: "" });
    expect(splitMemory([f("MEMORY.md"), f("a.md")])).toEqual({ index: f("MEMORY.md"), files: [f("a.md")] });
    expect(splitMemory(null)).toEqual({ index: undefined, files: [] });
    expect(memoryWriter("user")).toBe("你");
    expect(memoryWriter("lead-1 · #3")).toBe("lead-1 · #3");
    expect(memoryFileName(" testing ")).toBe("testing.md");
    expect(memoryFileName("a.MD")).toBe("a.MD");
    expect(leadTimeoutLabel(120)).toBe("2 分钟");
    expect(leadTimeoutLabel(90)).toBe("90 秒");
    expect(fromChoice(toChoice(""))).toBe("");
    expect(toChoice("claude-opus-5-5")).toBe("claude-opus-5-5");
  });
  it("inline text commits, resets a required empty value, ignores no change", () => {
    expect(inlineCommit("a", "a", true)).toEqual({});
    expect(inlineCommit("  ", "a", true)).toEqual({ reset: true });
    expect(inlineCommit("", "a", false)).toEqual({ save: "" });
    expect(inlineCommit("b", "a", true)).toEqual({ save: "b" });
  });
  it("repo refs and usage rows", () => {
    expect(repoRefs({ repos: [{ repo: "o/a", git_connection_id: undefined } as never] })).toEqual([{ repo: "o/a", git_connection_id: null }]);
    const t = { input: 1, output: 2, cache_read: 3, cache_write: 4 };
    const u = { by_thread: [{ thread_no: null, title: "", cost_usd: 1, tokens: t }, { thread_no: 12, title: "置顶", cost_usd: 2, tokens: t }], by_employee: [{ thread_no: 12, employee: "dev-1", cost_usd: 1, tokens: t }, { thread_no: null, employee: "leader", cost_usd: 1, tokens: t }] } as never;
    expect(threadRows(u).map((r) => [r.key, r.name])).toEqual([["c", "项目统筹"], ["12", "#12 置顶"]]);
    expect(employeeRows(u).map((r) => [r.key, r.name])).toEqual([["12-dev-1", "dev-1 · #12"], ["c-leader", "leader"]]);
  });
});
