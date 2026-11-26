// Nodes page helpers (pure): runtime chips, the card's meta line, the slot bar, totals and the
// settings patch.
import type { NodeRuntime, NodeUsage, NodeView, RuntimeUsage, UsageWindow } from "@/api/views";
import { formatDateTime } from "@/lib/library";
import { summarizeNodes, nodeSlots } from "@/components/shell/nodes";
import { RUNTIME_SPECS, runtimeSpec } from "@/lib/runtimes";
import { timeAgo } from "@/lib/utils";

export interface RuntimeInfo {
  cli?: string;
  sdk?: string;
  auth?: string;
  /** false: the daemon looked and the runtime is not there (Antigravity); absent counts as installed when cli is set. */
  installed?: boolean;
}

const AUTH_TEXT: Record<string, string> = { api_key: "已配置 API Key", adc: "已配置 ADC 凭证" };

export const RUNTIMES: [string, string][] = RUNTIME_SPECS.map((r) => [r.id, r.label]);

/** One runtime chip: version and login state (protocol.md hello.runtimes). */
export function runtimeLine(key: string, info: RuntimeInfo | undefined): { ver: string; login: string; ok: boolean } {
  if (!info || !info.cli || info.installed === false) return { ver: "未安装", login: runtimeSpec(key).installHint, ok: false };
  if (!info.auth || info.auth === "none") return { ver: info.cli, login: runtimeSpec(key).loginHint, ok: false };
  return { ver: info.cli, login: AUTH_TEXT[info.auth] ?? "已登录", ok: true };
}

export type NodeCli = NodeRuntime;

/** Runtimes the daemon may report before this console has a spec for them (#41). */
const EXTRA_LABELS: Record<string, string> = { gemini: "Gemini CLI", antigravity: "Antigravity" };

/** Display name of a runtime id: the spec's label, a known newcomer's, else the id itself. */
export function cliLabel(id: string): string {
  return RUNTIME_SPECS.find((r) => r.id === id)?.label ?? EXTRA_LABELS[id] ?? id;
}

const LOGIN_TEXT: Record<string, string> = { subscription: "已登录（订阅）", chatgpt: "已登录（ChatGPT）", api_key: "已配置 API Key", none: "未登录", unknown: "登录状态未知" };

/** Runtime-specific wording: Antigravity's subscription is a Google account login (the daemon maps agy's oauth to it). */
const RUNTIME_LOGIN_TEXT: Record<string, Record<string, string>> = { antigravity: { subscription: "已登录（Google 账号）", api_key: "已配置 API Key / ADC" } };

/** ok: sessions may run there; warn: something to do on the node; bad: was logged in, now expired; muted: unknown. */
export type CliTone = "ok" | "warn" | "bad" | "muted";

/** One runtime box on a node card. */
export interface CliRow {
  key: string;
  label: string;
  /** Version, "已安装" without one, or "未安装". */
  ver: string;
  login: string;
  tone: CliTone;
  /** "检测于 3 分钟前" ("" for an old daemon's hello data). */
  checked: string;
  /** Absolute time for the tooltip. */
  checkedTitle: string;
  /** Why the check failed, shown on hover. */
  error: string;
}

/** The login half of a reported CLI (data-model §2.2): not installed, expired, then by auth. */
export function cliLogin(c: Pick<NodeCli, "runtime" | "installed" | "auth" | "expired" | "ready">): { login: string; tone: CliTone } {
  if (!c.installed) return { login: RUNTIME_SPECS.find((r) => r.id === c.runtime)?.installHint ?? "安装后登录", tone: "warn" };
  if (c.expired) return { login: "登录已过期", tone: "bad" };
  const login = RUNTIME_LOGIN_TEXT[c.runtime]?.[c.auth] ?? LOGIN_TEXT[c.auth] ?? c.auth;
  if (c.auth === "unknown") return { login, tone: "muted" };
  if (c.auth === "none") return { login, tone: "warn" };
  // A login whose auth_expires_at has passed is not ready even when the daemon has not said expired yet.
  return c.ready ? { login, tone: "ok" } : { login: "登录已过期", tone: "bad" };
}

const HELLO_ALWAYS = new Set(["claude", "codex"]);

/**
 * The runtime boxes of a card: the CLIs the daemon reported (node.runtimes, in its order, any runtime
 * id) or, for a daemon that never reported (old version), the hello runtimes as before.
 */
