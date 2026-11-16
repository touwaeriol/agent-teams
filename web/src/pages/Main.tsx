// Main board: chat with the project coordinator in the middle, the thread
// list (→ thread detail) on the right. Phones get thread chips instead of the panel.
import { Clock, Home, Library, MoreHorizontal, PanelRight, Settings, Square, Users } from "lucide-react";
import { useState } from "react";
import { Link, Navigate, useNavigate, useParams, useSearchParams } from "react-router-dom";
import { conversations } from "@/api/actions";
import { useAction } from "@/api/mutations";
import { keys, useCoordinator, useMe, useProject, useQuestions, useThreads, useWaiting } from "@/api/queries";
import type { CoordinatorView, ProjectView, QuestionView, ThreadView } from "@/api/views";
import { Composer, Dot, StateMark } from "@/components/app/bits";
import { useAttachments } from "@/components/app/attachments";
import { WaitingCard } from "@/components/app/cards";
import { Chat } from "@/components/app/Chat";
import { ResizeHandle, usePanelWidth } from "@/components/app/ResizeHandle";
import { ThreadDetail, type ThreadViewId } from "@/components/app/ThreadDetail";
import { LibraryBrowser } from "@/components/app/Library";
import { SchedulesDialog } from "@/components/app/Schedules";
import { ThreadList } from "@/components/app/thread";
import { EmptyState, errorText, IconButton, IconLink, Loading } from "@/components/common";
import { ProjectSwitcher } from "@/components/shell/ProjectSwitcher";
import { NavButton } from "@/components/shell/Shell";
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuSeparator, DropdownMenuTrigger } from "@/components/ui/dropdown-menu";
import { sectionOf } from "@/lib/projectSettings";
import { chatQuestions } from "@/lib/ask";
import { markTone, threadLook } from "@/lib/present";
import { ProjectSettingsDialog } from "./ProjectSettings";

export function useProjectId(): string {
  return useParams().pid ?? "";
}

/** Waiting questions keyed by thread number (first open one wins), for list lines. */
export function waitingIndex(questions: QuestionView[] | undefined): Map<number, QuestionView> {
  const map = new Map<number, QuestionView>();
  for (const q of questions ?? []) if (q.thread_no !== null && !map.has(q.thread_no)) map.set(q.thread_no, q);
  return map;
}

/** What the right-hand side shows: the thread panel (default), the library, or nothing. */
export type SidePanel = "threads" | "library" | "none";

export function sidePanelOf(value: string | null): SidePanel {
  return value === "library" || value === "none" ? value : "threads";
}

/** Where a thread opens: /projects/:pid/threads/:no, plus /tasks, /deliverables or /members when that view is open over the chat. */
export function threadPath(pid: string, no: number, view?: ThreadViewId | null): string {
  return `/projects/${pid}/threads/${no}${view ? `/${view}` : ""}`;
}

