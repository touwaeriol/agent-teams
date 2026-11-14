import { useQuery, useQueryClient } from "@tanstack/react-query";
import { ArrowRight, ArrowUp, ChevronDown, ChevronRight, ExternalLink, GitBranch, PanelRight, Settings, TriangleAlert } from "lucide-react";
import { useEffect, useMemo, useRef, useState, type KeyboardEvent, type ReactNode } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import { toast } from "sonner";
import { EmptyState, errorText, Loading, nodeStatus, PLAN_STATUS, Spinner, StatusBadge, TASK_STATUS } from "@/components/common";
import { Markdown } from "@/components/Markdown";
import { QuestionCard } from "@/components/QuestionCard";
import { Dot } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { api, type Conversation, type Mail, type Message, type NodeInfo, type Plan, type Project, type Question, type Task, type TeamDefinition } from "@/lib/api";
import { activityFeed, chatTimeline, pendingForUser, recipientLabel, taskTree, upsertById, type ActivityLine } from "@/lib/comms";
import { keys, useConversations, useMail, useNodes, usePlan, useProject, useQuestions, useTeam } from "@/lib/queries";
import { clock, cn } from "@/lib/utils";

const messagesKey = (id: string) => ["messages", id] as const;
const tasksKey = (id: string) => ["tasks", id] as const;

/** A project's conversation: centered reading column, floating composer, context on the right. */
export function ProjectChatPage() {
  const { projectId = "", conversationId } = useParams();
  const project = useProject(projectId);
  const conversations = useConversations(projectId);
  const [context, setContext] = useState(true);
  const current = conversationId ?? conversations.data?.[0]?.id;

  if (project.error) {
    return (
      <div className="mx-auto max-w-3xl p-8">
        <EmptyState title="找不到这个项目" description={errorText(project.error)} action={<Button asChild><Link to="/projects">回到项目列表</Link></Button>} />
      </div>
    );
  }
  if (!project.data || conversations.isLoading) return <Loading />;

  return (
    <div className="flex h-full min-h-0">
      <section className="flex min-w-0 flex-1 flex-col">
        <ChatHeader project={project.data} conversation={conversations.data?.find((c) => c.id === current)} onToggleContext={() => setContext(!context)} />
        {current ? <ChatBody key={current} conversationId={current} /> : <Welcome project={project.data} />}
      </section>
      {context && <ContextPanel project={project.data} conversationId={current} />}
    </div>
  );
}

function ChatHeader({ project, conversation, onToggleContext }: { project: Project; conversation?: Conversation; onToggleContext: () => void }) {
  return (
    <header className="flex h-14 shrink-0 items-center justify-between gap-3 border-b px-5">
      <div className="flex min-w-0 items-center gap-2 text-sm">
        <Link to={`/projects/${project.id}`} className="max-w-[50%] shrink-0 truncate font-semibold hover:underline">
          {project.name}
        </Link>
        {conversation && (
          <>
            <span className="text-muted-foreground">/</span>
            <span className="truncate text-muted-foreground">{conversation.title || "未命名对话"}</span>
          </>
        )}
      </div>
      <div className="flex items-center gap-1">
        <Button asChild variant="ghost" size="icon" aria-label="项目设置">
          <Link to={`/projects/${project.id}/settings`}>
            <Settings />
          </Link>
        </Button>
        <Button variant="ghost" size="icon" onClick={onToggleContext} aria-label="显示或隐藏上下文面板">
          <PanelRight />
        </Button>
      </div>
    </header>
  );
}

/** No conversation yet: a calm start screen with the composer in the middle. */
function Welcome({ project }: { project: Project }) {
  const client = useQueryClient();
  const navigate = useNavigate();
  const start = async (content: string) => {
    const c = await api<Conversation>("/api/conversations", { method: "POST", json: { title: content.slice(0, 40), project_id: project.id } });
    await api(`/api/conversations/${c.id}/messages`, { method: "POST", json: { content } });
    client.invalidateQueries({ queryKey: keys.conversations(project.id) });
    client.invalidateQueries({ queryKey: keys.allConversations });
    navigate(`/projects/${project.id}/c/${c.id}`);
  };
  return (
    <div className="flex flex-1 flex-col items-center justify-center gap-6 px-5 pb-16">
      <div className="text-center">
        <h1 className="text-2xl font-semibold tracking-tight">今天要做点什么？</h1>
        <p className="mt-2 text-sm text-muted-foreground">把需求说给负责人：目标、范围、怎么验收。它会拆任务、派给员工，并在这里汇报。</p>
      </div>
      <div className="w-full max-w-3xl">
        <Composer onSend={start} />
      </div>
    </div>
  );
}

