import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  Activity,
  ChevronDown,
  ChevronRight,
  ExternalLink,
  GitBranch,
  MessageSquarePlus,
  SendHorizontal,
  Settings,
  TriangleAlert,
  Users,
} from "lucide-react";
import { useEffect, useMemo, useRef, useState, type KeyboardEvent } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import { toast } from "sonner";
import { Chip, EmptyState, errorText, Loading, nodeStatus, Panel, Spinner, StatusBadge, TASK_STATUS } from "@/components/common";
import { Markdown } from "@/components/Markdown";
import { Button } from "@/components/ui/button";
import { Textarea } from "@/components/ui/textarea";
import { api, type Conversation, type Message, type NodeInfo, type Project, type Task, type TeamDefinition } from "@/lib/api";
import { keys, useConversations, useNodes, useProject, useTeam } from "@/lib/queries";
import { clock, cn, timeAgo } from "@/lib/utils";

const messagesKey = (id: string) => ["messages", id] as const;
const tasksKey = (id: string) => ["tasks", id] as const;

export function ProjectChatPage() {
  const { projectId = "", conversationId } = useParams();
  const project = useProject(projectId);
  const conversations = useConversations(projectId);
  const navigate = useNavigate();
  const current = conversationId ?? conversations.data?.[0]?.id;

  if (project.error) {
    return (
      <div className="container py-10">
        <EmptyState title="找不到这个项目" description={errorText(project.error)} action={<Button asChild><Link to="/projects">回到项目列表</Link></Button>} />
      </div>
    );
  }
  if (!project.data) return <Loading />;

  return (
    <div className="flex flex-wrap items-start gap-4 p-4">
      <ConversationList
        project={project.data}
        conversations={conversations.data ?? []}
        current={current}
        onCreated={(c) => navigate(`/projects/${projectId}/c/${c.id}`)}
      />
      <Panel padding="none" className="min-h-[calc(100vh-7rem)] min-w-0 flex-[999_1_560px]">
        {current ? (
          <ChatPanel key={current} project={project.data} conversationId={current} />
        ) : (
          <FirstConversation project={project.data} onCreated={(c) => navigate(`/projects/${projectId}/c/${c.id}`)} />
        )}
      </Panel>
      <SidePanel project={project.data} conversationId={current} />
    </div>
  );
}

function ConversationList({
  project,
  conversations,
  current,
  onCreated,
}: {
  project: Project;
  conversations: Conversation[];
  current?: string;
  onCreated: (c: Conversation) => void;
}) {
  const client = useQueryClient();
  const create = useMutation({
    mutationFn: () => api<Conversation>("/api/conversations", { method: "POST", json: { title: `对话 ${conversations.length + 1}`, project_id: project.id } }),
    onSuccess: (c) => {
      client.invalidateQueries({ queryKey: keys.conversations(project.id) });
      onCreated(c);
    },
    onError: (e) => toast.error(errorText(e)),
  });
  return (
    <Panel as="nav" label="对话" padding="none" className="w-full min-w-0 flex-[1_1_220px] gap-1 p-3 lg:sticky lg:top-24 lg:max-w-[260px]">
      <div className="flex items-center justify-between px-1 pb-2">
        <h2 className="text-[15px] font-semibold">对话</h2>
        <Button size="sm" variant="ghost" onClick={() => create.mutate()} disabled={create.isPending}>
          <MessageSquarePlus /> 新对话
        </Button>
      </div>
      {conversations.length === 0 && <p className="px-1 text-xs text-muted-foreground">还没有对话</p>}
      {conversations.map((c) => (
        <Link
          key={c.id}
          to={`/projects/${project.id}/c/${c.id}`}
          aria-current={c.id === current ? "page" : undefined}
          className={cn(
            "flex flex-col gap-0.5 rounded-lg border border-transparent px-2.5 py-2 hover:bg-raised",
            c.id === current && "border-primary/70 bg-accent hover:bg-accent",
          )}
        >
          <span className="truncate font-medium">{c.title || "未命名对话"}</span>
          <span className="text-xs text-muted-foreground">{timeAgo(c.created_at)}</span>
        </Link>
      ))}
    </Panel>
  );
}

