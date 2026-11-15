// Office board: the pixel office (<Office/> from web/src/office) driven by the
// office snapshot plus live `status` frames from the project event stream,
// with the dark “调度看板” side panel, scope switch, employee card and replay.
import { useQuery } from "@tanstack/react-query";
import { useEffect, useMemo, useRef, useState, type CSSProperties } from "react";
import { Link, useNavigate } from "react-router-dom";
import { client, unwrap } from "@/api/client";
import { layoutTickKey, statusLogKey, type StatusFrame } from "@/api/events";
import { useProjectEvents } from "@/api/sse";
import { useEmployee, useOfficeSnapshot, useProject, useWaiting } from "@/api/queries";
import type { OfficeSnapshot, QuestionView, ThreadView } from "@/api/views";
import { EmptyState, errorText, Loading } from "@/components/common";
import { Office, ReplayBar, useRemoteLayout, useReplay, type OfficeAction, type OfficeLayout, type OfficeStatusEvent } from "@/office";
import { STATE_LABELS } from "@/office/overlay";
import { boardSummary, replayEvents, toOfficeEmployees, toOfficeEvents, toOfficeThreads, toWidgetData } from "@/lib/office";
import { memberDoing } from "@/lib/present";
import { useProjectId } from "./Main";

const WHO_COLOR: Record<string, string> = { coordinator: "#B49CF2", lead: "#F2A65E", dev: "#7AA8FF", qa: "#8FD3A2" };
const DARK = { page: "#1E2027", panel: "#262931", line: "#353844", soft: "#A8A49B", text: "#D8D4CB" };

function hhmmss(at: string | number): string {
  const d = new Date(at);
  return [d.getHours(), d.getMinutes(), d.getSeconds()].map((n) => String(n).padStart(2, "0")).join(":");
}

export function OfficePage() {
  const pid = useProjectId();
  const project = useProject(pid);
  const snapshot = useOfficeSnapshot(pid);
  // The office is outside the project shell (full-bleed dark page), so it opens its own stream.
  useProjectEvents(pid);
  useEffect(() => {
    const prev = document.body.style.background;
    document.body.style.background = DARK.page;
    return () => {
      document.body.style.background = prev;
    };
  }, []);
  if (snapshot.error) return <EmptyState title="办公室打不开" description={errorText(snapshot.error)} />;
  if (!snapshot.data || !project.data) return <div className="h-full" style={{ background: DARK.page }}><Loading className="h-full" /></div>;
  return <OfficeView pid={pid} name={project.data.name} snapshot={snapshot.data} layoutId={snapshot.data.layout_id || project.data.office_layout} />;
}

function useLiveEvents(pid: string, after: number): OfficeStatusEvent[] {
  const log = useQuery<StatusFrame[]>({ queryKey: statusLogKey(pid), queryFn: () => [], staleTime: Infinity, initialData: [] });
  return useMemo(() => toOfficeEvents(log.data ?? [], after), [log.data, after]);
}

function useLayoutTick(pid: string): number {
  return useQuery<number>({ queryKey: layoutTickKey(pid), queryFn: () => 0, staleTime: Infinity, initialData: 0 }).data ?? 0;
}

function OfficeView({ pid, name, snapshot, layoutId }: { pid: string; name: string; snapshot: OfficeSnapshot; layoutId: string }) {
  const navigate = useNavigate();
  const after = useRef(snapshot.last_event_id).current;
  const live = useLiveEvents(pid, after);
  const remote = useRemoteLayout(layoutId);
  const tick = useLayoutTick(pid);
  const reload = remote.reload;
  useEffect(() => {
    if (tick) reload();
  }, [tick, reload]);
  const [focus, setFocus] = useState<number | null>(null);
  const [selected, setSelected] = useState<string | null>(null);
  const [replaying, setReplaying] = useState(false);
  const threads = snapshot.threads.filter((t) => !["done", "archived", "failed"].includes(t.status));
  const onAction = (a: OfficeAction) => {
    if (a.type === "openThread") navigate(`/projects/${pid}/threads/${a.threadNo}`);
    if (a.type === "openTaskPool" && focus !== null) navigate(`/projects/${pid}/threads/${focus}`);
    if (a.type === "openNode") navigate("/settings/nodes");
  };
  const common = { layout: remote.layout, threads: toOfficeThreads(threads), focus, onFocusThread: setFocus, selected, onSelect: setSelected, data: toWidgetData(snapshot, focus), onAction };
  return (
    <div className="flex min-h-full flex-wrap text-[#F0EDE6]" style={{ background: DARK.page }}>
      <section aria-label="办公室场景" className="flex min-w-0 flex-[999_1_820px] flex-col gap-3 p-[18px]">
        <Header pid={pid} name={name} threads={threads} focus={focus} setFocus={setFocus} replaying={replaying} />
        {remote.error && <div className="text-xs text-[#F2A65E]">布局未生效：{remote.error}</div>}
        {replaying ? (
          <ReplayOffice pid={pid} common={common} />
        ) : (
          <Office {...common} employees={toOfficeEmployees(snapshot)} events={live} />
        )}
      </section>
      <SidePanel pid={pid} snapshot={snapshot} threads={threads} focus={focus} events={live} selected={selected} layout={remote.layout} replaying={replaying} setReplaying={setReplaying} />
    </div>
  );
}

