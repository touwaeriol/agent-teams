// Thread board (full page): header with controls, tabs 主要任务 / 任务池 /
// 和组长的对话 / 交付, and an aside with cards, members and activity.
// Below md it renders the MobileThread board instead.
import { safeHref } from "@/lib/safeurl";
import { ArrowLeft, ChevronDown, ChevronRight } from "lucide-react";
import { useState } from "react";
import { Link, useParams } from "react-router-dom";
import { conversations, threads as threadActions } from "@/api/actions";
import { useAction } from "@/api/mutations";
import { keys, useActivity, useChecks, useMergeQueue, useProject, useQuestions, useTasks, useThreadByNo } from "@/api/queries";
import type { MainTaskView, ProjectView, TaskView, ThreadView } from "@/api/views";
import { Bar, CheckMark, Composer, Dot, StatusPill, Tag } from "@/components/app/bits";
import { Chat } from "@/components/app/Chat";
import { Checklist, ThreadCards } from "@/components/app/thread";
import { ConfirmAction, EmptyState, Loading } from "@/components/common";
import { Button } from "@/components/ui/button";
import { useIsMobile } from "@/lib/media";
import {
  checkLook,
  leadOf,
  mainCount,
  mainProgress,
  memberDoing,
  memberTone,
  mergeLook,
  openForUser,
  poolColumns,
  prLook,
  RUNTIME_LABEL,
  taskLook,
  taskTitle,
  threadLook,
  TIER_LABEL,
  visibleMainTasks,
} from "@/lib/present";
import { clock, cn, timeAgo } from "@/lib/utils";

type View = "main" | "pool" | "chat" | "delivery";
const VIEWS: [View, string][] = [
  ["main", "主要任务"],
  ["pool", "任务池"],
  ["chat", "和组长的对话"],
  ["delivery", "交付"],
];

export function ThreadPage() {
  const { pid = "", no = "" } = useParams();
  const project = useProject(pid);
  const { thread, isLoading } = useThreadByNo(pid, Number(no));
  const mobile = useIsMobile();
  if (isLoading || !project.data) return <Loading className="h-full" />;
  if (!thread) return <EmptyState title={`找不到讨论串 #${no}`} description="它可能已被归档或删除。" action={<Button asChild><Link to={`/projects/${pid}`}>回到项目</Link></Button>} />;
  return mobile ? <MobileThread thread={thread} /> : <DesktopThread thread={thread} project={project.data} />;
}

function DesktopThread({ thread, project }: { thread: ThreadView; project: ProjectView }) {
  const [view, setView] = useState<View>("main");
  return (
    <div className="flex min-h-full flex-col">
      <ThreadHeader thread={thread} project={project} view={view} setView={setView} />
      <div className="flex flex-1 flex-wrap items-start gap-5 px-7 pb-8 pt-5">
        <section className="flex min-w-0 flex-[999_1_560px] flex-col gap-3.5">
          {view === "main" && <MainTasksView thread={thread} />}
          {view === "pool" && <PoolView thread={thread} />}
          {view === "chat" && <LeadChatView thread={thread} />}
          {view === "delivery" && <DeliveryView thread={thread} />}
        </section>
        <ThreadAside thread={thread} />
      </div>
    </div>
  );
}

