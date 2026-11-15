// Main board: chat with the project coordinator in the middle, the thread
// list (→ thread detail) on the right. Phones get thread chips instead of the panel.
import { Home, Settings, Square, Users } from "lucide-react";
import { Link, useNavigate, useParams, useSearchParams } from "react-router-dom";
import { conversations } from "@/api/actions";
import { useAction } from "@/api/mutations";
import { keys, useCoordinator, useProject, useQuestions, useThreads, useWaiting } from "@/api/queries";
import type { CoordinatorView, ProjectView, QuestionView, ThreadView } from "@/api/views";
import { Composer, Dot } from "@/components/app/bits";
import { WaitingCard } from "@/components/app/cards";
import { Chat } from "@/components/app/Chat";
import { ThreadList, ThreadPanel } from "@/components/app/thread";
import { EmptyState, errorText, Loading } from "@/components/common";
import { NavButton } from "@/components/shell/Shell";
import { markTone, openForUser } from "@/lib/present";

export function useProjectId(): string {
  return useParams().pid ?? "";
}

/** Waiting questions keyed by thread number (first open one wins), for list lines. */
export function waitingIndex(questions: QuestionView[] | undefined): Map<number, QuestionView> {
  const map = new Map<number, QuestionView>();
  for (const q of questions ?? []) if (q.thread_no !== null && !map.has(q.thread_no)) map.set(q.thread_no, q);
  return map;
}

export function MainPage() {
  const pid = useProjectId();
  const project = useProject(pid);
  const coordinator = useCoordinator(pid);
  const threads = useThreads(pid);
  const [params, setParams] = useSearchParams();
  const navigate = useNavigate();
  const openNo = Number(params.get("thread")) || null;
  const openThread = (no: number) => {
    if (window.matchMedia("(min-width: 1024px)").matches) setParams({ thread: String(no) });
    else navigate(`/projects/${pid}/threads/${no}`);
  };
  if (project.error) return <EmptyState title="找不到这个项目" description={errorText(project.error)} />;
  if (!project.data) return <Loading className="h-full" />;
  const list = threads.data ?? [];
  return (
    <div className="flex h-full min-h-0 flex-1">
      <main className="flex min-h-0 min-w-0 flex-1 flex-col">
        <MainHeader project={project.data} coordinator={coordinator.data} threads={list} />
        <ThreadChips pid={pid} threads={list} />
        <div className="flex-1 overflow-y-auto">
          <div className="mx-auto flex max-w-[700px] flex-col px-4 pb-6 pt-4 sm:px-6 sm:pt-8">
            <CoordinatorChat pid={pid} coordinator={coordinator.data} threads={list} onOpenThread={openThread} />
          </div>
        </div>
        <CoordinatorComposer coordinator={coordinator.data} />
      </main>
      <aside aria-label="讨论串" className="hidden h-full w-[min(440px,32vw)] min-w-[340px] shrink-0 flex-col border-l bg-panel lg:flex">
        {openNo ? <PanelDetail pid={pid} no={openNo} threads={list} onBack={() => setParams({})} /> : <PanelList pid={pid} threads={list} loading={threads.isLoading} onOpen={openThread} />}
      </aside>
    </div>
  );
}

function coordinatorState(c: CoordinatorView | undefined): { tone: string; text: string } {
  const name = c?.employee.name ?? "leader";
  if (!c?.employee.node) return { tone: "#B7B3AA", text: `${name} 待命` };
  if (!c.employee.node.online) return { tone: "#B42318", text: `${name} 离线` };
  return { tone: "#2E7D4F", text: `${name} 在线` };
}

function MainHeader({ project, coordinator, threads }: { project: ProjectView; coordinator?: CoordinatorView; threads: ThreadView[] }) {
  const state = coordinatorState(coordinator);
  const waiting = threads.reduce((n, t) => n + t.counts.waiting + (t.status === "review" ? 1 : 0), 0);
  const firstWaiting = threads.find((t) => t.counts.waiting > 0 || t.status === "review");
  const icon = "flex size-9 items-center justify-center rounded-lg text-[#4A4843] hover:bg-raised";
  return (
    <header className="flex h-14 shrink-0 items-center justify-between gap-3 border-b bg-background px-2 sm:px-6 max-md:bg-panel">
      <div className="flex min-w-0 items-center gap-2.5">
        <NavButton />
        <span className="flex min-w-0 flex-col md:flex-row md:items-center md:gap-2.5">
          <span className="truncate font-semibold">{project.name}</span>
          <span className="truncate font-mono text-xs text-muted-foreground max-md:hidden">{project.repo ? `${project.repo} · ${project.branch}` : "未关联仓库"}</span>
          <span className="text-[11px] text-muted-foreground md:hidden">项目统筹 · 只分派，不改代码</span>
        </span>
      </div>
      <span className="flex items-center gap-1.5">
        <span className="mr-1.5 hidden items-center gap-1.5 text-xs text-muted-foreground sm:flex">
          <Dot color={state.tone} />
          {state.text}
        </span>
        {waiting > 0 && firstWaiting && (
          <Link to={`/projects/${project.id}/threads/${firstWaiting.no}`} className="mr-1.5 flex h-8 items-center rounded-full bg-[#FFF4EC] px-2.5 text-xs text-[#B4530B] lg:hidden">
            等你 {waiting}
          </Link>
        )}
        <Link to={`/projects/${project.id}/overview`} aria-label="项目首页" className={`${icon} max-sm:hidden`}>
          <Home className="size-4" strokeWidth={1.8} />
        </Link>
        <Link to={`/projects/${project.id}/office`} aria-label="办公室" className={`${icon} max-sm:hidden`}>
          <Users className="size-4" strokeWidth={1.8} />
        </Link>
        <Link to={`/projects/${project.id}/settings`} aria-label="项目设置" className={icon}>
          <Settings className="size-4" strokeWidth={1.8} />
        </Link>
      </span>
    </header>
  );
}