export function cliRows(n: Pick<NodeView, "clis" | "runtimes"> & { runtime_status?: boolean; online?: boolean }, now = Date.now()): CliRow[] {
  const clis = n.clis ?? [];
  if (clis.length) {
    return clis.map((c) => {
      const t = Date.parse(c.checked_at);
      return {
        key: c.runtime,
        label: cliLabel(c.runtime),
        ver: c.installed ? c.version || "已安装" : "未安装",
        ...cliLogin(c),
        checked: Number.isNaN(t) ? "" : `检测于 ${timeAgo((Date.now() - Math.max(0, now - t)) / 1000)}`,
        checkedTitle: Number.isNaN(t) ? "" : formatDateTime(c.checked_at),
        error: c.error ?? "",
      };
    });
  }
  const hello = (n.runtimes ?? {}) as Record<string, RuntimeInfo>;
  // Every v2 daemon reports Claude Code and Codex in hello; a later runtime (Antigravity) only shows when the hello has it,
  // so a daemon too old to know it gets no "未安装" box for it.
  return RUNTIMES.filter(([key]) => HELLO_ALWAYS.has(key) || hello[key] !== undefined).map(([key, label]) => {
    const r = runtimeLine(key, hello[key]);
    // A daemon that reports but has not yet (just upgraded or connected): hello data until the first report.
    const checked = n.runtime_status && n.online ? "等待首次检测" : "";
    return { key, label, ver: r.ver, login: r.login, tone: r.ok ? "ok" : "warn", checked, checkedTitle: checked ? "守护进程连上后会立即检测并上报" : "", error: "" };
  });
}

/** What usageView reads per runtime: the reported CLIs when there are any (an expired login has no quota to show), else hello. */
export function runtimeInfos(n: Pick<NodeView, "clis" | "runtimes">): Record<string, RuntimeInfo> {
  const clis = n.clis ?? [];
  if (!clis.length) return (n.runtimes ?? {}) as Record<string, RuntimeInfo>;
  return Object.fromEntries(clis.map((c) => [c.runtime, c.installed ? { cli: c.version || "?", auth: c.ready ? c.auth : "expired" } : {}]));
}

/** The 重新检测 button: whether it can be pressed and, when not, why (tooltip). */
export function recheckState(n: Pick<NodeView, "online" | "runtime_status">): { enabled: boolean; hint: string } {
  if (!n.runtime_status) return { enabled: false, hint: "守护进程版本过旧，需要升级后才能重新检测" };
  if (!n.online) return { enabled: false, hint: "节点不在线" };
  return { enabled: true, hint: "立即检测各 CLI 的安装和登录状态，并刷新余量" };
}

/** Puts the node a recheck returned into the cached list. */
export function withNode<T extends { id: string }>(list: T[] | undefined, node: T): T[] | undefined {
  return list?.map((n) => (n.id === node.id ? node : n));
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
  // The plan moves to the account line when there is one (accountView); an older daemon keeps it here.
  const plan = hasAccount(usage) ? "" : usage.plan;
  return { kind: "windows", rows, updated: [plan, `更新于 ${timeAgo(usage.fetched_at || usage.received_at)}`].filter(Boolean).join(" · ") };
}

/** Claude's plan tier from `claude auth status`; max_other / other fall back to the subscription type. */
const CLAUDE_TIER: Record<string, string> = { pro: "Pro", max_5x: "Max 5x", max_20x: "Max 20x" };
const CLAUDE_PLAN: Record<string, string> = { pro: "Pro", max: "Max", team: "Team", enterprise: "Enterprise" };
/** Codex's planType (account/read). No usage multiples: they cannot be confirmed from the node's data. */
const CODEX_PLAN: Record<string, string> = {
  free: "Free", go: "Go", plus: "Plus", pro: "Pro", prolite: "Pro Lite", promax: "Pro Max", team: "Team", business: "Business", enterprise: "Enterprise", edu: "Edu",
};

/** The plan as shown on a card: a known tier or plan in its display form, any other reported value as is, "" when unknown. */
export function planLabel(key: string, u: Pick<RuntimeUsage, "plan" | "plan_tier"> | undefined): string {
  const plan = u?.plan?.trim() ?? "";
  const tier = u?.plan_tier?.trim() ?? "";
  if (key === "claude") return CLAUDE_TIER[tier] ?? (plan ? CLAUDE_PLAN[plan] ?? plan : "");
  if (key === "codex") return !plan || plan === "unknown" ? "" : CODEX_PLAN[plan] ?? plan;
  return plan;
}

/** A daemon that reports the login's account (email or tier); older ones never do. */
function hasAccount(u: RuntimeUsage): boolean {
  return Boolean(u.email || u.plan_tier);
}

/** The account line under a runtime: the login's email and plan. null when the daemon does not report them (the card stays as before). */
export type AccountView = { email: string; plan: string } | null;

export function accountView(key: string, info: RuntimeInfo | undefined, usage: RuntimeUsage | undefined): AccountView {
  const sub = SUBSCRIPTION[key];
  // Same login gate as the quota: logged out or switched to an API key hides a leftover account.
  if (!info?.cli || !sub || info.auth !== sub) return null;
  if (!usage || usage.auth !== sub || usage.status === "unsupported" || !hasAccount(usage)) return null;
  const email = usage.email?.trim() ?? "";
  const plan = planLabel(key, usage);
  return email || plan ? { email, plan } : null;
}

