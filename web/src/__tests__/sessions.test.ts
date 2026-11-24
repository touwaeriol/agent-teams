// 会话 (multi-repository sessions): the helpers behind the sidebar rows, the new-session form and 工作现场.
import { describe, expect, it } from "vitest";
import { homeOf, isSession, repoDir, sessionReposPayload, withBranches } from "@/components/app/session/session";

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
});