function ChatBody({ conversationId }: { conversationId: string }) {
  const client = useQueryClient();
  const messages = useQuery({ queryKey: messagesKey(conversationId), queryFn: () => api<Message[]>(`/api/conversations/${conversationId}/messages`) });
  const questions = useQuestions(conversationId);
  useLiveEvents(conversationId);
  const bottom = useRef<HTMLDivElement>(null);
  const list = messages.data ?? [];
  const timeline = useMemo(() => chatTimeline(list, questions.data ?? []), [list, questions.data]);
  const pending = pendingForUser(questions.data).length;
  const waiting = list.length > 0 && list[list.length - 1].role === "user";

  useEffect(() => {
    bottom.current?.scrollIntoView({ behavior: "smooth", block: "end" });
  }, [timeline.length, waiting, pending]);

  const send = async (content: string) => {
    const message = await api<Message>(`/api/conversations/${conversationId}/messages`, { method: "POST", json: { content } });
    client.setQueryData<Message[]>(messagesKey(conversationId), (old = []) => mergeMessage(old, message));
  };

  return (
    <>
      <div className="scroll-thin min-h-0 flex-1 overflow-y-auto">
        <div className="mx-auto flex w-full max-w-3xl flex-col gap-8 px-5 py-8">
          {messages.isLoading && <Loading />}
          {list.length === 0 && !messages.isLoading && (
            <p className="py-16 text-center text-sm text-muted-foreground">发一条消息开始。负责人收到后会回复你，派出去的任务会出现在右侧。</p>
          )}
          {timeline.map((item) =>
            item.kind === "message" ? (
              <MessageItem key={`m${item.message.id}`} message={item.message} />
            ) : (
              <QuestionCard key={`q${item.question.id}`} conversationId={conversationId} question={item.question} />
            ),
          )}
          {pending > 0 && (
            <p className="text-sm text-warn">有 {pending} 个问题在等你处理，员工会一直等到你回答。</p>
          )}
          {waiting && (
            <div className="flex items-center gap-2 text-sm text-muted-foreground">
              <Spinner className="size-3.5" /> 负责人正在处理……
            </div>
          )}
          <div ref={bottom} />
        </div>
      </div>
      <div className="shrink-0 px-5 pb-5">
        <div className="mx-auto w-full max-w-3xl">
          <Composer onSend={send} />
        </div>
      </div>
    </>
  );
}

function MessageItem({ message }: { message: Message }) {
  if (message.role === "user") {
    return (
      <div className="flex justify-end">
        <div className="max-w-[85%] whitespace-pre-wrap rounded-2xl bg-raised px-4 py-2.5 leading-7" title={clock(message.created_at)}>
          {message.content}
        </div>
      </div>
    );
  }
  if (message.role === "error") {
    return (
      <div className="flex items-start gap-2 rounded-xl border border-bad/30 bg-bad/5 px-4 py-3 text-sm text-bad">
        <TriangleAlert className="mt-0.5 size-4 shrink-0" />
        <span className="whitespace-pre-wrap">{message.content}</span>
      </div>
    );
  }
  return (
    <article className="flex flex-col gap-1.5">
      <span className="flex items-center gap-1.5 text-xs text-muted-foreground">
        <span className="size-2 rounded-full bg-brand" aria-hidden />
        负责人 · {clock(message.created_at)}
      </span>
      <Markdown className="text-[15px] leading-7">{message.content}</Markdown>
    </article>
  );
}

