// The thread's side views, opened from the ⋯ menu over the chat: 任务 (main tasks with their
// small tasks), 交付物 (PRs, merge queue, CI and published deliverables) and 成员 (who is in
// the thread, where they run, stop buttons). The chat itself stays conversation only.
import { Square } from "lucide-react";
import { useEffect, useState } from "react";
import { Link } from "react-router-dom";
import { threads as threadActions } from "@/api/actions";
import { useAction } from "@/api/mutations";
import { keys, useChecks, useDeliverables, useMergeQueue, useQuestions, useRoles, useTasks } from "@/api/queries";
import type { DeliverableView, EmployeeBrief, TaskView, ThreadView } from "@/api/views";
import { ConfirmAction, Loading } from "@/components/common";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { askingLabel, elapsed, isWorking, taskProgress } from "@/lib/live";
import { deliverableHref } from "@/lib/library";
import { checkLook, deliverableKind, mergeLook, onePRLook, openForUser, prLook, RUNTIME_LABEL, taskLook, taskTitle, threadPRs, TIER_LABEL, memberTone } from "@/lib/present";
import { usesAsk } from "@/lib/roles";
import { safeHref } from "@/lib/safeurl";
import { cn, timeAgo } from "@/lib/utils";
import { Bar, CheckMark, LiveDot, StateMark } from "./bits";
import { DeliverableThumb } from "./DeliverableThumb";
import { THREAD_VIEWS, type ThreadViewId } from "./ThreadMenu";

/** Re-renders every second while `on` (elapsed clocks). */
function useNow(on: boolean): number {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    if (!on) return;
    const t = setInterval(() => setNow(Date.now()), 1000);
    return () => clearInterval(t);
  }, [on]);
  return now;
}

export function ThreadViewDialog({ pid, thread, view, onView }: { pid: string; thread: ThreadView; view: ThreadViewId | null; onView: (v: ThreadViewId | null) => void }) {
  return (
    <Dialog open={view !== null} onOpenChange={(o) => !o && onView(null)}>
      <DialogContent onOpenAutoFocus={(e) => e.preventDefault()} className="flex max-h-[85vh] w-[min(640px,calc(100vw-32px))] max-w-none flex-col gap-0 p-0 outline-none">
        <DialogHeader className="shrink-0 gap-2 border-b px-5 pb-0 pt-4">
          <DialogTitle className="truncate pr-8 text-[15px]">
            #{thread.no} {thread.title}
          </DialogTitle>
          <nav aria-label="讨论串视图" className="-mb-px flex gap-1">
            {THREAD_VIEWS.map(([id, label]) => (
              <button
                key={id}
                type="button"
                aria-pressed={view === id}
                onClick={() => onView(id)}
                className={cn("h-9 border-b-2 px-2 text-[13px]", view === id ? "border-foreground font-semibold" : "border-transparent text-muted-foreground hover:text-foreground")}
              >
                {label}
              </button>
            ))}
          </nav>
        </DialogHeader>
        <div className="scroll-thin flex min-h-0 flex-1 flex-col gap-4 overflow-y-auto px-5 py-4">
          {view === "tasks" && <TasksView thread={thread} />}
          {view === "deliverables" && <DeliverablesView pid={pid} thread={thread} />}
          {view === "members" && <MembersView thread={thread} />}
        </div>
      </DialogContent>
    </Dialog>
  );
}

