// 会话 (multi-repository sessions): the helpers behind the sidebar rows, the new-session form and 工作现场.
import { describe, expect, it } from "vitest";
import { homeOf, isSession, parseDefaults, repoDir, reposLabel, sessionReposPayload, sessionText, withBranches } from "@/components/app/session/session";
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
