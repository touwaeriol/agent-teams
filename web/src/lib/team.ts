// Pure helpers for the team form (no runtime imports; tested by `node --test tests/`).
import type { Employee } from "./api";

export type MemberType = "claude_code" | "codex";

/** Fields only one member type understands (config.py ClaudeCodeSpec / CodexSpec). */
const ONLY: Record<MemberType, string[]> = {
  claude_code: ["permission_mode", "allowed_tools", "disallowed_tools", "max_turns", "claude_command"],
  codex: ["sandbox", "codex_command"],
};

export function memberType(employee: Employee): MemberType {
  return employee.type === "codex" ? "codex" : "claude_code";
}

/** Settings that mean different things per type (CLI flags), cleared on a switch. */
const PER_TYPE = ["extra_args"];

/** The employee as the other type: drops settings the new type would reject or misread. */
export function switchMemberType(employee: Employee, type: MemberType): Employee {
  if (memberType(employee) === type) return { ...employee, type };
  const other: MemberType = type === "codex" ? "claude_code" : "codex";
  const drop = new Set([...ONLY[other], ...PER_TYPE]);
  const kept = Object.entries(employee).filter(([key]) => !drop.has(key));
  return { ...Object.fromEntries(kept), name: employee.name, type } as Employee;
}

/** Only Claude Code members can lead (the lead needs a persistent session). */
export function canLead(employee: Employee): boolean {
  return memberType(employee) === "claude_code";
}