type Common = Omit<React.ComponentProps<typeof Office>, "employees" | "events">;

/** The last hour from /office/replay, played back with the same renderer. */
function ReplayOffice({ pid, common }: { pid: string; common: Common }) {
  const [range] = useState(() => {
    const to = new Date();
    return { from: new Date(to.getTime() - 3600_000).toISOString(), to: to.toISOString() };
  });
  const replay = useQuery({ queryKey: ["p", pid, "replay", range.from], queryFn: () => unwrap(client.GET("/api/projects/{pid}/office/replay", { params: { path: { pid }, query: range } })) });
  const events = useMemo(() => replayEvents(replay.data?.events ?? []), [replay.data]);
  const r = useReplay(events, Date.parse(range.from), Date.parse(range.to));
  if (replay.error) return <div className="text-sm text-[#F2A65E]">回放数据取不到：{errorText(replay.error)}</div>;
  if (!replay.data) return <Loading />;
  return (
    <>
      <ReplayBar replay={r} style={{ background: DARK.panel, border: `1px solid ${DARK.line}`, borderRadius: 10, padding: "10px 12px" }} />
      <Office {...common} employees={toOfficeEmployees(replay.data.snapshot)} events={r.events} now={r.time} speed={r.speed} seekKey={r.seekKey} />
    </>
  );
}

function Header({ pid, name, threads, focus, setFocus, replaying }: { pid: string; name: string; threads: ThreadView[]; focus: number | null; setFocus: (n: number | null) => void; replaying: boolean }) {
  const scopes: [number | null, string][] = [[null, "全部讨论串"], ...threads.map((t) => [t.no, `#${t.no}`] as [number, string])];
  return (
    <header className="flex flex-wrap items-center justify-between gap-2.5">
      <span className="flex items-center gap-2.5">
        <Link to={`/projects/${pid}`} className="text-[13px] text-[#A8A49B] hover:text-white">
          {name}
        </Link>
        <span className="text-[#5C5F6B]">/</span>
        <span className="text-base font-semibold">办公室</span>
        <span className="flex items-center gap-1.5 text-xs" style={{ color: replaying ? "#F2C14E" : "#8FD3A2" }}>
          <span className="size-[7px] rounded-full" style={{ background: replaying ? "#F2C14E" : "#5FD08A" }} />
          {replaying ? "回放" : "实时"}
        </span>
      </span>
      <div role="group" aria-label="显示范围" className="flex flex-wrap gap-1 rounded-[10px] bg-[#2A2D36] p-[3px]">
        {scopes.map(([id, label]) => (
          <button
            key={label}
            type="button"
            aria-pressed={focus === id}
            onClick={() => setFocus(id)}
            className="h-[30px] rounded-lg px-3 text-[13px]"
            style={{ background: focus === id ? "#F0EDE6" : "transparent", color: focus === id ? "#1F1E1C" : DARK.text, fontWeight: focus === id ? 600 : 400 }}
          >
            {label}
          </button>
        ))}
      </div>
    </header>
  );
}

interface BoardItem {
  state: string;
  text: string;
  color: string;
}