/** 任务: small-task progress and each main task (click to toggle done) with its small tasks. */
function TasksView({ thread }: { thread: ThreadView }) {
  const tasks = useTasks(thread.id);
  const all = tasks.data ?? [];
  const p = taskProgress(all);
  const mains = thread.main_tasks.slice().sort((a, b) => a.pos - b.pos);
  const loose = all.filter((t) => !t.main_task_id && t.status !== "cancelled");
  if (tasks.isLoading) return <Loading className="py-6" />;
  return (
    <>
      <span className="flex items-center gap-2.5 text-xs text-muted-foreground">
        <span className="whitespace-nowrap">
          小任务 {p.done}/{p.total}
        </span>
        <Bar pct={`${p.pct}%`} tone="#2F6FDB" />
        {p.running > 0 && <span className="whitespace-nowrap">进行中 {p.running}</span>}
        {p.failed > 0 && <span className="whitespace-nowrap text-destructive">失败 {p.failed}</span>}
      </span>
      {mains.length === 0 && <p className="text-[13px] text-muted-foreground">组长还在列主要任务…</p>}
      {mains.map((m) => (
        <section key={m.id} className="flex flex-col gap-1">
          <MainTaskLine thread={thread} id={m.id} title={m.title} status={m.status} />
          <TaskRows thread={thread} tasks={all.filter((t) => t.main_task_id === m.id)} all={all} />
        </section>
      ))}
      {loose.length > 0 && (
        <section className="flex flex-col gap-1">
          <span className="text-xs text-muted-foreground">其他小任务</span>
          <TaskRows thread={thread} tasks={loose} all={all} />
        </section>
      )}
    </>
  );
}

function MainTaskLine({ thread, id, title, status }: { thread: ThreadView; id: string; title: string; status: ThreadView["main_tasks"][number]["status"] }) {
  const toggle = useAction(() => threadActions.setMainTask(thread.id, id, status === "done" ? "doing" : "done"), { invalidate: [keys.threads(thread.project_id)] });
  return (
    <span className="flex items-center gap-2.5 text-[13px]">
      <button type="button" title={status === "done" ? "取消完成" : "标记为完成"} onClick={() => toggle.mutate()} className="rounded-full">
        <CheckMark status={status} />
      </button>
      <span className={cn("min-w-0 flex-1", status === "done" && "text-[#8A877F] line-through")}>{title}</span>
    </span>
  );
}

function TaskRows({ thread, tasks, all }: { thread: ThreadView; tasks: TaskView[]; all: TaskView[] }) {
  const visible = tasks.filter((t) => t.status !== "cancelled");
  if (visible.length === 0) return null;
  return (
    <div className="ml-[7px] flex flex-col border-l pl-[17px]">
      {visible.map((t) => (
        <TaskRow key={t.id} thread={thread} task={t} all={all} />
      ))}
    </div>
  );
}

function TaskRow({ thread, task, all }: { thread: ThreadView; task: TaskView; all: TaskView[] }) {
  const look = taskLook(task, all);
  const retry = useAction(() => threadActions.retryTask(thread.id, task.id), { invalidate: [keys.tasks(thread.id)], success: "已重新放回任务池" });
  return (
    <span className="flex min-h-[26px] items-center gap-2 text-[13px]">
      <StateMark look={look} size={6} />
      <span className={cn("min-w-0 flex-1 truncate", task.status === "done" && "text-[#8A877F] line-through")} title={task.instruction}>
        {taskTitle(task)}
      </span>
      <span className="font-mono text-[11px] text-[#8A877F]">{task.claimed_by || "待领取"}</span>
      {task.status === "failed" ? (
        <button type="button" onClick={() => retry.mutate()} disabled={retry.isPending} className="min-w-10 text-right text-[11px] text-destructive underline-offset-2 hover:underline">
          失败 · 重试
        </button>
      ) : (
        <span className="min-w-10 text-right text-[11px]" style={{ color: look.tone }}>
          {look.label}
        </span>
      )}
    </span>
  );
}

/** 交付物: the thread's PRs (open one when the main tasks are done), merge queue, CI, then published deliverables. */
function DeliverablesView({ pid, thread }: { pid: string; thread: ThreadView }) {
  const deliverables = useDeliverables(pid, "", "");
  const mine = (deliverables.data?.items ?? []).filter((d) => d.thread_no === thread.no);
  return (
    <>
      <Delivery thread={thread} />
      <section className="flex flex-col gap-2">
        <span className="text-xs text-muted-foreground">设计稿、文档和文件</span>
        {deliverables.isLoading ? (
          <Loading className="py-6" />
        ) : mine.length === 0 ? (
          <p className="text-[13px] leading-6 text-muted-foreground">还没有。员工发布的设计稿、文档和文件会出现在这里。</p>
        ) : (
          <div className="grid grid-cols-[repeat(auto-fill,minmax(150px,1fr))] gap-3">
            {mine.map((d) => (
              <DeliverableCard key={d.id} pid={pid} item={d} />
            ))}
          </div>
        )}
      </section>
    </>
  );
}