export function MainPage({ settings = false, openNo = null, view = null }: { settings?: boolean; openNo?: number | null; view?: ThreadViewId | null }) {
  const pid = useProjectId();
  const { section } = useParams();
  const project = useProject(pid);
  const coordinator = useCoordinator(pid);
  const threads = useThreads(pid);
  const me = useMe();
  const [width, setWidth] = usePanelWidth(me.data?.id ?? "anon");
  const [params, setParams] = useSearchParams();
  const navigate = useNavigate();
  const panel = sidePanelOf(params.get("panel"));
  const setPanel = (p: SidePanel) => setParams(p === "threads" ? {} : { panel: p });
  const openThread = (no: number) => navigate(threadPath(pid, no));
  const legacyNo = Number(params.get("thread"));
  if (legacyNo) return <Navigate to={threadPath(pid, legacyNo)} replace />;
  if (project.error) return <EmptyState title="找不到这个项目" description={errorText(project.error)} />;
  if (!project.data) return <Loading className="h-full" />;
  const list = threads.data ?? [];
  return (
    <div className="flex h-full min-h-0 flex-1">
      <main className="flex min-h-0 min-w-0 flex-1 flex-col">
        <MainHeader project={project.data} coordinator={coordinator.data} threads={list} panel={panel} onPanel={setPanel} />
        <ThreadChips pid={pid} threads={list} />
        <div className="flex-1 overflow-y-auto">
          <div className="mx-auto flex max-w-[720px] flex-col px-4 pb-6 pt-4 sm:px-6 sm:pt-8">
            <CoordinatorChat pid={pid} coordinator={coordinator.data} threads={list} onOpenThread={openThread} />
          </div>
        </div>
        <CoordinatorComposer pid={pid} coordinator={coordinator.data} />
      </main>
      {panel === "threads" && <ResizeHandle width={width} onChange={setWidth} label="调整讨论串宽度" />}
      {panel === "threads" && (
        <aside aria-label="讨论串" style={{ width }} className="hidden h-full shrink-0 flex-col border-l bg-panel lg:flex">
          {openNo ? (
            <PanelDetail
              pid={pid}
              no={openNo}
              view={view}
              threads={list}
              loading={threads.isLoading}
              onView={(v) => navigate(threadPath(pid, openNo, v), { replace: true })}
              onBack={() => navigate(`/projects/${pid}`)}
              onClose={() => navigate(`/projects/${pid}?panel=none`)}
            />
          ) : (
            <PanelList pid={pid} threads={list} loading={threads.isLoading} onOpen={openThread} />
          )}
        </aside>
      )}
      {panel === "library" && (
        <aside aria-label="资料库" className="fixed inset-0 z-30 flex flex-col bg-panel lg:static lg:z-auto lg:h-full lg:w-[min(820px,56vw)] lg:min-w-[420px] lg:shrink-0 lg:border-l">
          <LibraryBrowser pid={pid} variant="panel" onClose={() => setPanel("threads")} />
        </aside>
      )}
      {settings && (
        <ProjectSettingsDialog
          project={project.data}
          section={sectionOf(section)}
          onSection={(s) => navigate(`/projects/${pid}/settings/${s}`, { replace: true })}
          onClose={() => navigate(`/projects/${pid}`)}
        />
      )}
    </div>
  );
}

function coordinatorState(c: CoordinatorView | undefined): { tone: string; text: string } {
  const name = c?.employee.name ?? "leader";
  if (!c?.employee.node) return { tone: "#B7B3AA", text: `${name} 待命` };
  if (!c.employee.node.online) return { tone: "#B42318", text: `${name} 离线` };
  return { tone: "#2E7D4F", text: `${name} 在线` };
}

function MainHeader({ project, coordinator, threads, panel, onPanel }: { project: ProjectView; coordinator?: CoordinatorView; threads: ThreadView[]; panel: SidePanel; onPanel: (p: SidePanel) => void }) {
  const navigate = useNavigate();
  const [schedules, setSchedules] = useState(false);
  const state = coordinatorState(coordinator);
  const waiting = threads.reduce((n, t) => n + t.counts.waiting + (t.status === "review" ? 1 : 0), 0);
  const firstWaiting = threads.find((t) => t.counts.waiting > 0 || t.status === "review");
  return (
    <header className="flex h-14 shrink-0 items-center justify-between gap-2 border-b bg-background px-2 sm:px-4 max-md:bg-panel">
      <div className="flex min-w-0 items-center gap-1">
        <NavButton />
        <ProjectSwitcher project={project} />
        <span className={`ml-1 hidden items-center gap-1.5 whitespace-nowrap text-xs text-muted-foreground ${panel === "library" ? "" : "xl:flex"}`} title={project.repos?.map((r) => r.repo).join(" · ")}>
          <Dot color={state.tone} />
          {state.text}
        </span>
      </div>
      <span className="flex shrink-0 items-center gap-0.5">
        {waiting > 0 && firstWaiting && (
          <Link to={`/projects/${project.id}/threads/${firstWaiting.no}`} className="mr-1.5 flex h-8 items-center rounded-full bg-[#FFF4EC] px-2.5 text-xs text-[#B4530B] lg:hidden">
            等你 {waiting}
          </Link>
        )}
        <IconButton label="资料库" active={panel === "library"} onClick={() => onPanel(panel === "library" ? "threads" : "library")}>
          <Library strokeWidth={1.8} />
        </IconButton>
        <IconButton label={panel === "threads" ? "收起讨论串" : "显示讨论串"} active={panel === "threads"} onClick={() => onPanel(panel === "threads" ? "none" : "threads")} className="max-lg:hidden">
          <PanelRight strokeWidth={1.8} />
        </IconButton>
        <IconLink label="办公室" to={`/projects/${project.id}/office`} className="max-sm:hidden">
          <Users strokeWidth={1.8} />
        </IconLink>
        <DropdownMenu>
          <DropdownMenuTrigger asChild>
            <IconButton label="更多">
              <MoreHorizontal strokeWidth={1.8} />
            </IconButton>
          </DropdownMenuTrigger>
          <DropdownMenuContent align="end" className="w-44" onCloseAutoFocus={(e) => e.preventDefault()}>
            <DropdownMenuItem onSelect={() => navigate(`/projects/${project.id}/overview`)}>
              <Home /> 项目首页
            </DropdownMenuItem>
            <DropdownMenuItem onSelect={() => setSchedules(true)}>
              <Clock /> 定时任务
            </DropdownMenuItem>
            <DropdownMenuSeparator />
            <DropdownMenuItem onSelect={() => navigate(`/projects/${project.id}/settings`)}>
              <Settings /> 设置
            </DropdownMenuItem>
          </DropdownMenuContent>
        </DropdownMenu>
      </span>
      <SchedulesDialog pid={project.id} open={schedules} onOpenChange={setSchedules} />
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
          <StateMark look={{ tone: markTone(t), spin: threadLook(t).spin }} />#{t.no} {t.title}
        </Link>
      ))}
    </div>
  );
}

