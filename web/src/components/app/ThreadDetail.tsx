// Right-panel detail of one thread: breadcrumb "讨论串 > #12 标题", a live summary line and
// four tabs — 进度 (cards, who is doing what, live activity), 任务, 交付物 and 成员 — with
// the lead's message box (images allowed) at the bottom. Deep link: /projects/:pid/threads/:no/:tab.
import { ChevronRight, Maximize2, Square } from "lucide-react";
import { useEffect, useState } from "react";
import { Link } from "react-router-dom";
import { conversations, threads as threadActions } from "@/api/actions";
import { useAction } from "@/api/mutations";
import { keys, useActivity, useDeliverables, useQuestions, useTasks } from "@/api/queries";
import type { ActivityView, DeliverableView, EmployeeBrief, QuestionView, TaskView, ThreadView } from "@/api/views";
import { Loading } from "@/components/common";
import { activityLine, askingLabel, cardsByAsker, elapsed, isWorking, taskProgress, threadActive, waitingReason } from "@/lib/live";
import { deliverableHref } from "@/lib/library";
import { approvalCommand, approvalHistory, approvalVerdict, deliverableKind, leadOf, mainProgress, memberTone, openForUser, RUNTIME_LABEL, taskLook, taskTitle, threadLook, TIER_LABEL } from "@/lib/present";
import { clock, cn, timeAgo } from "@/lib/utils";
import { useAttachments } from "./attachments";
import { Bar, CheckMark, Composer, LiveDot, StateMark, StatusPill } from "./bits";
import { ReviewCard, WaitingCard } from "./cards";
import { DeliverableThumb } from "./DeliverableThumb";
import { ThreadMenu } from "./ThreadMenu";

export type DetailTab = "progress" | "tasks" | "deliverables" | "members";

export const DETAIL_TABS: [DetailTab, string][] = [
  ["progress", "进度"],
  ["tasks", "任务"],
  ["deliverables", "交付物"],
  ["members", "成员"],
];

export function detailTabOf(value: string | undefined): DetailTab {
  return DETAIL_TABS.some(([id]) => id === value) ? (value as DetailTab) : "progress";
}

/** Re-renders every second while `on` (elapsed clocks). */
export function useNow(on: boolean): number {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    if (!on) return;
    const t = setInterval(() => setNow(Date.now()), 1000);
    return () => clearInterval(t);
  }, [on]);
  return now;
}

interface DetailProps {
  pid: string;
  thread: ThreadView;
  tab: DetailTab;
  onTab: (tab: DetailTab) => void;
  onBack: () => void;
  fullHref: string;
}

export function ThreadDetail({ pid, thread, tab, onTab, onBack, fullHref }: DetailProps) {
  const questions = useQuestions(thread.id);
  const open = openForUser(questions.data);
  const deliverables = useDeliverables(pid, "", "");
  const mine = (deliverables.data?.items ?? []).filter((d) => d.thread_no === thread.no);
  const counts: Partial<Record<DetailTab, number>> = { deliverables: mine.length, members: thread.members.length, progress: open.length || undefined };
  return (
    <>
      <DetailHeader thread={thread} onBack={onBack} fullHref={fullHref} />
      <nav role="tablist" aria-label="讨论串视图" className="flex shrink-0 gap-1 border-b border-[#EFEDE8] px-3.5">
        {DETAIL_TABS.map(([id, label]) => (
          <button
            key={id}
            type="button"
            role="tab"
            aria-selected={tab === id}
            onClick={() => onTab(id)}
            className={cn("flex h-10 items-center gap-1 border-b-2 px-2.5 text-[13px]", tab === id ? "border-foreground font-semibold" : "border-transparent text-muted-foreground hover:text-foreground")}
          >
            {label}
            {counts[id] ? <span className={cn("rounded-full px-1.5 text-[11px]", id === "progress" ? "bg-[#FFF4EC] text-[#B4530B]" : "bg-raised text-muted-foreground")}>{counts[id]}</span> : null}
          </button>
        ))}
      </nav>
      <div className="scroll-thin flex flex-1 flex-col gap-4 overflow-y-auto px-5 py-4">
        {tab === "progress" && (questions.isLoading ? <Loading className="py-6" /> : <ProgressTab thread={thread} open={open} all={questions.data} />)}
        {tab === "tasks" && <TasksTab thread={thread} />}
        {tab === "deliverables" && <DeliverablesTab pid={pid} items={mine} loading={deliverables.isLoading} />}
        {tab === "members" && <MembersTab thread={thread} open={open} />}
      </div>
      <LeadComposer pid={pid} thread={thread} />
    </>
  );
}