function FirstConversation({ project, onCreated }: { project: Project; onCreated: (c: Conversation) => void }) {
  const client = useQueryClient();
  const start = async (content: string) => {
    const c = await api<Conversation>("/api/conversations", { method: "POST", json: { title: content.slice(0, 40), project_id: project.id } });
    await api(`/api/conversations/${c.id}/messages`, { method: "POST", json: { content } });
    client.invalidateQueries({ queryKey: keys.conversations(project.id) });
    onCreated(c);
  };
  return (
    <>
      <ChatHeader project={project} />
      <div className="flex flex-1 items-center justify-center p-8">
        <EmptyState title="和负责人开始第一次对话" description="说清楚要做什么：目标、范围、怎么验收。负责人会拆任务、派给员工，并在这里汇报。" />
      </div>
      <Composer onSend={start} />
    </>
  );
}

function ChatHeader({ project }: { project: Project }) {
  const team = useTeam();
  const nodes = useNodes();
  const lead = leaderOf(team.data?.team ?? null);
  const status = nodeStatus(nodes.data?.find((n) => n.name === lead?.node));
  return (
    <header className="flex flex-wrap items-center justify-between gap-3 border-b px-5 py-3.5">
      <div className="flex min-w-0 flex-col gap-1">
        <h1 className="truncate text-lg font-semibold">{project.name}</h1>
        <span className="flex flex-wrap items-center gap-x-1.5 text-[13px] text-muted-foreground">
          {lead ? (
            <>
              你在和负责人 <span className="font-mono text-foreground">{lead.name}</span> 对话 · Claude Code · {lead.node ?? "?"} ·
              <StatusBadge tone={status.tone} label={status.label} dot />
            </>
          ) : (
            <>
              还没配置团队，<Link to="/team" className="text-primary hover:underline">先去配置</Link>
            </>
          )}
        </span>
      </div>
      <Button asChild variant="outline" size="sm">
        <Link to={`/projects/${project.id}/settings`}>
          <Settings /> 项目设置
        </Link>
      </Button>
    </header>
  );
}

function ChatPanel({ project, conversationId }: { project: Project; conversationId: string }) {
  const client = useQueryClient();
  const messages = useQuery({ queryKey: messagesKey(conversationId), queryFn: () => api<Message[]>(`/api/conversations/${conversationId}/messages`) });
  useLiveEvents(conversationId);
  const bottom = useRef<HTMLDivElement>(null);
  const list = messages.data ?? [];
  const waiting = list.length > 0 && list[list.length - 1].role === "user";

  useEffect(() => {
    bottom.current?.scrollIntoView({ behavior: "smooth", block: "end" });
  }, [list.length]);

  const send = async (content: string) => {
    const message = await api<Message>(`/api/conversations/${conversationId}/messages`, { method: "POST", json: { content } });
    client.setQueryData<Message[]>(messagesKey(conversationId), (old = []) => mergeMessage(old, message));
  };

  return (
    <>
      <ChatHeader project={project} />
      <div className="scroll-thin flex flex-1 flex-col gap-5 overflow-y-auto px-5 py-5">
        {messages.isLoading && <Loading />}
        {list.length === 0 && !messages.isLoading && (
          <p className="py-10 text-center text-[13px] text-muted-foreground">发一条消息开始。负责人收到后会回复你，派出去的任务会出现在右侧。</p>
        )}
        {list.map((m) => (
          <MessageBubble key={m.id} message={m} />
        ))}
        {waiting && (
          <div className="flex items-center gap-2 pl-11 text-[13px] text-muted-foreground">
            <Spinner className="size-3" /> 负责人正在处理……
          </div>
        )}
        <div ref={bottom} />
      </div>
      <Composer onSend={send} />
    </>
  );
}

function MessageBubble({ message }: { message: Message }) {
  if (message.role === "user") {
    return (
      <div className="flex max-w-[78%] flex-col items-end gap-1 self-end">
        <span className="text-xs text-muted-foreground">你 · {clock(message.created_at)}</span>
        <div className="whitespace-pre-wrap rounded-[12px_12px_4px_12px] border border-[#5a4226] bg-accent px-3.5 py-2.5 leading-6">{message.content}</div>
      </div>
    );
  }
  const error = message.role === "error";
  return (
    <div className="flex max-w-[86%] gap-2.5 self-start">
      <span
        className={cn(
          "flex size-8 shrink-0 items-center justify-center rounded-lg border-[3px] text-[13px] font-bold text-background",
          error ? "border-bad bg-bad/80" : "border-ok bg-[#f5b27a]",
        )}
      >
        {error ? "!" : "L"}
      </span>
      <div className="flex min-w-0 flex-col gap-1">
        <span className="text-xs text-muted-foreground">{error ? "出错了" : "负责人"} · {clock(message.created_at)}</span>
        <div className={cn("rounded-[4px_12px_12px_12px] px-3.5 py-2.5", error ? "border border-bad/40 bg-bad/10 text-bad" : "bg-raised")}>
          {error ? <span className="whitespace-pre-wrap">{message.content}</span> : <Markdown>{message.content}</Markdown>}
        </div>
      </div>
    </div>
  );
}

