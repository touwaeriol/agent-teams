// Nodes page helpers (pure): runtime chips, the card's meta line, the slot bar, totals and the
// settings patch.
import type { NodeUsage, NodeView, RuntimeUsage, UsageWindow } from "@/api/views";
import { summarizeNodes, nodeSlots } from "@/components/shell/nodes";
import { RUNTIME_SPECS, runtimeSpec } from "@/lib/runtimes";
import { timeAgo } from "@/lib/utils";

export interface RuntimeInfo {
  cli?: string;
  sdk?: string;
  auth?: string;
}

export const RUNTIMES: [string, string][] = RUNTIME_SPECS.map((r) => [r.id, r.label]);

/** One runtime chip: version and login state (protocol.md hello.runtimes). */
export function runtimeLine(key: string, info: RuntimeInfo | undefined): { ver: string; login: string; ok: boolean } {
  if (!info || !info.cli) return { ver: "未安装", login: runtimeSpec(key).installHint, ok: false };
  if (!info.auth || info.auth === "none") return { ver: info.cli, login: "未登录", ok: false };
  return { ver: info.cli, login: info.auth === "api_key" ? "已配置 API Key" : "已登录", ok: true };
}

/** Subscription logins that have a quota to show (claude.ai / ChatGPT accounts). */
const SUBSCRIPTION: Record<string, string> = Object.fromEntries(RUNTIME_SPECS.map((r) => [r.id, r.subscriptionAuth]));

const WINDOW_NAMES: Record<string, string> = { five_hour: "5 小时", weekly: "每周", weekly_opus: "每周 Opus", weekly_sonnet: "每周 Sonnet" };

/** Chinese window name: weekly_model is "每周 {label}", a server label wins elsewhere, codex's primary/secondary are named by their length when known. */
export function windowLabel(w: Pick<UsageWindow, "key" | "window_minutes" | "label">): string {
  const label = w.label?.trim();
  if (w.key === "weekly_model") return label ? `每周 ${label}` : "每周（模型）";
  if (label) return label;
  if (WINDOW_NAMES[w.key]) return WINDOW_NAMES[w.key];
  const m = w.window_minutes;
  if (m) {
    if (m === 300) return "5 小时";
    if (m === 10080) return "每周";
    if (m % 1440 === 0) return `${m / 1440} 天`;
    if (m % 60 === 0) return `${m / 60} 小时`;
    return `${m} 分钟`;
  }
  return w.key === "primary" ? "短窗口" : w.key === "secondary" ? "长窗口" : w.key;
}

export type UsageLevel = "normal" | "warn" | "danger";

/** ≥80% warns, ≥95% is about to run out. */
export function usageLevel(pct: number): UsageLevel {
  return pct >= 95 ? "danger" : pct >= 80 ? "warn" : "normal";
}

export const USAGE_TONE: Record<UsageLevel, string> = { normal: "hsl(var(--info))", warn: "hsl(var(--warn))", danger: "hsl(var(--destructive))" };

/** "3 小时后重置" within a day, a date and time beyond that, "" when unknown. */
export function resetText(resetsAt: string | null | undefined, now = Date.now()): string {
  if (!resetsAt) return "";
  const t = Date.parse(resetsAt);
  if (Number.isNaN(t)) return "";
  const min = Math.ceil((t - now) / 60_000);
  if (min <= 1) return "即将重置";
  if (min < 60) return `${min} 分钟后重置`;
  if (min < 24 * 60) return `${Math.floor(min / 60)} 小时后重置`;
  const d = new Date(t);
  const hm = d.toLocaleTimeString("zh-CN", { hour: "2-digit", minute: "2-digit", hour12: false });
  return `${d.getMonth() + 1}月${d.getDate()}日 ${hm} 重置`;
}

export type UsageRow = { key: string; label: string; pct: number; width: string; tone: string; reset: string };

/** What goes under a runtime chip: quota rows, a fetch error, a "loading" hint or nothing. */
export type UsageView = { kind: "windows"; rows: UsageRow[]; updated: string } | { kind: "error"; error: string } | { kind: "pending" } | null;

