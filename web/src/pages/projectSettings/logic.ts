// 项目设置 dialog helpers (pure): memory sources, approval wording, lead timeouts, usage rows.
import type { MemoryView, ProjectView, ProjectUsage } from "@/api/views";
import type { RepoRef } from "@/components/app/repo/repo";

export const MODES = [
  { id: "worktree" as const, title: "worktree（推荐）", desc: "每个节点一份主仓库，讨论串和成员各开 worktree。" },
  { id: "clone" as const, title: "独立克隆", desc: "每个成员一份完整克隆，最稳但占磁盘。" },
];

const MEMORY_SOURCE: Record<string, string> = { user: "你填写", coordinator: "统筹整理", lead: "组长总结" };

export function memorySource(m: Pick<MemoryView, "source">): string {
  const thread = m.source.match(/^thread:(\d+)$/);
  if (thread) return `#${thread[1]} 总结`;
  return MEMORY_SOURCE[m.source] ?? m.source;
}

export const APPROVAL_ROUTING_DESC: Record<string, string> = {
  lead_first: "成员的权限请求先交给讨论串的组长：放行、拒绝，或拿不准时转给你。组长自己的请求总是问你。",
  user: "成员的每个权限请求都直接出卡片问你。",
};

export const LEAD_TIMEOUTS = [60, 120, 300, 600];

export function leadTimeoutLabel(seconds: number): string {
  return seconds % 60 === 0 ? `${seconds / 60} 分钟` : `${seconds} 秒`;
}

/** The select value for "follow the role" (an empty string cannot be an option value everywhere). */
export const ROLE = "__role";
export const fromChoice = (v: string): string => (v === ROLE ? "" : v);
export const toChoice = (v: string): string => v || ROLE;

export function repoRefs(project: Pick<ProjectView, "repos">): RepoRef[] {
  return (project.repos ?? []).map((r) => ({ repo: r.repo, git_connection_id: r.git_connection_id ?? null }));
}

/** Text-box commit rule: unchanged → nothing; a required field left empty → put the saved value back. */
export function inlineCommit(text: string, saved: string, required: boolean): { save?: string; reset?: boolean } {
  if (text === saved) return {};
  if (required && !text.trim()) return { reset: true };
  return { save: text };
}

type Tokens = { input: number; output: number; cache_read: number; cache_write: number };
export type UsageRow = { key: string; name: string; cost: number; tokens: Tokens };

/** Input tokens include cache reads and writes. */
export const inputTokens = (t: Tokens): number => t.input + t.cache_read + t.cache_write;

export function threadRows(u: Pick<ProjectUsage, "by_thread">): UsageRow[] {
  return (u.by_thread ?? []).map((t) => ({ key: String(t.thread_no ?? "c"), name: t.thread_no ? `#${t.thread_no} ${t.title}` : "项目统筹", cost: t.cost_usd, tokens: t.tokens }));
}

export function employeeRows(u: Pick<ProjectUsage, "by_employee">): UsageRow[] {
  return (u.by_employee ?? []).map((e) => ({ key: `${e.thread_no ?? "c"}-${e.employee}`, name: e.thread_no ? `${e.employee} · #${e.thread_no}` : e.employee, cost: e.cost_usd, tokens: e.tokens }));
}