export function boardItems(threads: ThreadView[], questions: QuestionView[], claimable: number, focus: number | null): BoardItem[] {
  const inScope = (no: number | null) => focus === null || no === focus;
  const items: BoardItem[] = [];
  if (claimable && focus === null) items.push({ state: "可领", text: `任务板上 ${claimable} 个小任务`, color: "#7AB8FF" });
  questions.filter((q) => inScope(q.thread_no)).forEach((q) => items.push({ state: "等你", text: `${q.thread_no ? `#${q.thread_no} ` : ""}${q.kind === "approval" ? `${q.asker} 请求权限` : q.text}`, color: "#F2A65E" }));
  threads.filter((t) => t.status === "review" && inScope(t.no)).forEach((t) => items.push({ state: "待验收", text: `#${t.no} ${t.title}`, color: "#B49CF2" }));
  threads.filter((t) => t.status === "queued" && inScope(t.no)).forEach((t) => items.push({ state: "排队", text: `#${t.no} ${t.title}`, color: "#E6E3DC" }));
  return items.slice(0, 6);
}

function SidePanel(p: { pid: string; snapshot: OfficeSnapshot; threads: ThreadView[]; focus: number | null; events: OfficeStatusEvent[]; selected: string | null; layout: OfficeLayout; replaying: boolean; setReplaying: (v: boolean) => void }) {
  const waiting = useWaiting(p.pid);
  const board = boardSummary(p.threads, p.focus);
  const items = boardItems(p.threads, waiting.data?.questions ?? [], p.snapshot.board.claimable, p.focus);
  const roster = new Map(p.snapshot.employees.map((e) => [e.id, e]));
  const recent = [...p.events].reverse().filter((e) => p.focus === null || roster.get(String(e.employee))?.thread_no === p.focus).slice(0, 14);
  const btn = (on: boolean): CSSProperties => ({ height: 30, padding: "0 10px", borderRadius: 8, border: on ? "none" : "1px solid #4A4D59", background: on ? "#C8622A" : "transparent", color: on ? "#FFFFFF" : "#E6E3DC", fontSize: 12 });
  return (
    <aside aria-label="事件流" className="flex min-h-screen min-w-0 max-w-[380px] flex-[1_1_320px] flex-col gap-3.5 border-l p-[18px]" style={{ background: DARK.panel, borderColor: DARK.line }}>
      <span className="flex items-center justify-between">
        <span className="text-base font-semibold">调度看板</span>
        <span className="flex gap-1.5">
          <button type="button" style={btn(!p.replaying)} onClick={() => p.setReplaying(false)}>
            实时
          </button>
          <button type="button" style={btn(p.replaying)} onClick={() => p.setReplaying(true)}>
            回放…
          </button>
        </span>
      </span>
      <div className="flex flex-col gap-1.5 rounded-[10px] border px-3 py-2.5 text-xs" style={{ background: DARK.page, borderColor: DARK.line }}>
        <span className="flex justify-between">
          <span className="text-[#F2C14E]">{board.title}</span>
          <span style={{ color: DARK.soft }}>{board.count}</span>
        </span>
        <span className="flex h-1" style={{ background: DARK.line }}>
          <span style={{ width: board.pct, background: "#5FD08A" }} />
        </span>
        {items.length === 0 && <span style={{ color: DARK.soft }}>现在没有等待中的事</span>}
        {items.map((i) => (
          <span key={i.state + i.text} className="flex gap-2 leading-[18px]">
            <span className="min-w-10" style={{ color: i.color }}>
              {i.state}
            </span>
            <span style={{ color: DARK.text }}>{i.text}</span>
          </span>
        ))}
      </div>
      <EmployeeCard id={p.selected} />
      <span className="text-xs" style={{ color: DARK.soft }}>
        点人物看他在做什么；点工作组门牌聚焦那个讨论串。
      </span>
      <div className="flex min-h-[120px] flex-1 flex-col gap-2.5 overflow-y-auto">
        {recent.length === 0 && <span className="text-xs text-[#7E8190]">等待事件…</span>}
        {recent.map((e) => {
          const who = roster.get(String(e.employee));
          return (
            <div key={e.seq} className="flex flex-col gap-0.5 text-xs leading-[18px]">
              <span className="font-mono text-[#7E8190]">
                {hhmmss(e.at)} · {who?.thread_no ? `#${who.thread_no}` : "统筹"}
              </span>
              <span style={{ color: DARK.text }}>
                <span className="font-mono" style={{ color: WHO_COLOR[who?.tier === "member" ? who.role : who?.tier ?? ""] ?? "#7AA8FF" }}>
                  {who?.name ?? String(e.employee)}
                </span>{" "}
                {e.status.bubble || STATE_LABELS[e.status.state] || e.status.state}
              </span>
            </div>
          );
        })}
      </div>
      <div className="flex flex-wrap gap-x-3 gap-y-1.5 border-t pt-3 text-xs" style={{ borderColor: DARK.line, color: DARK.text }}>
        {(p.layout.legend ?? []).map((l) => (
          <span key={l.label} className="flex items-center gap-1.5">
            <span className="size-2.5" style={{ background: l.color }} />
            {l.label}
          </span>
        ))}
      </div>
    </aside>
  );
}

function EmployeeCard({ id }: { id: string | null }) {
  const card = useEmployee(id);
  if (!id) return null;
  const e = card.data;
  return (
    <div className="flex flex-col gap-1 rounded-[10px] border border-[#4A4D59] px-3 py-2.5 text-xs" style={{ background: DARK.page }}>
      {!e ? (
        <span style={{ color: DARK.soft }}>{card.error ? errorText(card.error) : "加载中…"}</span>
      ) : (
        <>
          <span className="flex justify-between">
            <span className="text-[13px] font-semibold">{e.name}</span>
            <span style={{ color: DARK.soft }}>
              {e.title || e.role}
              {e.node ? ` · ${e.node.name}` : ""}
            </span>
          </span>
          <span className="text-[#8FD3A2]">{memberDoing(e)}</span>
          {e.current_task && <span style={{ color: DARK.text }}>在做：{e.current_task.instruction.split("\n")[0]}</span>}
          {e.recent_activity.slice(0, 3).map((a, i) => (
            <span key={i} className="text-[#7E8190]">
              {hhmmss(a.at)} {a.text}
            </span>
          ))}
        </>
      )}
    </div>
  );
}
