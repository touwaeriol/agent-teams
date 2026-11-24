// Codex permission modes (#53): what session.set_mode sends with the next turn/start. The shapes are
// typed by app-server's own TurnStartParams / SandboxPolicy / AskForApproval (src/codex-protocol,
// generated from codex-cli; checked identical to 0.171.0), so a wrong field fails typecheck; these
// cases pin the values: auto = workspace-write + on-request + auto_review, 手动 = workspace-write +
// untrusted reviewed by the user, 跳过权限确认 = danger-full-access + never, and switching 手动 ↔
// 跳过权限确认 keeps the worktree's .git writable (what git commit needs under workspace-write).
import { describe, expect, it } from "vitest";
import type { TurnStartParams } from "../src/codex-protocol/v2/TurnStartParams.js";
import { askForApproval, codexMode, modeTurnParams, sandboxPolicy } from "../src/sessions/codex/mode.js";
import type { CodexSessionOptions } from "../src/protocol.js";

const ROOT = "/srv/agent";
const session = (patch: Partial<CodexSessionOptions> = {}): CodexSessionOptions => ({
  model: "gpt-5.5-codex",
  sandbox: "workspace-write",
  approval_policy: "untrusted",
  config: { sandbox_workspace_write: { network_access: true, writable_roots: ["projects/p1/repos/o__r/.git", "/abs/cache"] } },
  ...patch,
});
/** What the driver merges into turn/start for a set_mode value (driver.setMode). */
function turn(mode: string, s: CodexSessionOptions): Partial<TurnStartParams> {
  const m = codexMode(mode, s);
  Object.assign(s, m);
  return modeTurnParams(m, s.config, ROOT);
}

describe("codex permission modes → turn/start", () => {
  it("自动: workspace-write, on-request, reviewed by auto_review", () => {
    expect(turn("auto", session())).toEqual({
      approvalPolicy: "on-request",
      approvalsReviewer: "auto_review",
      sandboxPolicy: { type: "workspaceWrite", writableRoots: [`${ROOT}/projects/p1/repos/o__r/.git`, "/abs/cache"], networkAccess: true, excludeTmpdirEnvVar: false, excludeSlashTmp: false },
    });
  });

  it("手动: workspace-write, untrusted, the user reviews", () => {
    const t = turn("default", session({ sandbox: "danger-full-access", approval_policy: "never" }));
    expect(t).toMatchObject({ approvalPolicy: "untrusted", approvalsReviewer: "user", sandboxPolicy: { type: "workspaceWrite" } });
  });

  it("跳过权限确认: danger-full-access, never", () => {
    expect(turn("bypassPermissions", session())).toEqual({ approvalPolicy: "never", approvalsReviewer: "user", sandboxPolicy: { type: "dangerFullAccess" } });
  });

  it("手动 ↔ 跳过权限确认 keeps the .git root writable each time it is back on workspace-write", () => {
    const s = session();
    const seq = ["bypassPermissions", "default", "bypassPermissions", "default", "auto", "default"].map((m) => turn(m, s));
    for (const t of seq.filter((x) => x.sandboxPolicy?.type === "workspaceWrite")) {
      const roots = (t.sandboxPolicy as { writableRoots: string[] }).writableRoots;
      expect(roots).toContain(`${ROOT}/projects/p1/repos/o__r/.git`);
      expect(roots.every((r) => r.startsWith("/"))).toBe(true);
    }
    expect(seq.map((t) => t.sandboxPolicy?.type)).toEqual(["dangerFullAccess", "workspaceWrite", "dangerFullAccess", "workspaceWrite", "workspaceWrite", "workspaceWrite"]);
    expect(seq.map((t) => t.approvalsReviewer)).toEqual(["user", "user", "user", "user", "auto_review", "user"]);
  });

  it("a read-only session stays read-only whatever is picked", () => {
    for (const m of ["auto", "default", "bypassPermissions"]) {
      expect(turn(m, session({ sandbox: "read-only", approval_policy: "never" }))).toMatchObject({ approvalPolicy: "never", sandboxPolicy: { type: "readOnly", networkAccess: false } });
    }
  });

  it("sends the replacement of on-failure, which current Codex no longer takes", () => {
    expect(askForApproval("on-failure")).toBe("on-request");
    for (const p of ["untrusted", "on-request", "never"] as const) expect(askForApproval(p)).toBe(p);
    expect(sandboxPolicy("workspace-write", undefined, ROOT)).toEqual({ type: "workspaceWrite", writableRoots: [], networkAccess: true, excludeTmpdirEnvVar: false, excludeSlashTmp: false });
  });
});