function DetailHeader({ thread, onBack, fullHref }: { thread: ThreadView; onBack: () => void; fullHref: string }) {
  const look = threadLook(thread);
  const active = threadActive(thread);
  const now = useNow(active);
  const progress = mainProgress(thread);
  const since = thread.started_at ?? thread.created_at;
  const until = active ? now : Date.parse(thread.finished_at ?? thread.updated_at);
  const working = thread.members.some(isWorking);
  return (
    <div className="flex shrink-0 flex-col gap-1.5 border-b border-[#EFEDE8] px-3.5 pb-2.5 pt-2.5">
      <span className="flex min-w-0 items-center gap-1">
        <nav aria-label="位置" className="flex min-w-0 flex-1 items-center gap-0.5 text-[13px]">
          <button type="button" onClick={onBack} className="h-8 shrink-0 rounded-lg px-2 text-muted-foreground hover:bg-background hover:text-foreground">
            讨论串
          </button>
          <ChevronRight className="size-3.5 shrink-0 text-[#B7B3AA]" strokeWidth={2} />
          <span aria-current="page" className="truncate px-1 font-semibold" title={`#${thread.no} ${thread.title}`}>
            #{thread.no} {thread.title}
          </span>
        </nav>
        <StatusPill tone={look.tone} label={look.label} spin={look.spin} className="shrink-0" />
        <Link to={fullHref} aria-label="全屏打开讨论串" className="flex size-8 shrink-0 items-center justify-center rounded-lg text-[#4A4843] hover:bg-background">
          <Maximize2 className="size-[15px]" strokeWidth={1.8} />
        </Link>
        <ThreadMenu thread={thread} fullHref={fullHref} onDeleted={onBack} />
      </span>
      <span className="flex items-center gap-2.5 px-2 text-xs text-muted-foreground">
        {working && active && <LiveDot color="#2F6FDB" on size={7} />}
        <span className="whitespace-nowrap">{active ? `已进行 ${elapsed(since, until)}` : `用时 ${elapsed(since, until)}`}</span>
        <Bar pct={progress.pct} tone={look.tone} />
        <span className="whitespace-nowrap">主要任务 {progress.text}</span>
      </span>
    </div>
  );
}

/** 进度: why it waits, each employee's live line (cards inline), the activity feed, the lead's latest. */
function ProgressTab({ thread, open, all }: { thread: ThreadView; open: QuestionView[]; all?: QuestionView[] }) {
  const reason = waitingReason(thread, open);
  const byAsker = cardsByAsker(open);
  const names = new Set(thread.members.map((m) => m.name));
  const orphans = open.filter((q) => !names.has(q.asker));
  const lead = leadOf(thread)?.name;
  return (
    <>
      {reason && (
        <p role="status" className="rounded-lg bg-[#FFF4EC] px-3 py-2 text-[13px] text-[#B4530B]">
          {reason}
        </p>
      )}
      {orphans.map((q) => (
        <WaitingCard key={q.id} cid={thread.id} pid={thread.project_id} question={q} variant="soft" leadName={lead} />
      ))}
      {thread.status === "review" && <ReviewCard thread={thread} variant="soft" />}
      <section className="flex flex-col gap-1.5" aria-label="员工动态">
        <span className="text-xs text-muted-foreground">谁在做什么</span>
        {thread.members.map((m) => (
          <MemberLive key={m.id} thread={thread} member={m} card={byAsker.get(m.name)} cards={open.filter((q) => q.asker === m.name)} leadName={lead} />
        ))}
      </section>
      <ActivityFeed thread={thread} />
      <ApprovalHistory questions={all} />
      <section className="flex flex-col gap-1.5">
        <span className="text-xs text-muted-foreground">组长最近说</span>
        <p className="text-[13px] leading-[22px]">{thread.lead_last?.text || "还没有汇报。"}</p>
      </section>
    </>
  );
}

