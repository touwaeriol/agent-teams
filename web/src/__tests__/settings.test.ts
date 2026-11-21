// Settings, team, nodes, GitHub, admin and project-settings helpers (ported from the React page
// tests plus the Vue-side helpers that replaced inline code).
import { describe, expect, it, vi } from "vitest";
import type { RoleView } from "@/api/views";
import { pickedSkillFiles, toBase64 } from "@/components/app/library/upload";
import { addRepo, matchesRepo, moveRepo, normalizeRepo, removeRepo, repoOptions, reposPayload, sortRepos, sourceLabel, standaloneOptions } from "@/components/app/repo/repo";
import { draftValid, emptyDraft, scheduleBody } from "@/components/app/schedules/schedule";
import { parseSetting } from "@/pages/admin/admin";
import { connectionKind, usedText } from "@/pages/settings/github/github";
import { createRefreshCooldown, hoverIntent, needsUsageRefresh, nodeMeta, nodeStats, resetText, runtimeLine, settingsPatch, slotPct, updateView, usageLevel, usageView, windowLabel, withNodeUsage } from "@/pages/settings/nodes/nodes";
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

  it("mixes standalone Git repositories into a project's list", () => {
    const g = { id: "gr_1", slug: "github.com/x/y", name: "y", provider: "gitlab", url: "https://github.com/x/y.git", access: "read", projects: [{ id: "p2", name: "other" }] } as never;
    const one = addRepo([], { repo: "github.com/x/y", git_connection_id: null, git_repo_id: "gr_1" });
    expect(one).toEqual([{ repo: "github.com/x/y", git_connection_id: null, git_repo_id: "gr_1" }]); // the slug is kept as is
    expect(addRepo(one, { repo: "github.com/x/y", git_connection_id: null, git_repo_id: "gr_1" })).toBe(one);
    expect(reposPayload([...one, { repo: "o/a", git_connection_id: "g" }])).toEqual([
      { repo: "github.com/x/y", git_connection_id: null, git_repo_id: "gr_1" },
      { repo: "o/a", git_connection_id: "g" },
    ]);
    expect(sourceLabel(one[0], [], [g])).toBe("GitLab · 单独添加");
    expect(sourceLabel({ repo: "a", git_connection_id: null, git_repo_id: "gone" }, [], [g])).toBe("仓库已删除");
    expect(standaloneOptions([g], [], "p1")).toEqual([{ value: "github.com/x/y", usedBy: "已被 other 使用", private: false, keywords: "y https://github.com/x/y.git" }]);
    expect(standaloneOptions([g], one)).toEqual([]);
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
  it("quota window names, levels and reset text", () => {
    expect(windowLabel({ key: "five_hour" })).toBe("5 小时");
    expect(windowLabel({ key: "weekly" })).toBe("每周");
    expect(windowLabel({ key: "weekly_opus" })).toBe("每周 Opus");
    expect(windowLabel({ key: "weekly_sonnet" })).toBe("每周 Sonnet");
    expect(windowLabel({ key: "primary", window_minutes: 300 })).toBe("5 小时");
    expect(windowLabel({ key: "secondary", window_minutes: 10080 })).toBe("每周");
    expect(windowLabel({ key: "primary", window_minutes: 120 })).toBe("2 小时");
    expect(windowLabel({ key: "secondary", window_minutes: 2880 })).toBe("2 天");
    expect(windowLabel({ key: "primary" })).toBe("短窗口");
    expect(windowLabel({ key: "secondary" })).toBe("长窗口");
    expect(windowLabel({ key: "weekly_model", label: "Fable" })).toBe("每周 Fable");
    expect(windowLabel({ key: "weekly_model" })).toBe("每周（模型）");
    expect(windowLabel({ key: "weekly_model", label: " " })).toBe("每周（模型）");
    expect(windowLabel({ key: "five_hour", label: "会话" })).toBe("会话");
    expect(windowLabel({ key: "primary", window_minutes: 300, label: "Fast" })).toBe("Fast");
    expect([0, 79.9, 80, 94, 95, 100].map(usageLevel)).toEqual(["normal", "normal", "warn", "warn", "danger", "danger"]);
    const now = Date.parse("2026-11-19T10:00:00Z");
    const at = (min: number) => new Date(now + min * 60_000).toISOString();
    expect(resetText(null, now)).toBe("");
    expect(resetText("bad", now)).toBe("");
    expect(resetText(at(-5), now)).toBe("即将重置");
    expect(resetText(at(25), now)).toBe("25 分钟后重置");
    expect(resetText(at(130), now)).toBe("2 小时后重置");
    const far = new Date(now + 3 * 86400_000);
    expect(resetText(far.toISOString(), now)).toMatch(new RegExp(`^${far.getMonth() + 1}月${far.getDate()}日 \\d{2}:\\d{2} 重置$`));
  });
  it("what shows under each runtime chip", () => {
    const now = Date.parse("2026-11-19T10:00:00Z");
    const ok = { auth: "subscription", status: "ok", plan: "Max", fetched_at: new Date().toISOString(), received_at: "", windows: [
      { key: "five_hour", used_percent: 42.4, resets_at: new Date(now + 90 * 60_000).toISOString() },
      { key: "weekly", used_percent: 96, resets_at: null },
    ] } as const;
    const sub = { cli: "2.1", auth: "subscription" };
    const on = { online: true, usage_supported: true }, off = { online: false, usage_supported: true }, old = { online: true, usage_supported: false };
    const v = usageView("claude", sub, { ...ok, windows: [...ok.windows] }, on, now);
    expect(v).toMatchObject({ kind: "windows", updated: "Max · 更新于 刚刚" });
    if (v?.kind !== "windows") throw new Error("expected windows");
    expect(v.rows.map((r) => [r.label, r.pct, r.width, r.reset])).toEqual([["5 小时", 42, "42%", "1 小时后重置"], ["每周", 96, "96%", ""]]);
    expect(v.rows[1].tone).toBe("hsl(var(--destructive))");
    // codex signs in with ChatGPT
    expect(usageView("codex", { cli: "0.1", auth: "chatgpt" }, { ...ok, auth: "chatgpt", windows: [{ key: "primary", used_percent: 81, window_minutes: 300, resets_at: null }] }, on, now)).toMatchObject({ kind: "windows", rows: [{ label: "5 小时", tone: "hsl(var(--warn))" }] });
    expect(usageView("claude", sub, { ...ok, status: "error", error: "401", windows: [] }, on, now)).toEqual({ kind: "error", error: "401" });
    expect(usageView("claude", sub, undefined, on, now)).toEqual({ kind: "pending" });
    expect(usageView("claude", sub, undefined, off, now)).toBeNull();
    // old daemon without the usage feature: no endless "loading" hint, but a report it did send still shows
    expect(usageView("claude", sub, undefined, old, now)).toBeNull();
    expect(usageView("claude", sub, { ...ok, windows: [...ok.windows] }, old, now)).toMatchObject({ kind: "windows" });
    expect(usageView("claude", sub, undefined, { online: true }, now)).toEqual({ kind: "pending" });
    expect(usageView("claude", { cli: "2.1", auth: "api_key" }, undefined, on, now)).toBeNull();
    expect(usageView("claude", { cli: "2.1", auth: "api_key" }, { ...ok, auth: "api_key", status: "unsupported", windows: [] }, on, now)).toBeNull();
    expect(usageView("claude", { cli: "2.1", auth: "none" }, undefined, on, now)).toBeNull();
    expect(usageView("claude", undefined, { ...ok, windows: [...ok.windows] }, on, now)).toBeNull();
    expect(usageView("claude", sub, { ...ok, windows: null }, on, now)).toBeNull();
    // hello says logged out / API key now: leftover subscription quota is hidden
    expect(usageView("claude", { cli: "2.1", auth: "none" }, { ...ok, windows: [...ok.windows] }, on, now)).toBeNull();
    expect(usageView("claude", { cli: "2.1", auth: "api_key" }, { ...ok, windows: [...ok.windows] }, on, now)).toBeNull();
    expect(usageView("codex", { cli: "0.1", auth: "api_key" }, { ...ok, auth: "chatgpt", windows: [...ok.windows] }, on, now)).toBeNull();
  });
  it("hover refresh: only stale subscription quota on online nodes, after 300 ms, one node's usage replaced", () => {
    const now = Date.parse("2026-11-19T10:00:00Z");
    const at = (s: number) => new Date(now - s * 1000).toISOString();
    const u = (received: string, fetched = received) => ({ auth: "subscription", status: "ok", windows: [], fetched_at: fetched, received_at: received }) as never;
    const sub = { claude: { cli: "2.1", auth: "subscription" } };
    expect(needsUsageRefresh({ online: true, runtimes: sub, usage: { claude: u(at(30)) } }, now)).toBe(false);
    expect(needsUsageRefresh({ online: true, runtimes: sub, usage: { claude: u(at(59)) } }, now)).toBe(false);
    expect(needsUsageRefresh({ online: true, runtimes: sub, usage: { claude: u(at(60)) } }, now)).toBe(true);
    expect(needsUsageRefresh({ online: true, runtimes: sub, usage: { claude: u(at(600)) } }, now)).toBe(true);
    // received_at first, fetched_at when it is missing
    expect(needsUsageRefresh({ online: true, runtimes: sub, usage: { claude: u(at(10), at(600)) } }, now)).toBe(false);
    expect(needsUsageRefresh({ online: true, runtimes: sub, usage: { claude: u("", at(10)) } }, now)).toBe(false);
    expect(needsUsageRefresh({ online: true, runtimes: sub, usage: { claude: u("", at(90)) } }, now)).toBe(true);
    expect(needsUsageRefresh({ online: true, runtimes: sub, usage: null }, now)).toBe(true);
    expect(needsUsageRefresh({ online: false, runtimes: sub, usage: null }, now)).toBe(false);
    // old daemon: never asked (the server would answer 409 usage_unsupported)
    expect(needsUsageRefresh({ online: true, usage_supported: false, runtimes: sub, usage: null }, now)).toBe(false);
    expect(needsUsageRefresh({ online: true, usage_supported: false, runtimes: sub, usage: { claude: u(at(600)) } }, now)).toBe(false);
    expect(needsUsageRefresh({ online: true, usage_supported: true, runtimes: sub, usage: null }, now)).toBe(true);
    expect(needsUsageRefresh({ online: true, runtimes: { claude: { cli: "2.1", auth: "api_key" }, codex: { cli: "0.1", auth: "none" } }, usage: null }, now)).toBe(false);
    expect(needsUsageRefresh({ online: true, runtimes: { claude: { cli: "2.1", auth: "subscription" }, codex: { cli: "0.1", auth: "chatgpt" } }, usage: { claude: u(at(5)) } }, now)).toBe(true);

    vi.useFakeTimers();
    try {
      const fire = vi.fn();
      const h = hoverIntent(300, fire);
      h.enter();
      vi.advanceTimersByTime(299);
      h.leave();
      vi.advanceTimersByTime(1000);
      expect(fire).not.toHaveBeenCalled();
      h.enter();
      h.enter();
      vi.advanceTimersByTime(300);
      expect(fire).toHaveBeenCalledTimes(1);
    } finally {
      vi.useRealTimers();
    }

    // cooldown: any attempt (ok, failed, 409) blocks that node for 60 s; other nodes are unaffected
    const cd = createRefreshCooldown();
    expect(cd.ready("a", now)).toBe(true);
    cd.mark("a", now);
    expect(cd.ready("a", now + 59_999)).toBe(false);
    expect(cd.ready("b", now + 1)).toBe(true);
    expect(cd.ready("a", now + 60_000)).toBe(true);
    cd.mark("a", now + 60_000);
    expect(cd.ready("a", now + 90_000)).toBe(false);
    const short = createRefreshCooldown(1000);
    short.mark("a", now);
    expect([short.ready("a", now + 999), short.ready("a", now + 1000)]).toEqual([false, true]);

    const list = [{ id: "a", usage: null }, { id: "b", usage: { claude: u(at(1)) } }];
    const fresh = { claude: u(at(0)) };
    expect(withNodeUsage(list, "a", fresh)).toEqual([{ id: "a", usage: fresh }, list[1]]);
    expect(withNodeUsage(list, "a", null)).toBe(list);
    expect(withNodeUsage(undefined, "a", fresh)).toBeUndefined();
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

describe("node updates", () => {
  const base = { online: true, version: "2.0.0+aaaaaaaa", latest_version: "2.0.0+bbbbbbbb", update_available: true, self_update: true };
  const up = (phase: string, extra: Record<string, unknown> = {}) =>
    ({ target: "2.0.0+bbbbbbbb", phase, mode: "wait_idle", active_turns: 0, started_at: "", updated_at: "", ...extra }) as never;

  it("offers the update only to nodes that can update themselves", () => {
    expect(updateView(base)).toMatchObject({ offer: true, busy: false, line: "" });
    expect(updateView({ ...base, self_update: false })).toMatchObject({ offer: false, tone: "warn" });
    expect(updateView({ ...base, self_update: false }).line).toContain("重新运行一次安装命令");
    expect(updateView({ ...base, update_available: false })).toMatchObject({ offer: false, line: "" });
  });

  it("shows the progress with 立即 / 取消 while it drains", () => {
    expect(updateView({ ...base, upgrade: up("staging") })).toMatchObject({ busy: true, offer: false, canNow: false, canCancel: true });
    const draining = updateView({ ...base, upgrade: up("draining", { active_turns: 2 }) });
    expect(draining).toMatchObject({ busy: true, canNow: true, canCancel: true });
    expect(draining.line).toContain("等 2 个");
    expect(updateView({ ...base, upgrade: up("draining", { active_turns: 2, mode: "now" }) }).canNow).toBe(false);
    expect(updateView({ ...base, upgrade: up("drain_timeout", { active_turns: 1 }) }).tone).toBe("warn");
    expect(updateView({ ...base, upgrade: up("restarting") })).toMatchObject({ busy: true, canNow: false, canCancel: false });
  });

  it("reports the outcome", () => {
    expect(updateView({ ...base, version: "2.0.0+bbbbbbbb", update_available: false, upgrade: up("done") })).toMatchObject({ line: "已更新到 2.0.0+bbbbbbbb", tone: "ok" });
    const back = updateView({ ...base, upgrade: up("rolled_back", { error: "boom" }) });
    expect(back).toMatchObject({ offer: true, tone: "error" });
    expect(back.line).toContain("已自动回到原版本：boom");
    expect(updateView({ ...base, upgrade: up("failed", { error: "sha256 mismatch" }) }).line).toBe("更新失败：sha256 mismatch");
  });
});