function ThreadHeader({ thread, project, view, setView }: { thread: ThreadView; project: ProjectView; view: View; setView: (v: View) => void }) {
  const look = threadLook(thread);
  const lead = leadOf(thread);
  const invalidate = [keys.threads(project.id), keys.thread(thread.id)];
  const update = useAction((body: { status?: "paused" | "running"; auto_approve?: boolean }) => threadActions.update(thread.id, body), { invalidate });
  const openPr = useAction((force: boolean) => threadActions.openPr(thread.id, force), { invalidate, success: "PR 已开，讨论串进入待验收" });
  const allDone = thread.main_tasks.length > 0 && thread.main_tasks.every((m) => m.status === "done");
  const paused = thread.status === "paused";
  const finished = ["done", "failed", "archived"].includes(thread.status);
  return (
    <header className="flex flex-col gap-3 border-b bg-panel px-7 pt-5">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div className="flex min-w-0 flex-col gap-1.5">
          <Link to={`/projects/${project.id}?thread=${thread.no}`} className="text-xs text-muted-foreground hover:text-foreground">
            {project.name} / 讨论串
          </Link>
          <span className="flex flex-wrap items-center gap-2.5">
            <span className="font-mono text-muted-foreground">#{thread.no}</span>
            <h1 className="text-[22px] font-semibold">{thread.title}</h1>
            <StatusPill tone={look.tone} label={look.label} />
          </span>
          <span className="flex flex-wrap gap-2">
            <Tag mono>{thread.branch ? `${thread.branch} ← ${thread.base_branch}` : `基准 ${thread.base_branch}`}</Tag>
            <Tag>{project.work_mode === "worktree" ? "worktree：每个成员一个" : "独立克隆：每个成员一份"}</Tag>
            {lead && <Tag>组长 {lead.name}{lead.node ? ` · ${lead.node.name}` : ""}</Tag>}
            <Tag>开始于 {clock(thread.created_at)}，{thread.opened_by === "user" ? "你创建" : "统筹创建"}</Tag>
          </span>
        </div>
        {!finished && (
          <div className="flex flex-wrap items-center gap-2">
            <AutoApproveToggle thread={thread} onChange={(v) => update.mutate({ auto_approve: v })} />
            <Button variant="outline" onClick={() => update.mutate({ status: paused ? "running" : "paused" })} disabled={update.isPending}>
              {paused ? "继续" : "暂停"}
            </Button>
            {thread.pr_url ? (
              <Button asChild>
                <a href={safeHref(thread.pr_url)} target="_blank" rel="noreferrer">
                  查看 PR #{thread.pr_number}
                </a>
              </Button>
            ) : allDone ? (
              <Button onClick={() => openPr.mutate(false)} disabled={openPr.isPending}>
                开 PR
              </Button>
            ) : (
              <ConfirmAction title="主要任务还没全部完成" description="现在开 PR 会跳过“主要任务全部完成”这一条检查，组长剩下的工作会继续推到同一个分支。" confirmLabel="仍然开 PR" variant="default" onConfirm={() => openPr.mutate(true)}>
                开 PR
              </ConfirmAction>
            )}
          </div>
        )}
      </div>
      <nav aria-label="视图" className="flex gap-1">
        {VIEWS.map(([id, label]) => (
          <button
            key={id}
            type="button"
            aria-pressed={view === id}
            onClick={() => setView(id)}
            className={cn("h-10 border-b-2 px-3 text-[13px]", view === id ? "border-foreground font-semibold" : "border-transparent text-muted-foreground hover:text-foreground")}
          >
            {label}
          </button>
        ))}
      </nav>
    </header>
  );
}

function AutoApproveToggle({ thread, onChange }: { thread: ThreadView; onChange: (v: boolean) => void }) {
  const [confirming, setConfirming] = useState(false);
  return (
    <>
      <label className="flex h-9 cursor-pointer items-center gap-2 rounded-lg border border-input bg-white px-3 text-[13px]">
        <input
          type="checkbox"
          checked={thread.auto_approve}
          onChange={(e) => (e.target.checked ? setConfirming(true) : onChange(false))}
          className="size-4 accent-[#B42318]"
        />
        本讨论串内全部放行
      </label>
      <ConfirmAction
        open={confirming}
        onOpenChange={setConfirming}
        title="本讨论串内全部放行？"
        description="打开后这个讨论串里所有成员的操作都不再询问，当前等你处理的审批也会一并放行（会记入审计）。"
        confirmLabel="全部放行"
        onConfirm={() => onChange(true)}
      />
    </>
  );
}

function MainTasksView({ thread }: { thread: ThreadView }) {
  const tasks = useTasks(thread.id);
  const [expanded, setExpanded] = useState(false);
  const { rows, hidden } = visibleMainTasks(thread.main_tasks, 6, expanded);
  if (thread.main_tasks.length === 0)
    return <div className="rounded-xl border border-[#E2DFD8] bg-panel px-4 py-6 text-center text-[13px] text-muted-foreground">组长还在列主要任务…</div>;
  return (
    <div className="flex flex-col gap-3.5">
      <div className="flex flex-col gap-0.5 rounded-xl border border-[#E2DFD8] bg-panel px-4 py-3">
        {rows.map((m) => (
          <MainTaskRow key={m.id} thread={thread} main={m} tasks={(tasks.data ?? []).filter((t) => t.main_task_id === m.id)} all={tasks.data ?? []} />
        ))}
        {hidden > 0 && (
          <button type="button" onClick={() => setExpanded(true)} className="ml-[26px] mt-1 self-start py-1 text-[13px] text-muted-foreground hover:text-foreground">
            显示另外 {hidden} 项
          </button>
        )}
      </div>
      <p className="text-xs text-[#8A877F]">主要任务一行一项；正在进行的那项展开它的小任务。小任务在“任务池”里按岗位领取。</p>
    </div>
  );
}