/** Rounded, floating input: text on top, who is listening and send below. */
function Composer({ onSend }: { onSend: (content: string) => Promise<void> }) {
  const [text, setText] = useState("");
  const [busy, setBusy] = useState(false);
  const area = useRef<HTMLTextAreaElement>(null);
  useEffect(() => {
    const el = area.current;
    if (!el) return;
    el.style.height = "auto";
    el.style.height = `${Math.min(el.scrollHeight, 240)}px`;
  }, [text]);
  const submit = async () => {
    const content = text.trim();
    if (!content || busy) return;
    setBusy(true);
    try {
      await onSend(content);
      setText("");
    } catch (e) {
      toast.error(errorText(e));
    } finally {
      setBusy(false);
    }
  };
  const onKey = (e: KeyboardEvent<HTMLTextAreaElement>) => {
    if (e.key === "Enter" && !e.shiftKey && !e.nativeEvent.isComposing) {
      e.preventDefault();
      void submit();
    }
  };
  return (
    <form
      onSubmit={(e) => {
        e.preventDefault();
        void submit();
      }}
      className="rounded-2xl border bg-panel shadow-[0_2px_12px_-4px_rgb(0_0_0/0.08)] focus-within:border-input"
    >
      <label htmlFor="chatbox" className="sr-only">给负责人发消息</label>
      <textarea
        id="chatbox"
        ref={area}
        rows={2}
        value={text}
        onChange={(e) => setText(e.target.value)}
        onKeyDown={onKey}
        placeholder="提需求、改计划、问进度……"
        className="block w-full resize-none bg-transparent px-4 pt-3.5 text-[15px] leading-6 outline-none placeholder:text-muted-foreground"
      />
      <div className="flex items-center justify-between gap-2 px-3 pb-3 pt-1">
        <LeaderStatus />
        <Button type="submit" size="icon" disabled={busy || !text.trim()} aria-label="发送" className="size-8 rounded-lg">
          {busy ? <Spinner className="size-3.5" /> : <ArrowUp />}
        </Button>
      </div>
    </form>
  );
}

function LeaderStatus() {
  const team = useTeam();
  const nodes = useNodes();
  const lead = leaderOf(team.data?.team ?? null);
  if (!lead) {
    return (
      <Link to="/settings/team" className="px-1 text-xs text-muted-foreground hover:text-foreground">
        还没配置团队 →
      </Link>
    );
  }
  const status = nodeStatus(nodes.data?.find((n) => n.name === lead.node));
  return (
    <span className="flex items-center gap-1.5 px-1 text-xs text-muted-foreground">
      <Dot tone={status.tone} />
      负责人 <span className="font-mono text-foreground/80">{lead.name}</span> · {lead.node} · {status.label}
    </span>
  );
}

// Live updates over SSE: messages, tasks, activity lines, questions, mail and the plan.
const activityStore = new Map<string, ActivityLine[]>();

interface LiveEvent {
  type: string;
  message?: Message;
  task?: Task;
  text?: string;
  question?: Question;
  mail?: Mail;
  plan?: Plan;
}
const activityListeners = new Set<() => void>();

function useLiveEvents(conversationId: string) {
  const client = useQueryClient();
  useEffect(() => {
    const source = new EventSource(`/api/conversations/${conversationId}/events`);
    source.onmessage = (event) => {
      const data = JSON.parse(event.data) as LiveEvent;
      if (data.type === "message" && data.message) {
        client.setQueryData<Message[]>(messagesKey(conversationId), (old = []) => mergeMessage(old, data.message!));
      } else if (data.type === "task" && data.task) {
        client.setQueryData<Task[]>(tasksKey(conversationId), (old = []) => upsertById(old, data.task!));
      } else if (data.type === "activity" && data.text) {
        const lines = [...(activityStore.get(conversationId) ?? []), { at: Date.now(), text: data.text }].slice(-200);
        activityStore.set(conversationId, lines);
        activityListeners.forEach((f) => f());
      } else if (data.type === "question" && data.question) {
        client.setQueryData<Question[]>(keys.questions(conversationId), (old = []) => upsertById(old, data.question!));
      } else if (data.type === "mail" && data.mail) {
        client.setQueryData<Mail[]>(keys.mail(conversationId), (old = []) => upsertById(old, data.mail!).slice(-200));
      } else if (data.type === "plan" && data.plan) {
        client.setQueryData<Plan>(keys.plan(conversationId), data.plan);
      }
    };
    return () => source.close();
  }, [client, conversationId]);
}

function useActivity(conversationId?: string): ActivityLine[] {
  const [, force] = useState(0);
  useEffect(() => {
    const listener = () => force((n) => n + 1);
    activityListeners.add(listener);
    return () => {
      activityListeners.delete(listener);
    };
  }, []);
  return conversationId ? activityStore.get(conversationId) ?? [] : [];
}

function mergeMessage(list: Message[], message: Message): Message[] {
  return list.some((m) => m.id === message.id) ? list : [...list, message].sort((a, b) => a.id - b.id);
}