function CoordinatorChat({ pid, coordinator, threads, onOpenThread }: { pid: string; coordinator?: CoordinatorView; threads: ThreadView[]; onOpenThread: (no: number) => void }) {
  const cid = coordinator?.conversation_id;
  const questions = useQuestions(cid);
  const inserts = cid
    ? chatQuestions(questions.data).map((q) => ({ key: q.id, at: q.created_at, node: <WaitingCard cid={cid} pid={pid} question={q} variant="soft" /> }))
    : [];
  const empty = (
    <div className="flex flex-col items-center gap-2 py-16 text-center">
      <p className="text-[15px] font-medium">和项目统筹说说要做什么</p>
      <p className="max-w-md text-[13px] leading-6 text-muted-foreground">统筹只分派、不改代码：它会判断要做什么，开讨论串让组长带成员在仓库里完成，有结论回到这里告诉你。</p>
    </div>
  );
  if (!coordinator) return <Loading className="py-16" />;
  return <Chat cid={cid} pid={pid} threads={threads} onOpenThread={onOpenThread} empty={empty} inserts={inserts} speakerColor="#C8622A" />;
}

function CoordinatorComposer({ pid, coordinator }: { pid: string; coordinator?: CoordinatorView }) {
  const cid = coordinator?.conversation_id ?? "";
  const attachments = useAttachments(pid);
  const send = useAction((v: { text: string; images: string[] }) => conversations.send(cid, v.text, false, v.images), { invalidate: [keys.messages(cid)] });
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
        placeholder={`说需求或问进度，${name} 会开讨论串去做；可以粘贴或拖入图片`}
        hint={`${name} 只分派、不改代码`}
        extra={stopButton}
        busy={send.isPending}
        attachments={attachments}
        onSend={(text, images) => send.mutateAsync({ text, images })}
        id="boss"
        draftKey={cid ? `draft:${cid}` : undefined}
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
        <nav aria-label="位置" className="font-semibold">
          讨论串
        </nav>
        <span className="text-xs text-muted-foreground">共 {visible.length} 个</span>
      </div>
      <div className="scroll-thin flex-1 overflow-y-auto px-5 pb-6 pt-3.5">
        {loading ? <Loading /> : <ThreadList threads={threads} onOpen={onOpen} waitingByThread={waitingIndex(waiting.data?.questions)} menus />}
      </div>
    </>
  );
}

interface PanelDetailProps {
  pid: string;
  no: number;
  view: ThreadViewId | null;
  threads: ThreadView[];
  loading: boolean;
  onView: (view: ThreadViewId | null) => void;
  onBack: () => void;
  onClose: () => void;
}

function PanelDetail({ pid, no, view, threads, loading, onView, onBack, onClose }: PanelDetailProps) {
  const thread = threads.find((t) => t.no === no);
  if (loading) return <Loading className="py-16" />;
  if (!thread)
    return (
      <EmptyState
        title={`找不到讨论串 #${no}`}
        description="它可能已被删除。"
        action={
          <button type="button" onClick={onBack} className="text-[13px] underline-offset-2 hover:underline">
            回到讨论串列表
          </button>
        }
      />
    );
  const full = `/projects/${pid}/threads/${no}/full`;
  return (
    <ThreadDetail
      key={thread.id}
      pid={pid}
      thread={thread}
      view={view}
      onView={onView}
      onBack={onBack}
      onClose={onClose}
      fullHref={full}
      expand={{ to: full, label: "全屏打开" }}
    />
  );
}
