// Overview board: the project's threads as cards with filters, and on the
// side what waits for you, project memory and old (v1) conversations.
import { useState } from "react";
import { Link } from "react-router-dom";
import { useLegacyConversations, useOverview, useRoles } from "@/api/queries";
import type { OverviewView, QuestionView, RoleView, ThreadView } from "@/api/views";
import { Bar, StatusPill } from "@/components/app/bits";
import { EmptyState, errorText, Loading } from "@/components/common";
import { Button } from "@/components/ui/button";
import { NavButton } from "@/components/shell/Shell";
import { leadOf, mainProgress, questionLine, teamText, threadKind, threadLook, type ThreadKind } from "@/lib/present";
import { cn, timeAgo } from "@/lib/utils";
import { useProjectId } from "./Main";

type Filter = "all" | "doing" | "wait" | "review" | "done";
const FILTERS: [Filter, string, ThreadKind[]][] = [
  ["all", "全部", []],
  ["doing", "进行中", ["doing", "queued", "paused"]],
  ["wait", "等你", ["wait"]],
  ["review", "待验收", ["review"]],
  ["done", "已完成", ["done", "failed"]],
];

export function filterThreads(threads: ThreadView[], f: Filter): ThreadView[] {
  const live = threads.filter((t) => t.status !== "archived");
  const kinds = FILTERS.find((x) => x[0] === f)![2];
  return kinds.length ? live.filter((t) => kinds.includes(threadKind(t))) : live;
}

/** “组长 + 2 开发 + 1 测试” from the role templates. */
export function teamSummary(roles: RoleView[]): string {
  const members = roles.filter((r) => r.tier === "member").map((r) => `${r.count} ${r.title || r.role}`);
  return ["组长", ...members].join(" + ");
}

export function OverviewPage() {
  const pid = useProjectId();
  const overview = useOverview(pid);
  const roles = useRoles();
  const [filter, setFilter] = useState<Filter>("all");
  if (overview.error) return <EmptyState title="打不开项目首页" description={errorText(overview.error)} />;
  if (!overview.data) return <Loading className="h-full" />;
  const o = overview.data;
  const p = o.project;
  return (
    <main className="px-4 pb-12 pt-4 sm:px-8 sm:pt-8">
      <div className="mx-auto flex max-w-[1100px] flex-col gap-6">
        <header className="flex flex-wrap items-end justify-between gap-4">
          <div className="flex items-start gap-1">
            <NavButton />
            <div className="flex flex-col gap-1.5">
              <h1 className="text-2xl font-semibold tracking-tight">{p.name}</h1>
              <span className="flex flex-wrap gap-2 text-[13px] text-muted-foreground">
                <span className="font-mono text-foreground">{p.repos?.length ? p.repos.map((r) => r.repo).join("、") : "未关联仓库"}</span>
                <span>工作方式 {p.work_mode === "worktree" ? "worktree" : "独立克隆"}</span>
                {roles.data?.roles && <span>团队模板：{teamSummary(roles.data.roles)}</span>}
              </span>
            </div>
          </div>
          <div className="flex gap-2">
            <Button asChild variant="outline" className="h-[38px]">
              <Link to={`/projects/${pid}/settings`}>项目设置</Link>
            </Button>
            <Button asChild variant="outline" className="h-[38px]">
              <Link to={`/projects/${pid}/office`}>办公室</Link>
            </Button>
            <Button asChild className="h-[38px]">
              <Link to={`/projects/${pid}`}>和项目统筹说话</Link>
            </Button>
          </div>
        </header>
        <div className="flex flex-wrap items-start gap-5">
          <ThreadCards overview={o} filter={filter} setFilter={setFilter} />
          <aside className="flex min-w-0 flex-[1_1_300px] flex-col gap-3.5">
            <WaitingBox pid={pid} overview={o} />
            <MemoryBox pid={pid} overview={o} />
            <LegacyBox pid={pid} />
          </aside>
        </div>
      </div>
    </main>
  );
}

function ThreadCards({ overview, filter, setFilter }: { overview: OverviewView; filter: Filter; setFilter: (f: Filter) => void }) {
  const pid = overview.project.id;
  const shown = filterThreads(overview.threads, filter);
  return (
    <section className="flex min-w-0 flex-[999_1_520px] flex-col gap-3">
      <div className="flex flex-wrap items-center justify-between gap-2.5">
        <h2 className="text-[15px] font-semibold">讨论串</h2>
        <div role="group" aria-label="按状态筛选" className="flex flex-wrap gap-1">
          {FILTERS.map(([id, label]) => (
            <button
              key={id}
              type="button"
              aria-pressed={filter === id}
              onClick={() => setFilter(id)}
              className={cn("h-[30px] rounded-full border px-2.5 text-xs", filter === id ? "border-foreground bg-foreground/5" : "border-input bg-white")}
            >
              {label} {filterThreads(overview.threads, id).length}
            </button>
          ))}
        </div>
      </div>
      {shown.length === 0 && <p className="rounded-xl border border-dashed p-6 text-center text-[13px] text-muted-foreground">这里还没有讨论串。</p>}
      {shown.map((t) => (
        <ThreadCard key={t.id} pid={pid} thread={t} />
      ))}
    </section>
  );
}