/** Quota younger than this is shown as is; hovering does not ask the node again. */
export const USAGE_FRESH_MS = 60_000;

/** Hovering a card asks for fresh quota only when the node is online, its daemon reports usage, it has a subscription login (per the reported CLIs when there are any) and that runtime's quota is missing or older than a minute. */
export function needsUsageRefresh(n: UsageNodeState & { runtimes?: unknown; clis?: NodeRuntime[] | null; usage?: NodeUsage | null }, now = Date.now(), maxAge = USAGE_FRESH_MS): boolean {
  if (!n.online || n.usage_supported === false) return false;
  // Same source as the card's quota (runtimeInfos): the reported CLIs, hello only for an old daemon.
  const runtimes = runtimeInfos(n as Pick<NodeView, "clis" | "runtimes">) as Record<string, RuntimeInfo | undefined>;
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
    { k: s.unlimited ? "运行中（有节点不限槽位）" : "运行中 / 总槽位", v: s.unlimited ? String(s.active) : `${s.active} / ${s.slots}` },
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

/** What the card shows about daemon updates (architecture.md §14.3). */
export interface UpdateView {
  /** Offer the 更新 button (a release the node is not on, and the node can update itself). */
  offer: boolean;
  /** An update is under way: the status line and its 立即 / 取消 buttons. */
  busy: boolean;
  /** The drain is waiting on running turns: offer 立即更新. */
  canNow: boolean;
  /** Before the switch: offer 取消. */
  canCancel: boolean;
  line: string;
  tone: "muted" | "ok" | "warn" | "error";
}

const BUSY = new Set(["requested", "staging", "draining", "drain_timeout", "restarting", "confirming"]);

export function updateView(n: Pick<NodeView, "online" | "version" | "latest_version" | "update_available" | "self_update" | "upgrade">): UpdateView {
  const u = n.upgrade;
  const none: UpdateView = { offer: false, busy: false, canNow: false, canCancel: false, line: "", tone: "muted" };
  if (u && BUSY.has(u.phase) && n.online !== false) {
    const turns = u.active_turns;
    const line: Record<string, string> = {
      requested: `准备更新到 ${u.target}…`,
      staging: `正在下载并安装 ${u.target}…`,
      draining: turns ? `等 ${turns} 个正在跑的回合结束后切换到 ${u.target}` : `正在切换到 ${u.target}…`,
      drain_timeout: `已等待较久，还有 ${turns} 个回合在跑；可以立即更新（会中断它们）`,
      restarting: `正在重启到 ${u.target}…`,
      confirming: `${u.target} 已启动，确认运行正常中…`,
    };
    const waiting = u.phase === "draining" || u.phase === "drain_timeout";
    return { offer: false, busy: true, canNow: waiting && u.mode !== "now", canCancel: u.phase === "staging" || waiting, line: line[u.phase] ?? "", tone: u.phase === "drain_timeout" ? "warn" : "muted" };
  }
  if (u?.phase === "done" && u.target === n.version) return { ...none, line: `已更新到 ${u.target}`, tone: "ok" };
  const failed = u && (u.phase === "failed" || u.phase === "rolled_back") && u.target === n.latest_version;
  const failLine = failed ? (u.phase === "rolled_back" ? `更新到 ${u.target} 失败，已自动回到原版本：${u.error ?? ""}` : `更新失败：${u.error ?? ""}`) : "";
  if (!n.update_available) return failed ? { ...none, line: failLine, tone: "error" } : none;
  if (!n.self_update) return { ...none, line: `有新版本 ${n.latest_version}；这台节点要先在机器上重新运行一次安装命令（容器节点按新的 compose.yml 重建）才能一键更新`, tone: "warn" };
  return { ...none, offer: true, line: failLine, tone: failed ? "error" : "muted" };
}

/** The node's shared-folder line (共享文件夹); null when its daemon cannot use the folder. */
export function sharedFolderLine(n: Pick<NodeView, "shared_folder" | "online">): { text: string; tone: "ok" | "warn" | "error" | "muted"; error?: string } | null {
  const f = n.shared_folder;
  if (!f) return null;
  const how = f.mode === "external" ? "（Docker 卷）" : "";
  if (!n.online) return { text: `共享文件夹${how}：离线`, tone: "muted" };
  const STATES: Record<string, { text: string; tone: "ok" | "warn" | "error" | "muted" }> = {
    mounted: { text: "已挂载", tone: "ok" },
    mounting: { text: "挂载中", tone: "warn" },
    unmounted: { text: "未挂载", tone: "muted" },
    error: { text: "挂载失败", tone: "error" },
  };
  const s = STATES[f.state] ?? { text: "未开通或未上报", tone: "muted" as const };
  return { text: `共享文件夹${how}：${s.text}`, tone: s.tone, ...(f.state === "error" && f.error ? { error: f.error } : {}) };
}