function Delivery({ thread }: { thread: ThreadView }) {
  const queue = useMergeQueue(thread.id);
  const checks = useChecks(thread.id);
  const invalidate = [keys.threads(thread.project_id), keys.thread(thread.id)];
  const openPr = useAction((force: boolean) => threadActions.openPr(thread.id, force), { invalidate, success: "PR 已开，讨论串进入待验收" });
  const prs = threadPRs(thread);
  const allDone = thread.main_tasks.length > 0 && thread.main_tasks.every((m) => m.status === "done");
  const finished = ["done", "failed", "archived"].includes(thread.status);
  return (
    <section className="flex flex-col gap-2">
      <span className="flex items-center justify-between gap-2 text-xs text-muted-foreground">
        代码
        {!finished && !thread.pr_url && (allDone ? (
          <Button size="sm" onClick={() => openPr.mutate(false)} disabled={openPr.isPending}>
            开 PR
          </Button>
        ) : (
          <ConfirmAction title="主要任务还没全部完成" description="现在开 PR 会跳过“主要任务全部完成”这一条检查，组长剩下的工作会继续推到同一个分支。" confirmLabel="仍然开 PR" variant="default" size="sm" onConfirm={() => openPr.mutate(true)}>
            开 PR
          </ConfirmAction>
        ))}
      </span>
      {(queue.data ?? []).map((e) => (
        <DeliveryRow key={e.id} branch={`${e.repo ? `${e.repo} · ` : ""}${e.head_branch} → ${thread.branch}`} what={`${e.employee} · ${e.task_id}${e.error ? ` · ${e.error}` : ""}`} look={mergeLook(e.status)} />
      ))}
      {prs.length > 0 ? (
        prs.map((p) => (
          <DeliveryRow key={p.url ?? p.number} branch={`${p.repo ? `${p.repo} · ` : ""}${thread.branch} → ${p.base}`} what={`PR #${p.number}`} look={p.repo ? onePRLook(p) : prLook(thread)} href={safeHref(p.url)} />
        ))
      ) : (
        <DeliveryRow branch={`${thread.branch || "讨论串分支"} → 基准分支`} what="讨论串 PR" look={prLook(thread)} />
      )}
      {(checks.data ?? []).map((c) => {
        const look = checkLook(c);
        return (
          <a key={c.name} href={safeHref(c.url)} target="_blank" rel="noreferrer" className="flex items-center gap-2 px-2.5 text-[13px]">
            <StateMark look={look} />
            <span className="font-mono">{c.name}</span>
            <span style={{ color: look.tone }}>{look.label}</span>
            <span className="truncate text-muted-foreground">{c.summary}</span>
          </a>
        );
      })}
    </section>
  );
}

function DeliveryRow({ branch, what, look, href }: { branch: string; what: string; look: { tone: string; label: string }; href?: string }) {
  return (
    <span className="flex flex-wrap items-center gap-x-3.5 gap-y-1 rounded-lg bg-background px-2.5 py-2 text-[13px]">
      <span className="min-w-0 flex-[1_1_220px] font-mono [overflow-wrap:anywhere]">{branch}</span>
      <span className="text-muted-foreground">{what}</span>
      {href ? (
        <a href={href} target="_blank" rel="noreferrer" style={{ color: look.tone }}>
          {look.label} ↗
        </a>
      ) : (
        <span style={{ color: look.tone }}>{look.label}</span>
      )}
    </span>
  );
}

