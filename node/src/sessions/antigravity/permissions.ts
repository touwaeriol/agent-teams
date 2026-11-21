// How the daemon answers agy's session/request_permission (protocol.md §5.2): platform tools are
// allowed, the node policy (secret paths, dangerous shell, read-only rules) may refuse, then the
// session's approval mode decides (auto: allow here; ask: an approval card).
import { decideTool, type ToolPolicy } from "../policy.js";
import { commandOf, isPlatformTool, pathOf, policyTool, READ_ONLY_KINDS, type AcpToolCall } from "./mapper.js";

export type PermissionAction = { action: "allow" } | { action: "deny"; reason: string } | { action: "ask" };

export interface PermissionOption {
  optionId: string;
  name?: string;
  kind: string; // allow_once | allow_always | reject_once | reject_always
}

/**
 * The local decision for one permission request. Read-only sessions (antigravity.read_only, as
 * ToolPolicy.readOnly) refuse every kind but read/search/think, and run a command only when
 * bash_policy allows it; nothing of a read-only session ever reaches the user as a card.
 */
export function decidePermission(policy: ToolPolicy, approval: "ask" | "auto", t: AcpToolCall, cwd: string, platformTools: readonly string[]): PermissionAction {
  if (isPlatformTool(t, platformTools)) return { action: "allow" };
  const kind = t.kind ?? "other";
  const tool = policyTool(kind);
  let input: Record<string, unknown> = {};
  if (kind === "execute") {
    const command = commandOf(t);
    if (command === null) return policy.readOnly ? { action: "deny", reason: "read-only session: command not recognised" } : approval === "auto" ? { action: "allow" } : { action: "ask" };
    input = { command };
  } else {
    const path = pathOf(t);
    if (path) input = { file_path: path, path };
  }
  if (policy.readOnly && kind !== "execute" && !READ_ONLY_KINDS.has(kind)) return { action: "deny", reason: `read-only session: ${kind} is not allowed` };
  const d = decideTool(policy, tool, input, cwd);
  if (d) return { action: "deny", reason: d.deny };
  if (policy.readOnly) return { action: "allow" };
  return approval === "auto" ? { action: "allow" } : { action: "ask" };
}

/** The ACP outcome that picks the first option of these kinds; cancelled when agy offered none. */
export function outcome(options: PermissionOption[], kinds: string[]): { outcome: { outcome: "selected"; optionId: string } | { outcome: "cancelled" } } {
  for (const k of kinds) {
    const o = options.find((x) => x.kind === k);
    if (o) return { outcome: { outcome: "selected", optionId: o.optionId } };
  }
  return { outcome: { outcome: "cancelled" } };
}

export const ALLOW_ONCE = ["allow_once", "allow_always"];
export const ALLOW_ALWAYS = ["allow_always", "allow_once"];
export const REJECT = ["reject_once", "reject_always"];