function MainTaskRow({ thread, main, tasks, all }: { thread: ThreadView; main: MainTaskView; tasks: TaskView[]; all: TaskView[] }) {
  const [open, setOpen] = useState(main.status === "doing");
  const toggle = useAction(() => threadActions.setMainTask(thread.id, main.id, main.status === "done" ? "doing" : "done"), { invalidate: [keys.threads(thread.project_id)] });
  const visible = tasks.filter((t) => t.status !== "cancelled");
  return (
    <div className="flex flex-col">
      <span className="flex min-h-[30px] items-center gap-2.5">
        <button type="button" title={main.status === "done" ? "取消完成" : "标记为完成"} onClick={() => toggle.mutate()} className="rounded-full">
          <CheckMark status={main.status} size={16} />
        </button>
        <button type="button" onClick={() => setOpen(!open)} className={cn("flex min-w-0 flex-1 items-center gap-1 text-left text-sm", main.status === "done" && "text-[#8A877F] line-through")}>
          {main.title}
          {visible.length > 0 && (open ? <ChevronDown className="size-3.5 text-[#8A877F]" /> : <ChevronRight className="size-3.5 text-[#8A877F]" />)}
        </button>
        <span className="text-xs text-[#8A877F]">{mainCount(main)}</span>
      </span>
      {open && visible.length > 0 && (
        <div className="mb-1.5 ml-[7px] mt-0.5 flex flex-col border-l pl-[17px]">
          {visible.map((t) => {
            const look = taskLook(t, all);
            return (
              <span key={t.id} className="flex min-h-[26px] items-center gap-2 text-[13px]">
                <Dot color={look.tone} size={6} />
                <span className={cn("min-w-0 flex-1 truncate", t.status === "done" && "text-[#8A877F] line-through")}>{taskTitle(t)}</span>
                <span className="font-mono text-[11px] text-[#8A877F]">{t.claimed_by || "待领取"}</span>
                <span className="min-w-10 text-right text-[11px]" style={{ color: look.tone }}>
                  {look.label}
                </span>
              </span>
            );
          })}
        </div>
      )}
    </div>
  );
}

function PoolView({ thread }: { thread: ThreadView }) {
  const tasks = useTasks(thread.id);
  const all = tasks.data ?? [];
  const mainTitle = (id: string | null) => thread.main_tasks.find((m) => m.id === id)?.title ?? "—";
  if (tasks.isLoading) return <Loading />;
  const columns = poolColumns(all);
  return (
    <div className="scroll-thin grid gap-3 overflow-x-auto" style={{ gridTemplateColumns: `repeat(${columns.length}, minmax(200px, 1fr))` }}>
      {columns.map((c) => (
        <div key={c.id} className="flex min-w-0 flex-col gap-2">
          <span className="flex justify-between px-1 text-[13px] font-semibold">
            <span>{c.title}</span>
            <span className="font-normal text-muted-foreground">{c.tasks.length}</span>
          </span>
          {c.tasks.map((t) => (
            <PoolCard key={t.id} thread={thread} task={t} all={all} main={mainTitle(t.main_task_id)} />
          ))}
        </div>
      ))}
    </div>
  );
}