/** Phones and narrow screens: active threads as chips above the chat. */
function ThreadChips({ pid, threads }: { pid: string; threads: ThreadView[] }) {
  const live = threads.filter((t) => !["done", "failed", "archived"].includes(t.status));
  if (live.length === 0) return null;
  return (
    <div className="scroll-thin flex gap-2 overflow-x-auto border-b border-[#EFEDE8] px-3 py-2.5 lg:hidden">
      {live.map((t) => (
        <Link key={t.id} to={`/projects/${pid}/threads/${t.no}`} className="flex h-8 shrink-0 items-center gap-1.5 rounded-full border bg-panel px-3 text-xs">
          <Dot color={markTone(t)} />#{t.no} {t.title}
        </Link>
      ))}
    </div>
  );
}

function CoordinatorChat({ pid, coordinator, threads, onOpenThread }: { pid: string; coordinator?: CoordinatorView; threads: ThreadView[]; onOpenThread: (no: number) => void }) {
  const cid = coordinator?.conversation_id;
  const questions = useQuestions(cid);
  const open = openForUser(questions.data);
  const empty = (
    <div className="flex flex-col items-center gap-2 py-16 text-center">
      <p className="text-[15px] font-medium">和项目统筹说说要做什么</p>
      <p className="max-w-md text-[13px] leading-6 text-muted-foreground">统筹只分派、不改代码：它会判断要做什么，开讨论串让组长带成员在仓库里完成，有结论回到这里告诉你。</p>
    </div>
  );
  const after = cid && open.length > 0 && (
    <div className="flex flex-col gap-3">
      {open.map((q) => (
        <WaitingCard key={q.id} cid={cid} pid={pid} question={q} variant="soft" />
      ))}
    </div>
  );
  if (!coordinator) return <Loading className="py-16" />;
  return <Chat cid={cid} threads={threads} onOpenThread={onOpenThread} empty={empty} after={after} speakerColor="#C8622A" />;
}

function CoordinatorComposer({ coordinator }: { coordinator?: CoordinatorView }) {
  const cid = coordinator?.conversation_id ?? "";
  const send = useAction((text: string) => conversations.send(cid, text), { invalidate: [keys.messages(cid)] });
  const stop = useAction(() => conversations.interrupt(cid), { success: "已让统筹停下" });
  const busy = coordinator?.employee.status && !["idle", "offline"].includes(coordinator.employee.status.state);
  const name = coordinator?.employee.name ?? "leader";
  const stopButton = busy ? (
    <button type="button" aria-label="停止" onClick={() => stop.mutate()} className="flex size-8 items-center justify-center rounded-lg border border-input bg-white">
      <Square className="size-3 fill-current" />
    </button>
  ) : null;
  return (
    <div className="shrink-0 bg-background px-3 pb-3 sm:px-6 sm:pb-5">
      <Composer
        className="mx-auto max-w-[700px]"
        label={`给 ${name} 发消息`}
        placeholder={`说需求或问进度，${name} 会开讨论串去做`}
        hint={`${name} 只分派、不改代码`}
        extra={stopButton}
        busy={send.isPending}
        onSend={(t) => send.mutateAsync(t)}
        id="boss"
      />
    </div>
  );
}

function PanelList({ pid, threads, loading, onOpen }: { pid: string; threads: ThreadView[]; loading: boolean; onOpen: (no: number) => void }) {
  const waiting = useWaiting(pid);
  const visible = threads.filter((t) => t.status !== "archived");
  return (
    <>
      <div className="flex h-14 shrink-0 items-center justify-between border-b border-[#EFEDE8] px-5">
        <span className="font-semibold">讨论串</span>
        <span className="text-xs text-muted-foreground">共 {visible.length} 个</span>
      </div>
      <div className="scroll-thin flex-1 overflow-y-auto px-5 pb-6 pt-3.5">
        {loading ? <Loading /> : <ThreadList threads={visible} onOpen={onOpen} waitingByThread={waitingIndex(waiting.data?.questions)} />}
      </div>
    </>
  );
}

function PanelDetail({ pid, no, threads, onBack }: { pid: string; no: number; threads: ThreadView[]; onBack: () => void }) {
  const thread = threads.find((t) => t.no === no);
  if (!thread) return <Loading className="py-16" />;
  return <ThreadPanel key={thread.id} thread={thread} onBack={onBack} fullHref={`/projects/${pid}/threads/${no}`} />;
}
