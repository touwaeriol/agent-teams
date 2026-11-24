// Permission modes of a Codex session (session.set_mode, protocol.md §4.5): the set_mode values map
// to a sandbox × approval policy × approvals reviewer, sent with the next turn/start.
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
 * roots and network access from config.sandbox_workspace_write.
 */
export function sandboxPolicy(sandbox: CodexSessionOptions["sandbox"], config: CodexSessionOptions["config"]): Record<string, unknown> {
  switch (sandbox) {
    case "danger-full-access":
      return { type: "dangerFullAccess" };
    case "read-only":
      return { type: "readOnly", networkAccess: false };
    default: {
      const ws = ((config ?? {}) as { sandbox_workspace_write?: { writable_roots?: string[]; network_access?: boolean } }).sandbox_workspace_write ?? {};
      return { type: "workspaceWrite", writableRoots: ws.writable_roots ?? [], networkAccess: ws.network_access ?? true, excludeTmpdirEnvVar: false, excludeSlashTmp: false };
    }
  }
}