function PoolCard({ thread, task, all, main }: { thread: ThreadView; task: TaskView; all: TaskView[]; main: string }) {
  const look = taskLook(task, all);
  const invalidate = [keys.tasks(thread.id)];
  const retry = useAction(() => threadActions.retryTask(thread.id, task.id), { invalidate, success: "已重新放回任务池" });
  const cancel = useAction(() => threadActions.cancelTask(thread.id, task.id), { invalidate, success: "已取消" });
  const who = task.claimed_by || (look.label === "等依赖" ? "等依赖" : "—");
  return (
    <div className="flex flex-col gap-1.5 rounded-[10px] border bg-panel px-3 py-2.5">
      <span className="text-[13px] leading-5">{taskTitle(task)}</span>
      <span className="text-[11px] text-muted-foreground">属于：{main}</span>
      <span className="flex items-center justify-between text-xs">
        <span className="rounded-full bg-[#EFEDE8] px-2 py-px text-[#4A4843]">{task.role}</span>
        <span className="font-mono text-muted-foreground">{who}</span>
      </span>
      {task.status === "failed" && (
        <span className="flex items-center justify-between text-[11px]">
          <span className="text-destructive">失败（第 {task.attempt} 次）</span>
          <button type="button" onClick={() => retry.mutate()} className="text-foreground underline-offset-2 hover:underline">
            重试
          </button>
        </span>
      )}
      {["pending", "claimed", "running", "blocked"].includes(task.status) && (
        <button type="button" onClick={() => cancel.mutate()} className="self-end text-[11px] text-muted-foreground hover:text-destructive">
          取消
        </button>
      )}
    </div>
  );
}

function LeadChatView({ thread }: { thread: ThreadView }) {
  return (
    <div className="rounded-xl border bg-panel p-[18px]">
      <Chat cid={thread.id} compact speakerColor="#2F6FDB" empty={<p className="text-[13px] text-muted-foreground">还没有对话。</p>} />
    </div>
  );
}

function DeliveryView({ thread }: { thread: ThreadView }) {
  const queue = useMergeQueue(thread.id);
  const checks = useChecks(thread.id);
  const pr = prLook(thread);
  return (
    <div className="flex flex-col gap-3 rounded-xl border bg-panel p-[18px]">
      <h2 className="text-[15px] font-semibold">交付</h2>
      <p className="text-[13px] leading-[22px] text-[#4A4843]">
        成员在自己的分支上完成小任务，组长审查后合并进 <code className="font-mono">{thread.branch || "讨论串分支"}</code>。全部主要任务完成后，组长开一个指向基准分支 {thread.base_branch} 的 PR，讨论串进入“待验收”；PR 合并或你点“验收通过”后才算完成。
      </p>
      {(queue.data ?? []).map((e) => {
        const look = mergeLook(e.status);
        return (
          <DeliveryRow key={e.id} branch={`${e.head_branch} → ${thread.branch}`} what={`${e.employee} · ${e.task_id}${e.error ? ` · ${e.error}` : ""}`} look={look} />
        );
      })}
      <DeliveryRow branch={`${thread.branch || "讨论串分支"} → ${thread.base_branch}`} what="讨论串 PR" look={pr} href={safeHref(thread.pr_url)} />
      {(checks.data ?? []).length > 0 && (
        <div className="flex flex-col gap-1.5 pt-1">
          <span className="text-xs text-muted-foreground">CI 检查</span>
          {(checks.data ?? []).map((c) => {
            const look = checkLook(c);
            return (
              <a key={c.name} href={safeHref(c.url)} target="_blank" rel="noreferrer" className="flex items-center gap-2 text-[13px]">
                <Dot color={look.tone} />
                <span className="font-mono">{c.name}</span>
                <span style={{ color: look.tone }}>{look.label}</span>
                <span className="truncate text-muted-foreground">{c.summary}</span>
              </a>
            );
          })}
        </div>
      )}
    </div>
  );
}