function Composer({ onSend }: { onSend: (content: string) => Promise<void> }) {
  const [text, setText] = useState("");
  const [busy, setBusy] = useState(false);
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
      className="flex flex-col gap-2 border-t px-5 py-4"
      onSubmit={(e) => {
        e.preventDefault();
        void submit();
      }}
    >
      <label htmlFor="chatbox" className="sr-only">给负责人发消息</label>
      <Textarea id="chatbox" rows={3} value={text} onChange={(e) => setText(e.target.value)} onKeyDown={onKey} placeholder="提需求、改计划、问进度……都发给负责人" className="resize-y" />
      <div className="flex flex-wrap items-center justify-between gap-2">
        <span className="text-xs text-muted-foreground">Enter 发送，Shift + Enter 换行</span>
        <Button type="submit" disabled={busy || !text.trim()}>
          {busy ? <Spinner /> : <SendHorizontal />} 发送
        </Button>
      </div>
    </form>
  );
}

// Live updates: new messages, task changes and activity lines over SSE.
const activityStore = new Map<string, string[]>();
const activityListeners = new Set<() => void>();

function useLiveEvents(conversationId: string) {
  const client = useQueryClient();
  useEffect(() => {
    const source = new EventSource(`/api/conversations/${conversationId}/events`);
    source.onmessage = (event) => {
      const data = JSON.parse(event.data) as { type: string; message?: Message; task?: Task; text?: string };
      if (data.type === "message" && data.message) {
        client.setQueryData<Message[]>(messagesKey(conversationId), (old = []) => mergeMessage(old, data.message!));
      } else if (data.type === "task" && data.task) {
        client.setQueryData<Task[]>(tasksKey(conversationId), (old = []) => upsertTask(old, data.task!));
      } else if (data.type === "activity" && data.text) {
        const lines = [...(activityStore.get(conversationId) ?? []), data.text].slice(-200);
        activityStore.set(conversationId, lines);
        activityListeners.forEach((f) => f());
      }
    };
    return () => source.close();
  }, [client, conversationId]);
}