function MemberLive({ thread, member, card, cards, leadName }: { thread: ThreadView; member: EmployeeBrief; card?: QuestionView; cards: QuestionView[]; leadName?: string }) {
  const working = isWorking(member);
  const asking = cards.length > 0;
  const now = useNow(working || asking);
  const tone = asking ? "#B4530B" : memberTone(member);
  return (
    <div className={cn("flex flex-col gap-2 rounded-lg px-2.5 py-2", asking ? "bg-[#FFF9F4]" : "bg-background")}>
      <span className="flex min-w-0 items-center gap-2 text-[13px]">
        <LiveDot color={tone} on={working} />
        <span className="font-mono">{member.name}</span>
        <span className="text-xs" style={{ color: tone }}>
          {askingLabel(member, asking)}
        </span>
        {member.status?.since && (working || asking) && <span className="ml-auto whitespace-nowrap text-[11px] text-muted-foreground">{elapsed(member.status.since, now)}</span>}
      </span>
      {(member.status?.bubble || asking) && <span className="pl-4 text-xs leading-[18px] text-[#4A4843] [overflow-wrap:anywhere]">{activityLine(member, card)}</span>}
      {cards.map((q) => (
        <WaitingCard key={q.id} cid={thread.id} pid={thread.project_id} question={q} variant="soft" leadName={leadName} />
      ))}
    </div>
  );
}

/** 审批记录: permission requests the lead is judging or someone settled, with who decided. */
export function ApprovalHistory({ questions }: { questions?: QuestionView[] }) {
  const items = approvalHistory(questions);
  if (items.length === 0) return null;
  return (
    <section className="flex flex-col gap-1.5" aria-label="审批记录">
      <span className="text-xs text-muted-foreground">审批记录</span>
      <ol className="flex flex-col gap-1">
        {items.map((q) => (
          <li key={q.id} className="flex gap-2 text-xs leading-[19px] text-[#4A4843]">
            <span className="shrink-0 font-mono text-[11px] text-muted-foreground" title={timeAgo(q.answered_at ?? q.created_at)}>
              {clock(q.answered_at ?? q.created_at)}
            </span>
            <span className="min-w-0 [overflow-wrap:anywhere]">
              <span className="font-mono text-foreground">{q.asker}</span> {approvalCommand(q)} <span className="text-muted-foreground">— {approvalVerdict(q)}</span>
            </span>
          </li>
        ))}
      </ol>
    </section>
  );
}

const FEED_COLLAPSED = 4;

/** Thread activity, newest last; SSE appends lines as they happen. Collapsed to the last few. */
export function ActivityFeed({ thread }: { thread: ThreadView }) {
  const activity = useActivity(thread.id);
  const [expanded, setExpanded] = useState(false);
  const all: ActivityView[] = [...(activity.data?.items ?? [])].sort((a, b) => Date.parse(a.at) - Date.parse(b.at));
  const shown = expanded ? all : all.slice(-FEED_COLLAPSED);
  const live = threadActive(thread);
  return (
    <section className="flex flex-col gap-1.5" aria-label="动态">
      <span className="flex items-center gap-2 text-xs text-muted-foreground">
        动态
        {live && (
          <span className="flex items-center gap-1 text-[#2F6FDB]">
            <LiveDot color="#2F6FDB" on size={6} /> 实时
          </span>
        )}
        {all.length > FEED_COLLAPSED && (
          <button type="button" onClick={() => setExpanded(!expanded)} className="ml-auto hover:text-foreground">
            {expanded ? "收起" : `展开全部 ${all.length} 条`}
          </button>
        )}
      </span>
      {activity.isLoading && <Loading className="py-2" />}
      {!activity.isLoading && all.length === 0 && <span className="text-xs text-muted-foreground">还没有动态</span>}
      <ol className={cn("flex flex-col gap-1", expanded && "max-h-80 overflow-y-auto")}>
        {shown.map((e, i) => (
          <li key={`${e.at}-${i}`} className="flex gap-2 text-xs leading-[19px] text-[#4A4843]">
            <span className="shrink-0 font-mono text-[11px] text-muted-foreground" title={timeAgo(e.at)}>
              {clock(e.at)}
            </span>
            <span className="min-w-0 [overflow-wrap:anywhere]">
              <span className="font-mono text-foreground">{e.employee}</span> {e.text}
            </span>
          </li>
        ))}
      </ol>
    </section>
  );
}

