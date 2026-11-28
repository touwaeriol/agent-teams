// 上下文 breakdown of a session (GET /api/conversations/{cid}/context/detail, Claude only): the
// window's categories in the panel's order, the coloured segments of the bar and the MCP tools
// grouped by server. Rows are classified on the SDK's id and kind, never on the English name.
import { $t } from "@/i18n";
import type { components } from "@/api/schema";
import { zhTokens } from "./session";

export type ContextDetail = components["schemas"]["ContextDetailView"];
export type ContextCategory = components["schemas"]["ContextCategoryView"];
export type ContextMcpTool = components["schemas"]["ContextMcpToolView"];

/** Chinese names by SDK category id; an id we do not know keeps the runtime's own name. */
export const CATEGORY_LABEL: Record<string, string> = {
  mcp_tools: $t("MCP 工具"),
  system_tools: $t("系统工具"),
  messages: $t("消息"),
  skills: "Skills",
  memory_files: $t("记忆文件"),
  system_prompt: $t("系统提示词"),
  mcp_instructions: $t("MCP 服务器指令"),
  custom_agents: $t("自定义子代理"),
  autocompact_buffer: $t("自动压缩缓冲区"),
  compact_buffer: $t("压缩缓冲区"),
  free_space: $t("可用空间"),
  mcp_tools_deferred: $t("延迟加载的 MCP 工具"),
  system_tools_deferred: $t("延迟加载的系统工具"),
};

/** Colours of the rows that fill the window (the bar's segments). Ink-like rows use theme variables (dark mode). */
export const CATEGORY_COLOR: Record<string, string> = {
  mcp_tools: "#C8622A",
  system_tools: "#2F6FDB",
  messages: "hsl(var(--foreground))",
  skills: "#2E7D4F",
  memory_files: "#7A4FD1",
  system_prompt: "#8A877F",
  mcp_instructions: "#E3A57A",
  custom_agents: "#2F8F9D",
};
const OTHER_COLOR = "hsl(var(--ring))";

/** The swatches of the rows outside what is used, in theme variables so they show in dark mode too. */
export const HATCH = "repeating-linear-gradient(135deg, hsl(var(--input)) 0 2px, hsl(var(--raised)) 2px 4px)";
export function swatchStyle(kind: ContextCategory["kind"], color: string): Record<string, string> {
  if (kind === "buffer") return { background: HATCH };
  if (kind === "free") return { background: "hsl(var(--raised))", border: "1px solid hsl(var(--input))" };
  if (kind === "deferred") return { border: "1px dashed hsl(var(--ring))" };
  return { background: color };
}

/**
 * A failed refresh in words the user reads (the node answers in English); null when there is no
 * error. The original text goes in the tooltip.
 */
export function refreshErrorText(error: string | undefined | null): string | null {
  if (!error) return null;
  const e = error.toLowerCase();
  if (e.includes("no live process") || e.includes("not running") || e.includes("parked")) return $t("会话现在没有在运行（已停放），发一条消息后再刷新");
  if (e.includes("no such session") || e.includes("unknown session")) return $t("节点上找不到这个会话（节点可能重启过），发一条消息后再刷新");
  if (e.includes("offline") || e.includes("not online") || e.includes("disconnected")) return $t("节点不在线，稍后再试");
  if (e.includes("timed out") || e.includes("timeout") || e.includes("did not report")) return $t("节点没能及时统计完，稍后再试");
  if (e.includes("no context breakdown") || e.includes("unsupported")) return $t("这个运行时不提供上下文细分");
  return $t("节点没能统计上下文，稍后再试");
}

/** The order rows are listed in (others follow in the runtime's order). */
const USED_ORDER = ["mcp_tools", "system_tools", "messages", "skills", "memory_files", "system_prompt", "mcp_instructions", "custom_agents"];
const REST_ORDER = ["autocompact_buffer", "compact_buffer", "free_space", "mcp_tools_deferred", "system_tools_deferred"];

export interface CategoryRow {
  key: string;
  id: string;
  label: string;
  tokens: number;
  /** "7.7万" */
  tokensText: string;
  /** "0.9%", or 不计入 for deferred rows (outside the window). */
  pctText: string;
  kind: ContextCategory["kind"];
  color: string;
  /** MCP tools shown when the row is expanded (mcp_tools: loaded ones, mcp_tools_deferred: deferred ones). */
  tools: ContextMcpTool[];
}

/** "0.96%": share of the window with `digits` decimals; "<0.01%" for a sliver, "—" without a window. */
export function pctText(tokens: number, max: number, digits = 1): string {
  if (max <= 0) return "—";
  const v = (tokens / max) * 100;
  const floor = 10 ** -digits;
  if (v > 0 && v < floor) return `<${floor.toFixed(digits)}%`;
  return `${v.toFixed(digits)}%`;
}