/** Everything the conversation runs on: repository, tasks, team and live activity. */
function ContextPanel({ project, conversationId }: { project: Project; conversationId?: string }) {
  return (
    <aside className="scroll-thin hidden w-[320px] shrink-0 overflow-y-auto border-l bg-panel xl:block">
      <Section title="代码仓库" action={<Link to={`/projects/${project.id}/settings`} className="text-xs text-muted-foreground hover:text-foreground">管理</Link>}>
        <RepoInfo project={project} />
      </Section>
      {conversationId && <PlanSection conversationId={conversationId} />}
      {conversationId && <TasksSection conversationId={conversationId} />}
      <TeamSection />
      {conversationId && <ActivitySection conversationId={conversationId} />}
    </aside>
  );
}

function Section({ title, action, children }: { title: string; action?: ReactNode; children: ReactNode }) {
  return (
    <section className="flex flex-col gap-2.5 border-b px-5 py-4 last:border-0">
      <div className="flex items-center justify-between">
        <h2 className="text-xs font-medium uppercase tracking-wide text-muted-foreground">{title}</h2>
        {action}
      </div>
      {children}
    </section>
  );
}

function RepoInfo({ project }: { project: Project }) {
  if (!project.repo) return <p className="text-sm text-muted-foreground">没有关联仓库，员工在空目录里工作。</p>;
  return (
    <>
      <a href={`https://github.com/${project.repo}`} target="_blank" rel="noreferrer" className="flex items-center gap-1.5 font-mono text-[13px] hover:underline">
        {project.repo} <ExternalLink className="size-3 text-muted-foreground" />
      </a>
      <span className="flex items-center gap-1.5 text-xs text-muted-foreground">
        <GitBranch className="size-3.5" /> 基准分支 {project.branch || "默认"} · 每个员工独立克隆
      </span>
      {project.context && (
        <details className="group text-sm">
          <summary className="cursor-pointer text-xs text-muted-foreground hover:text-foreground">项目上下文</summary>
          <Markdown className="mt-2 text-[13px]">{project.context}</Markdown>
        </details>
      )}
    </>
  );
}

function TasksSection({ conversationId }: { conversationId: string }) {
  const tasks = useQuery({ queryKey: tasksKey(conversationId), queryFn: () => api<Task[]>(`/api/conversations/${conversationId}/tasks`) });
  const list = tasks.data ?? [];
  const done = list.filter((t) => t.status === "done").length;
  const percent = list.length ? Math.round((done / list.length) * 100) : 0;
  return (
    <Section title="任务进度" action={list.length ? <span className="text-xs text-muted-foreground">{done} / {list.length}</span> : undefined}>
      {list.length === 0 ? (
        <p className="text-sm text-muted-foreground">负责人派出的任务会显示在这里。</p>
      ) : (
        <>
          <div className="h-1.5 overflow-hidden rounded-full bg-raised" role="progressbar" aria-valuenow={percent} aria-valuemin={0} aria-valuemax={100}>
            <div className="h-full rounded-full bg-ok transition-all" style={{ width: `${percent}%` }} />
          </div>
          <div className="flex flex-col">
            {taskTree(list).map(({ task, depth }) => (
              <TaskRow key={task.id} task={task} depth={depth} />
            ))}
          </div>
        </>
      )}
    </Section>
  );
}

function TaskRow({ task, depth }: { task: Task; depth: number }) {
  const [open, setOpen] = useState(false);
  const status = TASK_STATUS[task.status];
  return (
    <div
      className={cn("-mx-2 rounded-lg hover:bg-raised/60", depth > 0 && "border-l")}
      style={depth > 0 ? { marginLeft: `${Math.min(depth, 4) * 14 - 8}px` } : undefined}
    >
      <button type="button" onClick={() => setOpen(!open)} className="flex w-full items-start gap-2 px-2 py-2 text-left" aria-expanded={open}>
        {open ? <ChevronDown className="mt-0.5 size-3.5 shrink-0 text-muted-foreground" /> : <ChevronRight className="mt-0.5 size-3.5 shrink-0 text-muted-foreground" />}
        <span className="min-w-0 flex-1">
          <span className={cn("block text-[13px] leading-5", !open && "line-clamp-2")}>{task.instruction}</span>
          <span className="font-mono text-[11px] text-muted-foreground">
            {task.id} → {task.assignee}
            {task.parent ? ` · 子任务（${task.parent}）` : ""}
          </span>
        </span>
        <StatusBadge tone={status.tone} label={status.label} spinning={task.status === "running"} className="shrink-0" />
      </button>
      {open && task.output && (
        <div className="scroll-thin mx-2 mb-2 max-h-72 overflow-y-auto rounded-md border bg-background p-2.5">
          <Markdown className="text-[12px]">{task.output}</Markdown>
        </div>
      )}
    </div>
  );
}

