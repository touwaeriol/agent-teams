// What the /status and /context panels show, pure and unit-tested.
import type { components } from "@/api/schema";
import type { CoordinatorView, EmployeeBrief, OverviewView } from "@/api/views";
import { isWorking, stateLabel } from "@/lib/live";
import { formatTokens } from "@/lib/projectSettings";

type ComposerContext = components["schemas"]["ComposerContext"];

export interface BusyRow {
  name: string;
  where: string;
  doing: string;
}

export interface StatusSummary {
  busy: BusyRow[];
  activeThreads: number;
  cards: number;
  reviews: number;
  nodesOnline: number;
  nodesTotal: number;
}

const ACTIVE = new Set(["running", "waiting", "queued"]);

const row = (e: EmployeeBrief, where: string): BusyRow => ({ name: e.name, where, doing: e.status?.bubble || stateLabel(e) });

/** /status: who is working now, open cards and reviews, nodes online. */
export function statusSummary(o: Pick<OverviewView, "threads" | "waiting" | "nodes">, coordinator?: CoordinatorView): StatusSummary {
  const busy: BusyRow[] = [];
  if (coordinator && isWorking(coordinator.employee)) busy.push(row(coordinator.employee, "项目"));
  for (const t of o.threads) {
    for (const m of t.members) if (isWorking(m)) busy.push(row(m, `#${t.no}`));
  }
  return {
    busy,
    activeThreads: o.threads.filter((t) => ACTIVE.has(t.status)).length,
    cards: o.waiting.questions.length,
    reviews: o.waiting.reviews.length,
    nodesOnline: o.nodes.filter((n) => n.online).length,
    nodesTotal: o.nodes.length,
  };
}

/** /context rows: label and value. */
export function contextRows(c: ComposerContext): Array<[string, string]> {
  const rows: Array<[string, string]> = [
    ["项目指令", c.instructions_chars ? `${c.instructions_chars} 字` : "没有"],
    ["MEMORY.md", c.memory_index_chars ? `${c.memory_index_chars} 字` : "没有"],
    ["其他记忆文件", `${c.memory_files} 个（需要时按名字读取）`],
    ["这个对话", `${c.messages} 条消息，${c.message_chars} 字`],
  ];
  const s = c.session;
  if (!s) return [...rows, ["会话", "还没有打开过会话"]];
  const last = s.last_turn;
  const lastIn = last.input + last.cache_read + last.cache_write;
  return [
    ...rows,
    ["会话", `${s.model || "—"} · ${s.turns} 轮`],
    ["上一轮", s.turns ? `输入 ${formatTokens(lastIn)}（缓存读 ${formatTokens(last.cache_read)}）· 输出 ${formatTokens(last.output)} tokens` : "还没有完成的一轮"],
    ["本次会话合计", `输入 ${formatTokens(s.total.input + s.total.cache_read + s.total.cache_write)} · 输出 ${formatTokens(s.total.output)} tokens`],
  ];
}