const rank = (order: string[], id: string) => {
  const i = order.indexOf(id);
  return i < 0 ? order.length : i;
};

function rowOf(c: ContextCategory, i: number, d: ContextDetail): CategoryRow {
  const id = c.id ?? "";
  const tools = d.mcp_tools ?? [];
  return {
    key: id || `row-${i}`,
    id,
    label: CATEGORY_LABEL[id] ?? c.name,
    tokens: c.tokens,
    tokensText: zhTokens(c.tokens),
    pctText: c.kind === "deferred" ? $t("不计入") : pctText(c.tokens, d.max_tokens),
    kind: c.kind,
    color: CATEGORY_COLOR[id] ?? OTHER_COLOR,
    tools: id === "mcp_tools" ? tools.filter((t) => t.loaded) : id === "mcp_tools_deferred" ? tools.filter((t) => !t.loaded) : [],
  };
}

/** The window's rows: what fills it (used), then below the divider the buffer, free space and deferred tools. */
export function categoryRows(d: ContextDetail): { used: CategoryRow[]; rest: CategoryRow[] } {
  const rows = (d.categories ?? []).map((c, i) => ({ row: rowOf(c, i, d), i }));
  const sort = (order: string[]) => (a: { row: CategoryRow; i: number }, b: { row: CategoryRow; i: number }) => rank(order, a.row.id) - rank(order, b.row.id) || a.i - b.i;
  const used = rows.filter((r) => r.row.kind === "used" && r.row.tokens > 0).sort(sort(USED_ORDER));
  const rest = rows.filter((r) => r.row.kind !== "used").sort(sort(REST_ORDER));
  return { used: used.map((r) => r.row), rest: rest.map((r) => r.row) };
}

export interface Segment {
  key: string;
  label: string;
  /** Share of the bar, "0.96%". */
  width: string;
  color: string;
  buffer?: boolean;
}

/** The coloured bar: one segment per used row, then the autocompact reserve (hatched). */
export function segments(d: ContextDetail): Segment[] {
  const max = d.max_tokens;
  if (max <= 0) return [];
  const { used, rest } = categoryRows(d);
  const w = (t: number) => `${Math.min(100, (t / max) * 100)}%`;
  const out: Segment[] = used.map((r) => ({ key: r.key, label: `${r.label} ${r.tokensText}`, width: w(r.tokens), color: r.color }));
  for (const r of rest) if (r.kind === "buffer" && r.tokens > 0) out.push({ key: r.key, label: r.label, width: w(r.tokens), color: "", buffer: true });
  return out;
}

export interface McpGroup {
  server: string;
  tokens: number;
  tools: { name: string; short: string; tokens: number; tokensText: string; pctText: string }[];
}

/**
 * MCP tools by server, heaviest first. A tool is shown without its mcp__<server>__ prefix (the
 * server heads the group); the full name stays for the tooltip.
 */
export function mcpGroups(tools: ContextMcpTool[], max: number): McpGroup[] {
  const groups = new Map<string, McpGroup>();
  for (const t of tools) {
    const server = t.server || "mcp";
    let g = groups.get(server);
    if (!g) groups.set(server, (g = { server, tokens: 0, tools: [] }));
    g.tokens += t.tokens;
    g.tools.push({ name: t.name, short: shortTool(t.name, server), tokens: t.tokens, tokensText: t.tokens.toLocaleString("en-US"), pctText: pctText(t.tokens, max, 2) });
  }
  const list = [...groups.values()];
  for (const g of list) g.tools.sort((a, b) => b.tokens - a.tokens || a.short.localeCompare(b.short));
  return list.sort((a, b) => b.tokens - a.tokens || a.server.localeCompare(b.server));
}

/** mcp__platform__ask_user → ask_user (the server's own part, however the runtime normalised it). */
export function shortTool(name: string, server: string): string {
  const prefix = `mcp__${server}__`;
  if (name.startsWith(prefix)) return name.slice(prefix.length);
  const parts = name.split("__");
  return parts.length >= 3 && parts[0] === "mcp" ? parts.slice(2).join("__") : name;
}

/** "更新于 3 分钟前 · 估算" (summary after a turn) / "· 精确" (a refresh); null before the first report. */
export function updatedText(d: Pick<ContextDetail, "updated_at" | "detail">, ago: (iso: string) => string): string | null {
  if (!d.updated_at) return null;
  const how = d.detail === "full" ? $t("精确") : $t("估算");
  return $t("更新于 {updated_at} · {how}", { updated_at: ago(d.updated_at), how });
}