function useActivity(conversationId?: string): string[] {
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

function upsertTask(list: Task[], task: Task): Task[] {
  const index = list.findIndex((t) => t.id === task.id);
  if (index === -1) return [...list, task];
  const copy = [...list];
  copy[index] = task;
  return copy;
}

function SidePanel({ project, conversationId }: { project: Project; conversationId?: string }) {
  return (
    <aside className="flex w-full min-w-0 flex-[1_1_300px] flex-col gap-3 lg:sticky lg:top-24 lg:max-w-[360px]">
      <RepoCard project={project} />
      {conversationId && <TasksCard conversationId={conversationId} />}
      <TeamCard />
      {conversationId && <ActivityCard conversationId={conversationId} />}
    </aside>
  );
}

function RepoCard({ project }: { project: Project }) {
  return (
    <Panel padding="sm" title="代码仓库" icon={<GitBranch />} action={<Link to={`/projects/${project.id}/settings`} className="text-[13px] text-primary hover:underline">管理</Link>}>
      {project.repo ? (
        <a
          href={`https://github.com/${project.repo}`}
          target="_blank"
          rel="noreferrer"
          className="flex flex-col gap-0.5 rounded-lg bg-raised p-2.5 hover:text-accent-foreground"
        >
          <span className="flex items-center gap-1.5 font-mono text-[13px]">
            {project.repo} <ExternalLink className="size-3" />
          </span>
          <span className="text-xs text-muted-foreground">GitHub · 基准分支 {project.branch || "默认"} · 每个员工独立克隆</span>
        </a>
      ) : (
        <p className="text-[13px] text-muted-foreground">没有关联仓库，员工在空目录里工作。</p>
      )}
      {project.context && (
        <details className="group rounded-lg bg-raised/60 p-2.5 text-[13px]">
          <summary className="cursor-pointer list-none text-muted-foreground group-open:mb-2">项目上下文 ▸</summary>
          <Markdown className="text-[13px]">{project.context}</Markdown>
        </details>
      )}
    </Panel>
  );
}

function TasksCard({ conversationId }: { conversationId: string }) {
  const tasks = useQuery({ queryKey: tasksKey(conversationId), queryFn: () => api<Task[]>(`/api/conversations/${conversationId}/tasks`) });
  const list = tasks.data ?? [];
  const done = list.filter((t) => t.status === "done").length;
  return (
    <Panel padding="sm" title={list.length ? `任务 · ${done} / ${list.length}` : "任务"} icon={<Activity />}>
      {list.length === 0 && <p className="text-[13px] text-muted-foreground">负责人派出的任务会显示在这里。</p>}
      <div className="flex flex-col gap-1.5">
        {list.map((t) => (
          <TaskRow key={t.id} task={t} />
        ))}
      </div>
    </Panel>
  );
}

function TaskRow({ task }: { task: Task }) {
  const [open, setOpen] = useState(false);
  const status = TASK_STATUS[task.status];
  return (
    <div className="rounded-lg bg-raised/60">
      <button type="button" onClick={() => setOpen(!open)} className="flex w-full items-start gap-2 p-2 text-left" aria-expanded={open}>
        {open ? <ChevronDown className="mt-0.5 size-3.5 shrink-0" /> : <ChevronRight className="mt-0.5 size-3.5 shrink-0" />}
        <span className="min-w-0 flex-1">
          <span className="flex items-center gap-1.5 text-xs text-muted-foreground">
            <span className="font-mono">{task.id}</span> → <span className="font-mono text-foreground">{task.assignee}</span>
          </span>
          <span className={cn("block text-[13px] leading-5", !open && "line-clamp-2")}>{task.instruction}</span>
        </span>
        <StatusBadge tone={status.tone} label={status.label} spinning={task.status === "running"} className="shrink-0" />
      </button>
      {open && task.output && (
        <div className="scroll-thin mx-2 mb-2 max-h-72 overflow-y-auto rounded-md bg-well p-2.5">
          <Markdown className="text-[12px]">{task.output}</Markdown>
        </div>
      )}
    </div>
  );
}

function TeamCard() {
  const team = useTeam();
  const nodes = useNodes();
  const online = useMemo(() => new Map((nodes.data ?? []).map((n: NodeInfo) => [n.name, n.online])), [nodes.data]);
  const employees = team.data?.team?.employees ?? [];
  const leader = team.data?.team?.leader;
  return (
    <Panel padding="sm" title="团队" icon={<Users />} action={<Link to="/team" className="text-[13px] text-primary hover:underline">编辑</Link>}>
      {team.data?.error && team.data.saved && (
        <p className="flex items-start gap-1.5 text-xs text-warn"><TriangleAlert className="mt-0.5 size-3.5 shrink-0" />{team.data.error}</p>
      )}
      <div className="flex flex-wrap gap-1.5">
        {employees.map((e) => (
          <Chip key={e.name} tone={online.get(e.node ?? "") ? "ok" : "bad"} title={e.description}>
            {e.name}
            {e.name === leader && <span className="font-sans text-[11px] text-primary">负责人</span>}
            <span className="text-muted-foreground">@{e.node}</span>
          </Chip>
        ))}
      </div>
    </Panel>
  );
}

function ActivityCard({ conversationId }: { conversationId: string }) {
  const lines = useActivity(conversationId);
  return (
    <Panel padding="sm" title="工作动态" icon={<Activity />}>
      {lines.length === 0 ? (
        <p className="text-[13px] text-muted-foreground">页面打开期间，员工的实时进展会滚动显示在这里。</p>
      ) : (
        <div className="scroll-thin flex max-h-80 flex-col gap-1 overflow-y-auto border-l-2 border-border pl-3">
          {lines.slice(-80).map((line, i) => (
            <span key={i} className="break-all font-mono text-[11px] leading-[18px] text-muted-foreground">{line}</span>
          ))}
        </div>
      )}
    </Panel>
  );
}

function leaderOf(team: TeamDefinition | null) {
  if (!team?.leader) return undefined;
  return team.employees?.find((e) => e.name === team.leader);
}