/** The node fields that decide whether missing quota is still coming: online and a daemon that reports usage (usage_supported; absent counts as supported). */
export type UsageNodeState = { online: boolean; usage_supported?: boolean };

export function usageView(key: string, info: RuntimeInfo | undefined, usage: RuntimeUsage | undefined, node: UsageNodeState, now = Date.now()): UsageView {
  const sub = SUBSCRIPTION[key];
  // hello's login state wins: logged out or switched to an API key hides any leftover quota.
  if (!info?.cli || !sub || info.auth !== sub) return null;
  // An old daemon never reports usage: no endless "loading" hint for it.
  if (!usage) return node.online && node.usage_supported !== false ? { kind: "pending" } : null;
  if (usage.auth !== sub || usage.status === "unsupported") return null;
  if (usage.status === "error") return { kind: "error", error: usage.error || "未知原因" };
  const rows = (usage.windows ?? []).map((w) => {
    const pct = Math.max(0, Math.min(100, Math.round(w.used_percent)));
    return { key: w.key, label: windowLabel(w), pct, width: `${pct}%`, tone: USAGE_TONE[usageLevel(pct)], reset: resetText(w.resets_at, now) };
  });
  if (!rows.length) return null;
  return { kind: "windows", rows, updated: [usage.plan, `更新于 ${timeAgo(usage.fetched_at || usage.received_at)}`].filter(Boolean).join(" · ") };
}

/** Quota younger than this is shown as is; hovering does not ask the node again. */
export const USAGE_FRESH_MS = 60_000;

/** Hovering a card asks for fresh quota only when the node is online, its daemon reports usage, it has a subscription login and that runtime's quota is missing or older than a minute. */
export function needsUsageRefresh(n: UsageNodeState & { runtimes?: unknown; usage?: NodeUsage | null }, now = Date.now(), maxAge = USAGE_FRESH_MS): boolean {
  if (!n.online || n.usage_supported === false) return false;
  const runtimes = (n.runtimes ?? {}) as Record<string, RuntimeInfo | undefined>;
  return Object.entries(SUBSCRIPTION).some(([key, sub]) => {
    const info = runtimes[key];
    if (!info?.cli || info.auth !== sub) return false;
    const u = n.usage?.[key as keyof NodeUsage];
    const at = Date.parse(u?.received_at || u?.fetched_at || "");
    return Number.isNaN(at) || now - at >= maxAge;
  });
}

/** Hover refresh cooldown: after any attempt (answered, failed or 409) a node is not asked again for a minute. */
export function createRefreshCooldown(ms = USAGE_FRESH_MS) {
  const last = new Map<string, number>();
  return {
    ready: (id: string, now = Date.now()) => {
      const t = last.get(id);
      return t === undefined || now - t >= ms;
    },
    mark: (id: string, now = Date.now()) => void last.set(id, now),
  };
}

/** Shared by every card so remounting the page does not reset it. */
export const refreshCooldown = createRefreshCooldown();

/** Fires `fire` once the pointer has stayed `ms` (hover intent); leaving earlier cancels it. */
export function hoverIntent(ms: number, fire: () => void): { enter: () => void; leave: () => void } {
  let timer: ReturnType<typeof setTimeout> | undefined;
  const leave = () => {
    if (timer !== undefined) clearTimeout(timer);
    timer = undefined;
  };
  return {
    enter: () => {
      leave();
      timer = setTimeout(() => {
        timer = undefined;
        fire();
      }, ms);
    },
    leave,
  };
}

/** Puts one node's refreshed quota into the cached node list (a null answer keeps the old data). */
export function withNodeUsage<T extends { id: string; usage?: NodeUsage | null }>(list: T[] | undefined, id: string, usage: NodeUsage | null | undefined): T[] | undefined {
  if (!list || !usage) return list;
  return list.map((n) => (n.id === id ? { ...n, usage } : n));
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
