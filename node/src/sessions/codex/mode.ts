// Permission modes of a Codex session (session.set_mode, protocol.md §4.5): the set_mode values map
// to a sandbox × approval policy × approvals reviewer, sent with the next turn/start.
import { isAbsolute, join } from "node:path";
import type { AskForApproval } from "../../codex-protocol/v2/AskForApproval.js";
import type { SandboxPolicy } from "../../codex-protocol/v2/SandboxPolicy.js";
import type { TurnStartParams } from "../../codex-protocol/v2/TurnStartParams.js";
import type { CodexSessionOptions } from "../../protocol.js";

type ModeFields = Pick<CodexSessionOptions, "sandbox" | "approval_policy" | "approvals_reviewer">;

/**
 * The options a set_mode value stands for. auto: workspace-write + on-request, approvals reviewed
 * by Codex's auto_review subagent; default: workspace-write + untrusted, approvals go to the user;
 * bypassPermissions: danger-full-access + never. A read-only session stays read-only.
 */
export function codexMode(mode: string, current: CodexSessionOptions): ModeFields {
  if (current.sandbox === "read-only") return { sandbox: "read-only", approval_policy: current.approval_policy };
  switch (mode) {
    case "bypassPermissions":
      return { sandbox: "danger-full-access", approval_policy: "never", approvals_reviewer: "user" };
    case "auto":
      return { sandbox: "workspace-write", approval_policy: "on-request", approvals_reviewer: "auto_review" };
    default:
      return { sandbox: "workspace-write", approval_policy: "untrusted", approvals_reviewer: "user" };
  }
}

/**
 * turn/start's sandboxPolicy for a sandbox mode (thread/start takes the mode name `sandbox`,
 * turn/start only the full policy object). workspace-write keeps the session's extra writable
 * roots and network access from config.sandbox_workspace_write. The console names those roots
 * relative to the node root; turn/start takes absolute paths (AbsolutePathBuf), so a relative
 * one is joined onto root.
 */
export function sandboxPolicy(sandbox: CodexSessionOptions["sandbox"], config: CodexSessionOptions["config"], root: string): SandboxPolicy {
  switch (sandbox) {
    case "danger-full-access":
      return { type: "dangerFullAccess" };
    case "read-only":
      return { type: "readOnly", networkAccess: false };
    default: {
      const ws = ((config ?? {}) as { sandbox_workspace_write?: { writable_roots?: string[]; network_access?: boolean } }).sandbox_workspace_write ?? {};
      return { type: "workspaceWrite", writableRoots: (ws.writable_roots ?? []).map((p) => (isAbsolute(p) ? p : join(root, p))), networkAccess: ws.network_access ?? true, excludeTmpdirEnvVar: false, excludeSlashTmp: false };
    }
  }
}

/**
 * app-server's approvalPolicy for the session's approval_policy. The node protocol still lists
 * "on-failure" (the console never sends it), which current Codex no longer takes (AskForApproval):
 * it goes out as "on-request", Codex's replacement for it.
 */
export function askForApproval(p: CodexSessionOptions["approval_policy"]): AskForApproval {
  return p === "on-failure" ? "on-request" : p;
}

/** The turn/start fields a mode sets, typed by app-server's own TurnStartParams (codex-protocol, generated from the CLI). */
export type ModeTurnParams = Required<Pick<TurnStartParams, "approvalPolicy" | "sandboxPolicy" | "approvalsReviewer">>;

/** turn/start's approvalPolicy + sandboxPolicy + approvalsReviewer for a set_mode value (session.set_mode). */
export function modeTurnParams(m: ModeFields, config: CodexSessionOptions["config"], root: string): ModeTurnParams {
  return { approvalPolicy: askForApproval(m.approval_policy), sandboxPolicy: sandboxPolicy(m.sandbox, config, root), approvalsReviewer: m.approvals_reviewer ?? "user" };
}
