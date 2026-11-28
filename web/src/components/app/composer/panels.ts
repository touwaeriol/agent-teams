// What the /status and /context panels show, pure and unit-tested.
import { statusBubble } from "@/lib/bubble";
import { $t } from "@/i18n";
import type { components } from "@/api/schema";
import type { CoordinatorView, EmployeeBrief, OverviewView } from "@/api/views";
import { isWorking, stateLabel, workingHere } from "@/lib/live";
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

const row = (e: EmployeeBrief, where: string): BusyRow => ({ name: e.name, where, doing: statusBubble(e.status?.bubble) || stateLabel(e) });

/** /status: who is working now, open cards and reviews, nodes online. */
export function statusSummary(o: Pick<OverviewView, "threads" | "waiting" | "nodes">, coordinator?: CoordinatorView): StatusSummary {
  const busy: BusyRow[] = [];
  if (coordinator && isWorking(coordinator.employee)) busy.push(row(coordinator.employee, $t("项目")));
  for (const t of o.threads) {
    for (const m of t.members) if (workingHere(m)) busy.push(row(m, `#${t.no}`));
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
    [$t("项目指令"), c.instructions_chars ? $t("{instructions_chars} 字", { instructions_chars: c.instructions_chars }) : $t("没有")],
    ["MEMORY.md", c.memory_index_chars ? $t("{memory_index_chars} 字", { memory_index_chars: c.memory_index_chars }) : $t("没有")],
    [$t("其他记忆文件"), $t("{memory_files} 个（需要时按名字读取）", { memory_files: c.memory_files })],
    [$t("这个对话"), $t("{messages} 条消息，{message_chars} 字", { messages: c.messages, message_chars: c.message_chars })],
  ];
  const s = c.session;
  if (!s) return [...rows, [$t("会话"), $t("还没有打开过会话")]];
  const last = s.last_turn;
  const lastIn = last.input + last.cache_read + last.cache_write;
  return [
    ...rows,
    [$t("会话"), $t("{n} · {turns} 轮", { n: s.model || "—", turns: s.turns })],
    [$t("上一轮"), s.turns ? $t("输入 {lastIn}（缓存读 {cache_read}）· 输出 {output} tokens", { lastIn: formatTokens(lastIn), cache_read: formatTokens(last.cache_read), output: formatTokens(last.output) }) : $t("还没有完成的一轮")],
    [$t("本次会话合计"), $t("输入 {formatTokens} · 输出 {output} tokens", { formatTokens: formatTokens(s.total.input + s.total.cache_read + s.total.cache_write), output: formatTokens(s.total.output) })],
  ];
}