/** 任务: small-task progress and the plan, each main task with its small tasks. */
function TasksTab({ thread }: { thread: ThreadView }) {
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
          <span className="flex items-center gap-2.5 text-[13px]">
            <CheckMark status={m.status} />
            <span className={cn("min-w-0 flex-1", m.status === "done" && "text-[#8A877F] line-through")}>{m.title}</span>
          </span>
          <TaskRows tasks={all.filter((t) => t.main_task_id === m.id)} all={all} />
        </section>
      ))}
      {loose.length > 0 && (
        <section className="flex flex-col gap-1">
          <span className="text-xs text-muted-foreground">其他小任务</span>
          <TaskRows tasks={loose} all={all} />
        </section>
      )}
    </>
  );
}

function TaskRows({ tasks, all }: { tasks: TaskView[]; all: TaskView[] }) {
  const visible = tasks.filter((t) => t.status !== "cancelled");
  if (visible.length === 0) return null;
  return (
    <div className="ml-[7px] flex flex-col border-l pl-[17px]">
      {visible.map((t) => {
        const look = taskLook(t, all);
        return (
          <span key={t.id} className="flex min-h-[26px] items-center gap-2 text-[13px]">
            <StateMark look={look} size={6} />
            <span className={cn("min-w-0 flex-1 truncate", t.status === "done" && "text-[#8A877F] line-through")} title={t.instruction}>
              {taskTitle(t)}
            </span>
            <span className="font-mono text-[11px] text-[#8A877F]">{t.claimed_by || "待领取"}</span>
            <span className="min-w-10 text-right text-[11px]" style={{ color: look.tone }}>
              {look.label}
            </span>
          </span>
        );
      })}
    </div>
  );
}

/** 交付物: this thread's designs, documents and files as cards that open the viewer. */
function DeliverablesTab({ pid, items, loading }: { pid: string; items: DeliverableView[]; loading: boolean }) {
  if (loading) return <Loading className="py-6" />;
  if (items.length === 0) return <p className="text-[13px] leading-6 text-muted-foreground">这个讨论串还没有交付物。员工发布的设计稿、文档和文件会出现在这里。</p>;
  return (
    <div className="grid grid-cols-[repeat(auto-fill,minmax(150px,1fr))] gap-3">
      {items.map((d) => (
        <DeliverableCard key={d.id} pid={pid} item={d} />
      ))}
    </div>
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
  if (target.to) return <Link to={target.to} className={cls}>{body}</Link>;
  return (
    <a href={target.href} target="_blank" rel="noreferrer" className={cls}>
      {body}
    </a>
  );
}

/** 成员: everyone in the thread with role, runtime, node, state and a stop button while working. */
function MembersTab({ thread, open }: { thread: ThreadView; open: QuestionView[] }) {
  const now = useNow(thread.members.some(isWorking));
  return (
    <div className="flex flex-col gap-2">
      {thread.members.map((m) => (
        <MemberRow key={m.id} thread={thread} member={m} asking={open.some((q) => q.asker === m.name)} now={now} />
      ))}
    </div>
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

function LeadComposer({ pid, thread }: { pid: string; thread: ThreadView }) {
  const attachments = useAttachments(pid);
  const lead = leadOf(thread);
  const send = useAction((v: { text: string; images: string[] }) => conversations.send(thread.id, v.text, false, v.images), {
    invalidate: [keys.messages(thread.id)],
    success: "已发给组长",
  });
  if (["done", "failed", "archived"].includes(thread.status))
    return <p className="mx-4 mb-4 rounded-xl bg-background px-3 py-2.5 text-center text-xs text-muted-foreground">讨论串已{thread.status === "archived" ? "归档" : "结束"}，不能再给组长发消息</p>;
  return (
    <Composer
      variant="inline"
      label="和组长说话"
      placeholder={`直接和组长 ${lead?.name ?? ""} 说话，可粘贴图片`}
      className="mx-4 mb-4"
      busy={send.isPending}
      attachments={attachments}
      onSend={(text, images) => send.mutateAsync({ text, images })}
      id={`lead-${thread.id}`}
    />
  );
}