function TeamSection() {
  const team = useTeam();
  const nodes = useNodes();
  const online = useMemo(() => new Map((nodes.data ?? []).map((n: NodeInfo) => [n.name, n.online])), [nodes.data]);
  const employees = team.data?.team?.employees ?? [];
  const leader = team.data?.team?.leader;
  return (
    <Section title="团队" action={<Link to="/settings/team" className="text-xs text-muted-foreground hover:text-foreground">编辑</Link>}>
      {team.data?.error && team.data.saved && (
        <p className="flex items-start gap-1.5 text-xs text-warn">
          <TriangleAlert className="mt-0.5 size-3.5 shrink-0" />
          {team.data.error}
        </p>
      )}
      <ul className="flex flex-col gap-1.5">
        {employees.map((e) => (
          <li key={e.name} className="flex items-center gap-2 text-[13px]" title={e.description}>
            <Dot tone={online.get(e.node ?? "") ? "ok" : "bad"} />
            <span className="font-mono">{e.name}</span>
            {e.name === leader && <span className="text-[11px] text-brand">负责人</span>}
            {e.type === "codex" && <span className="text-[11px] text-info">Codex</span>}
            <span className="ml-auto text-xs text-muted-foreground">{e.node}</span>
          </li>
        ))}
      </ul>
    </Section>
  );
}

function ActivitySection({ conversationId }: { conversationId: string }) {
  const lines = useActivity(conversationId);
  const mail = useMail(conversationId);
  const items = useMemo(() => activityFeed(lines, mail.data ?? []).slice(-80), [lines, mail.data]);
  return (
    <Section title="工作动态">
      {items.length === 0 ? (
        <p className="text-sm text-muted-foreground">页面打开期间，员工的实时进展和彼此之间的消息会显示在这里。</p>
      ) : (
        <div className="scroll-thin flex max-h-80 flex-col gap-1 overflow-y-auto">
          {items.map((item, i) =>
            item.kind === "mail" ? (
              <MailLine key={`m${item.mail.id}`} mail={item.mail} />
            ) : (
              <span key={i} className="break-all font-mono text-[11px] leading-[18px] text-muted-foreground">
                {item.text}
              </span>
            ),
          )}
        </div>
      )}
    </Section>
  );
}

/** A message between members: who to whom, then the text. */
function MailLine({ mail }: { mail: Mail }) {
  return (
    <div className="my-0.5 rounded-md border bg-background px-2 py-1.5 text-[12px] leading-[18px]" title={clock(mail.created_at)}>
      <span className="flex items-center gap-1 font-mono text-[11px] text-muted-foreground">
        {mail.sender} <ArrowRight className="size-3" /> {recipientLabel(mail.recipient)}
      </span>
      <span className="block whitespace-pre-wrap break-words">{mail.body}</span>
    </div>
  );
}

/** The lead's plan (update_plan): steps with their state. */
function PlanSection({ conversationId }: { conversationId: string }) {
  const plan = usePlan(conversationId);
  const steps = plan.data?.steps ?? [];
  const done = steps.filter((s) => s.status === "done").length;
  return (
    <Section title="计划" action={steps.length ? <span className="text-xs text-muted-foreground">{done} / {steps.length}</span> : undefined}>
      {steps.length === 0 ? (
        <p className="text-sm text-muted-foreground">负责人定下计划后会显示在这里。</p>
      ) : (
        <ol className="flex flex-col gap-1.5">
          {steps.map((step, i) => (
            <li key={i} className="flex items-start gap-2 text-[13px] leading-5">
              <span className="w-4 shrink-0 text-right font-mono text-[11px] leading-5 text-muted-foreground">{i + 1}</span>
              <span className={cn("min-w-0 flex-1", step.status === "done" && "text-muted-foreground line-through")}>{step.title}</span>
              <StatusBadge tone={PLAN_STATUS[step.status]?.tone ?? "neutral"} label={PLAN_STATUS[step.status]?.label ?? step.status} spinning={step.status === "doing"} className="shrink-0" />
            </li>
          ))}
        </ol>
      )}
    </Section>
  );
}

function leaderOf(team: TeamDefinition | null) {
  if (!team?.leader) return undefined;
  return team.employees?.find((e) => e.name === team.leader);
}
