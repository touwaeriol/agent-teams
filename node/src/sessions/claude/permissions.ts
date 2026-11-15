import type { PermissionResult, PermissionUpdate } from "@anthropic-ai/claude-agent-sdk";
import type { ApprovalResolve } from "../../protocol.js";

/** approval.resolve → SDK PermissionResult (protocol.md §5.2); never returns null. */
export function toPermissionResult(res: ApprovalResolve, input: Record<string, unknown>, suggestions?: PermissionUpdate[]): PermissionResult {
  if (res.decision === "deny") return { behavior: "deny", message: res.message || "The user denied this action." };
  const allow: PermissionResult = { behavior: "allow", updatedInput: res.updated_input ?? input };
  if (res.decision === "allow_always" && suggestions?.length) allow.updatedPermissions = sessionScoped(suggestions);
  return allow;
}

/** Every PermissionUpdate.destination becomes "session", so nothing is written into the worktree. */
export function sessionScoped(updates: PermissionUpdate[]): PermissionUpdate[] {
  return updates.map((u) => ({ ...u, destination: "session" }) as PermissionUpdate);
}