function prText(t: ThreadView): string {
  if (!t.pr_number) return "PR 未创建";
  return t.status === "done" ? `PR #${t.pr_number} 已合并` : `PR #${t.pr_number}`;
}

function ThreadCard({ pid, thread }: { pid: string; thread: ThreadView }) {
  const look = threadKind(thread) === "done" ? { tone: "#2E7D4F", label: "已完成" } : threadLook(thread);
  const progress = mainProgress(thread);
  return (
    <Link to={`/projects/${pid}/threads/${thread.no}`} className="flex flex-col gap-2.5 rounded-xl border bg-panel px-[18px] py-4 hover:border-input">
      <span className="flex items-center justify-between gap-3">
        <span className="flex min-w-0 items-center gap-2">
          <span className="font-mono text-xs text-muted-foreground">#{thread.no}</span>
          <span className="truncate font-semibold">{thread.title}</span>
        </span>
        <StatusPill tone={look.tone} label={look.label} spin={look.spin} />
      </span>
      <span className="text-[13px] leading-[21px] text-[#4A4843]">{thread.summary || thread.lead_last?.text || thread.brief}</span>
      <span className="flex items-center gap-2.5">
        <Bar pct={progress.pct} tone={look.tone} />
        <span className="whitespace-nowrap text-xs text-muted-foreground">主要任务 {progress.text}</span>
      </span>
      <span className="flex flex-wrap gap-x-4 gap-y-1.5 text-xs text-muted-foreground">
        <span>组长 {leadOf(thread)?.name ?? "—"}</span>
        <span>成员 {teamText(thread)}</span>
        {thread.branch && <span className="font-mono">{thread.branch}</span>}
        <span>{prText(thread)}</span>
        <span>{timeAgo(thread.updated_at)}</span>
      </span>
    </Link>
  );
}

function waitingFrom(q: QuestionView): string {
  const where = q.thread_no ? `#${q.thread_no}` : "统筹";
  return `${where} · ${q.kind === "approval" ? "权限请求" : q.asker.startsWith("lead") ? "组长提问" : `${q.asker} 提问`}`;
}

function WaitingBox({ pid, overview }: { pid: string; overview: OverviewView }) {
  const { questions, reviews } = overview.waiting;
  const total = questions.length + reviews.length;
  return (
    <section className="flex flex-col gap-2.5 rounded-xl border border-[#F0C9A8] bg-panel p-4">
      <h2 className="text-sm font-semibold">等你处理 · {total}</h2>
      {total === 0 && <span className="text-[13px] text-muted-foreground">现在没有要你处理的事。</span>}
      {questions.map((q) => (
        <Link key={q.id} to={q.thread_no ? `/projects/${pid}/threads/${q.thread_no}` : `/projects/${pid}`} className="flex flex-col gap-0.5 rounded-lg bg-[#FFF9F4] px-2.5 py-2">
          <span className="text-[13px]">{q.kind === "approval" ? questionLine(q).replace("请求执行", "想执行") : q.text}</span>
          <span className="text-xs text-muted-foreground">{waitingFrom(q)}</span>
        </Link>
      ))}
      {reviews.map((t) => (
        <Link key={t.id} to={`/projects/${pid}/threads/${t.no}`} className="flex flex-col gap-0.5 rounded-lg bg-[#FFF9F4] px-2.5 py-2">
          <span className="text-[13px]">{t.pr_number ? `PR #${t.pr_number} ${t.title}，等你验收` : `${t.title}，等你验收`}</span>
          <span className="text-xs text-muted-foreground">#{t.no} · 待验收</span>
        </Link>
      ))}
    </section>
  );
}

function MemoryBox({ pid, overview }: { pid: string; overview: OverviewView }) {
  return (
    <section className="flex flex-col gap-2 rounded-xl border bg-panel p-4">
      <h2 className="text-sm font-semibold">项目记忆</h2>
      <p className="text-[13px] leading-[22px] text-[#4A4843]">讨论串结束时组长写总结，统筹据此更新，开新讨论串时自动带上。</p>
      {overview.memory.length > 0 ? (
        <ul className="list-disc pl-[18px] text-[13px] leading-[22px]">
          {overview.memory.map((m) => (
            <li key={m.id}>{m.text}</li>
          ))}
        </ul>
      ) : (
        <span className="text-[13px] text-muted-foreground">还没有记忆。</span>
      )}
      <Link to={`/projects/${pid}/settings/memory`} className="text-[13px] underline-offset-2 hover:underline">
        查看和编辑
      </Link>
    </section>
  );
}

function LegacyBox({ pid }: { pid: string }) {
  const legacy = useLegacyConversations(pid);
  const list = legacy.data ?? [];
  if (list.length === 0) return null;
  return (
    <section className="flex flex-col gap-2 rounded-xl border bg-panel p-4">
      <h2 className="text-sm font-semibold">旧对话 · {list.length}</h2>
      <p className="text-xs text-muted-foreground">v1 里的对话，只读。</p>
      {list.map((c) => (
        <Link key={c.id} to={`/projects/${pid}/legacy/${c.id}`} className="flex justify-between gap-2 rounded-lg px-2 py-1.5 text-[13px] hover:bg-background">
          <span className="truncate">{c.title || "未命名对话"}</span>
          <span className="shrink-0 text-xs text-muted-foreground">{timeAgo(c.created_at)}</span>
        </Link>
      ))}
    </section>
  );
}
