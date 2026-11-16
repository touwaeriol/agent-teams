// Nodes page helpers (pure): runtime chips, the card's meta line, the slot bar, totals and the
// settings patch.
import type { NodeView } from "@/api/views";
import { summarizeNodes, nodeSlots } from "@/components/shell/nodes";
import { timeAgo } from "@/lib/utils";

export interface RuntimeInfo {
  cli?: string;
  sdk?: string;
  auth?: string;
}

export const RUNTIMES: [string, string][] = [
  ["claude", "Claude Code"],
  ["codex", "Codex"],
];

/** One runtime chip: version and login state (protocol.md hello.runtimes). */
export function runtimeLine(key: string, info: RuntimeInfo | undefined): { ver: string; login: string; ok: boolean } {
  if (!info || !info.cli) return { ver: "未安装", login: key === "codex" ? "安装后登录或设置 CODEX_API_KEY" : "安装后登录", ok: false };
  if (!info.auth || info.auth === "none") return { ver: info.cli, login: "未登录", ok: false };
  return { ver: info.cli, login: info.auth === "api_key" ? "已配置 API Key" : "已登录", ok: true };
}

const ISOLATION: Record<string, string> = { agent_user: "独立系统用户", sandbox: "沙箱", none: "未隔离" };

/** v0.3.1 · 独立系统用户 · 连接于 5 分钟前 */
export function nodeMeta(n: Pick<NodeView, "version" | "isolation" | "online" | "since" | "last_seen_at">): string {
  const seen = n.online && n.since ? `连接于 ${timeAgo(n.since)}` : n.last_seen_at ? `最后在线 ${timeAgo(n.last_seen_at)}` : "从未连接";
  return [n.version ? `v${n.version}` : null, ISOLATION[n.isolation], seen].filter(Boolean).join(" · ");
}

export function slotPct(n: Pick<NodeView, "active_sessions" | "max_active_sessions" | "capacity">): string {
  const slots = nodeSlots(n);
  return slots ? `${Math.min(100, Math.round((n.active_sessions / slots) * 100))}%` : "0%";
}

export function nodeStats(list: NodeView[]): { k: string; v: string }[] {
  const s = summarizeNodes(list);
  return [
    { k: "节点", v: `${s.total}（在线 ${s.online}）` },
    { k: "运行中 / 总槽位", v: `${s.active} / ${s.slots}` },
    { k: "排队中", v: String(s.queued) },
    { k: "停放的会话", v: String(list.reduce((n, x) => n + x.parked_sessions, 0)) },
  ];
}

/** PATCH /api/nodes/{id}: an empty number clears the override. */
export function settingsPatch(max: string, park: string, isolated: boolean, labels: string[]) {
  return {
    ...(max ? { max_active_sessions: Number(max) } : { clear_max_active_sessions: true }),
    ...(park ? { park_after_s: Number(park) } : { clear_park_after_s: true }),
    isolated,
    labels,
  };
}