function DeliverableCard({ pid, item }: { pid: string; item: DeliverableView }) {
  const target = deliverableHref(pid, item);
  const look = deliverableKind(item.kind);
  const body = (
    <>
      <DeliverableThumb item={item} />
      <span className="flex flex-col gap-0.5 px-2.5 pb-2.5 pt-2">
        <span className="line-clamp-2 text-[13px] font-medium leading-[18px]">{item.title}</span>
        <span className="text-[11px] text-muted-foreground">
          {look.label} · {timeAgo(item.updated_at)}
        </span>
      </span>
    </>
  );
  const cls = "flex flex-col overflow-hidden rounded-xl border bg-panel text-left hover:border-input hover:shadow-sm";
  if (target.to)
    return (
      <Link to={target.to} className={cls}>
        {body}
      </Link>
    );
  return (
    <a href={target.href} target="_blank" rel="noreferrer" className={cls}>
      {body}
    </a>
  );
}

/** 成员: everyone in the thread with role, runtime, node and state; stop while working; the 全部放行 switch. */
function MembersView({ thread }: { thread: ThreadView }) {
  const questions = useQuestions(thread.id);
  const open = openForUser(questions.data);
  const now = useNow(thread.members.some(isWorking));
  const askRoles = usesAsk(useRoles().data?.roles ?? []);
  const finished = ["done", "failed", "archived"].includes(thread.status);
  return (
    <div className="flex flex-col gap-2">
      {!finished && (thread.auto_approve || askRoles) && <AutoApproveToggle thread={thread} />}
      {thread.members.map((m) => (
        <MemberRow key={m.id} thread={thread} member={m} asking={open.some((q) => q.asker === m.name)} now={now} />
      ))}
    </div>
  );
}

function AutoApproveToggle({ thread }: { thread: ThreadView }) {
  const [confirming, setConfirming] = useState(false);
  const update = useAction((v: boolean) => threadActions.update(thread.id, { auto_approve: v }), { invalidate: [keys.threads(thread.project_id), keys.thread(thread.id)] });
  return (
    <>
      <label className="flex cursor-pointer items-center gap-2 rounded-lg border px-3 py-2 text-[13px]">
        <input type="checkbox" checked={thread.auto_approve} onChange={(e) => (e.target.checked ? setConfirming(true) : update.mutate(false))} className="size-4 accent-[#B42318]" />
        本讨论串内全部放行
      </label>
      <ConfirmAction
        open={confirming}
        onOpenChange={setConfirming}
        title="本讨论串内全部放行？"
        description="只影响设成“逐条问”的成员：打开后它们的操作也不再询问，当前等你处理的审批会一并放行（会记入审计）。"
        confirmLabel="全部放行"
        onConfirm={() => update.mutate(true)}
      />
    </>
  );
}

function MemberRow({ thread, member, asking, now }: { thread: ThreadView; member: EmployeeBrief; asking: boolean; now: number }) {
  const stop = useAction(() => threadActions.interruptMember(thread.id, member.name), { success: `已让 ${member.name} 停下` });
  const working = isWorking(member);
  return (
    <div className="flex flex-col gap-1 rounded-lg border px-3 py-2.5">
      <span className="flex items-center gap-2 text-[13px]">
        <LiveDot color={asking ? "#B4530B" : memberTone(member)} on={working} />
        <span className="font-mono font-medium">{member.name}</span>
        <span className="rounded bg-[#EFEDE8] px-1.5 py-px text-[11px] text-[#4A4843]">{member.tier === "lead" ? TIER_LABEL.lead : member.title || member.role}</span>
        {working && (
          <button type="button" onClick={() => stop.mutate()} disabled={stop.isPending} aria-label={`让 ${member.name} 停下`} className="ml-auto flex h-7 items-center gap-1 rounded-md border px-2 text-xs hover:bg-background">
            <Square className="size-2.5 fill-current" /> 停下
          </button>
        )}
      </span>
      <span className="text-xs text-muted-foreground">
        {RUNTIME_LABEL[member.runtime] ?? member.runtime} · {member.node ? `${member.node.name}${member.node.online ? "" : "（离线）"}` : "未分配节点"}
      </span>
      <span className="text-xs text-[#4A4843]">
        {askingLabel(member, asking)}
        {member.status?.since ? ` · ${elapsed(member.status.since, now)}` : ""}
        {member.status?.task_id ? ` · 任务 ${member.status.task_id}` : ""}
      </span>
    </div>
  );
}