function DeliveryRow({ branch, what, look, href }: { branch: string; what: string; look: { tone: string; label: string }; href?: string }) {
  return (
    <span className="flex flex-wrap items-center gap-x-3.5 gap-y-2 rounded-lg bg-background px-2.5 py-2 text-[13px]">
      <span className="flex-[1_1_260px] font-mono">{branch}</span>
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

function ThreadAside({ thread }: { thread: ThreadView }) {
  const questions = useQuestions(thread.id);
  const activity = useActivity(thread.id);
  const lead = leadOf(thread);
  const send = useAction((text: string) => conversations.send(thread.id, text), { invalidate: [keys.messages(thread.id)], success: "已发给组长" });
  const feed = [...(activity.data?.items ?? [])].reverse().slice(0, 8);
  return (
    <aside className="flex min-w-0 max-w-[380px] flex-[1_1_300px] flex-col gap-3.5">
      <ThreadCards thread={thread} questions={openForUser(questions.data)} variant="plain" />
      <section className="flex flex-col gap-2.5 rounded-xl border bg-panel px-4 py-3.5">
        <h2 className="text-sm font-semibold">成员</h2>
        {thread.members.map((m) => (
          <div key={m.id} className="flex flex-col gap-0.5 rounded-lg bg-background px-2.5 py-2">
            <span className="flex items-center gap-2 text-[13px]">
              <Dot color={memberTone(m)} size={8} />
              <span className="font-mono">{m.name}</span>
              <span className="rounded bg-[#EFEDE8] px-1.5 py-px text-[11px] text-[#4A4843]">
                {m.tier === "lead" ? TIER_LABEL.lead : m.title || m.role}
                {m.runtime === "codex" ? ` · ${RUNTIME_LABEL.codex}` : ""}
              </span>
              <span className="ml-auto text-xs text-muted-foreground">{m.node?.name ?? "未分配节点"}</span>
            </span>
            <span className="pl-4 text-xs text-muted-foreground">{memberDoing(m)}</span>
          </div>
        ))}
      </section>
      <section className="flex flex-col gap-2 rounded-xl border bg-panel px-4 py-3.5">
        <h2 className="text-sm font-semibold">动态</h2>
        {feed.length === 0 && <span className="text-xs text-muted-foreground">还没有动态</span>}
        {feed.map((e, i) => (
          <span key={`${e.at}-${i}`} className="text-xs leading-[19px] text-[#4A4843] [overflow-wrap:anywhere]" title={timeAgo(e.at)}>
            <span className="font-mono text-foreground">{e.employee}</span> {e.text}
          </span>
        ))}
      </section>
      <Composer variant="inline" label="和组长说话" placeholder={`和组长 ${lead?.name ?? ""} 说话`} busy={send.isPending} onSend={(t) => send.mutateAsync(t)} id="to-lead" />
    </aside>
  );
}

/** MobileThread board: cards first, then the checklist, composer at the bottom. */
function MobileThread({ thread }: { thread: ThreadView }) {
  const questions = useQuestions(thread.id);
  const look = threadLook(thread);
  const lead = leadOf(thread);
  const progress = mainProgress(thread);
  const send = useAction((text: string) => conversations.send(thread.id, text), { invalidate: [keys.messages(thread.id)], success: "已发给组长" });
  return (
    <div className="flex h-full min-h-0 flex-col">
      <header className="flex flex-col gap-2 border-b bg-panel px-3 pb-3 pt-2">
        <div className="flex items-center gap-1">
          <Link to={`/projects/${thread.project_id}`} aria-label="返回统筹对话" className="flex size-11 items-center justify-center">
            <ArrowLeft className="size-5" strokeWidth={1.8} />
          </Link>
          <span className="flex min-w-0 flex-1 flex-col">
            <span className="truncate font-semibold">
              #{thread.no} {thread.title}
            </span>
            <span className="truncate text-[11px] text-muted-foreground">
              组长 {lead?.name ?? "—"} · {thread.branch || thread.base_branch}
            </span>
          </span>
          <StatusPill tone={look.tone} label={look.label} />
        </div>
        <span className="flex items-center gap-2.5 px-1">
          <Bar pct={progress.pct} tone={look.tone} />
          <span className="text-xs text-muted-foreground">主要任务 {progress.text}</span>
        </span>
      </header>
      <div className="flex flex-1 flex-col gap-3 overflow-y-auto px-3 py-3.5">
        <ThreadCards thread={thread} questions={openForUser(questions.data)} variant="mobile" />
        <div className="rounded-xl border border-[#E2DFD8] bg-panel px-3 py-2">
          <Checklist tasks={thread.main_tasks} size="md" />
        </div>
        {thread.lead_last && (
          <section className="flex flex-col gap-1 px-1">
            <span className="text-xs text-muted-foreground">组长最近说</span>
            <p className="text-[13px] leading-[22px]">{thread.lead_last.text}</p>
          </section>
        )}
      </div>
      <Composer variant="inline" label="和组长说话" placeholder="和组长说话" className="mx-3 mb-3 rounded-2xl" busy={send.isPending} onSend={(t) => send.mutateAsync(t)} id="m-lead" />
    </div>
  );
}
