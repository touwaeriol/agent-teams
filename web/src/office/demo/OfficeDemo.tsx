// Dev-only demo (/dev/office under `npm run dev`): the Office board of the
// design canvas driven by simulated status events, with live and replay
// modes. /office/default.json is imported raw so Vite HMR hot-reloads it.
import { useEffect, useMemo, useState, type CSSProperties } from "react";
import layoutText from "../../../../office/default.json?raw";
import { Office } from "../Office";
import { ReplayBar } from "../ReplayBar";
import { STATE_LABELS } from "../overlay";
import { useReplay } from "../replay";
import { useHotLayout } from "../useLayout";
import type { OfficeLayout } from "../types";
import { DEMO_DATA, DEMO_THREADS, DEMO_TITLES, LOOP_MS, demoRoster, loopEvents, recordedHistory, type DemoEvent } from "./simulation";

const WHO_COLOR: Record<string, string> = { coordinator: "#B49CF2", lead: "#F2A65E", dev: "#7AA8FF", qa: "#8FD3A2", review: "#E6E3DC", decision: "#E6E3DC" };

const BOARDS: Record<string, { title: string; count: string; pct: string; items: { state: string; text: string; c: string }[] }> = {
  all: {
    title: "全项目 · 2 个讨论串", count: "小任务 10 / 19", pct: "53%",
    items: [
      { state: "可领", text: "#12 t8 计划与子任务展示", c: "#7AB8FF" },
      { state: "待审查", text: "#12 审批走 permission-prompt-tool", c: "#B49CF2" },
      { state: "待决策", text: "#12 卡片布局 A / B", c: "#E6E3DC" },
      { state: "等你", text: "#13 先用 CODEX_API_KEY 还是登录？", c: "#F2A65E" },
    ],
  },
  "12": {
    title: "#12 员工通信与审批", count: "7 / 13", pct: "54%",
    items: [
      { state: "可领", text: "t8 计划与子任务展示", c: "#7AB8FF" },
      { state: "进行中", text: "t6 提问 / 审批卡片 · dev-2", c: "#8FD3A2" },
      { state: "待审查", text: "t5 审批工具 · dev-1", c: "#B49CF2" },
      { state: "待决策", text: "t9 卡片布局 A / B → architect", c: "#E6E3DC" },
    ],
  },
  "13": {
    title: "#13 Codex 员工接入", count: "3 / 6", pct: "50%",
    items: [
      { state: "等你", text: "先用 CODEX_API_KEY 还是你去登录？", c: "#F2A65E" },
      { state: "进行中", text: "t3 跑 test_codex.py · qa-1", c: "#8FD3A2" },
    ],
  },
};

function hhmmss(ms: number): string {
  const d = new Date(ms);
  return [d.getHours(), d.getMinutes(), d.getSeconds()].map((n) => String(n).padStart(2, "0")).join(":");
}

/** Appends the looping script to a live event list as time passes. */
function useLiveEvents(start: number): DemoEvent[] {
  const [events, setEvents] = useState<DemoEvent[]>([]);
  useEffect(() => {
    let loop = 0;
    let queue = loopEvents(start, 0);
    const timer = setInterval(() => {
      const now = Date.now();
      const due = queue.filter((e) => (e.at as number) <= now);
      if (!due.length) return;
      queue = queue.filter((e) => (e.at as number) > now);
      if (!queue.length) {
        loop++;
        queue = loopEvents(start + loop * LOOP_MS, loop * 100);
      }
      setEvents((list) => [...list, ...due].slice(-400));
    }, 200);
    return () => clearInterval(timer);
  }, [start]);
  return events;
}

/** Variation used by the "hot reload" button: another floor and wall colour for the dev room. */
function variant(doc: OfficeLayout): OfficeLayout {
  const next = structuredClone(doc);
  const dev = next.rooms.find((r) => r.id === "dev")!;
  dev.floor = "loungeTiles";
  dev.wall = { ...dev.wall, color: "#7D8BA0" };
  dev.labelStyle = { ...dev.labelStyle, bg: "#7D8BA0" };
  return next;
}

export default function OfficeDemo() {
  const [start] = useState(() => Date.now());
  const [mode, setMode] = useState<"live" | "replay">("live");
  const [focus, setFocus] = useState<number | null>(null);
  const [selected, setSelected] = useState<string | null>(null);
  const [alt, setAlt] = useState(false);
  const baseDoc = useMemo(() => JSON.parse(layoutText) as OfficeLayout, []);
  const doc = useMemo(() => (alt ? variant(baseDoc) : baseDoc), [alt, baseDoc]);
  const hot = useHotLayout(doc);
  const roster = useMemo(() => demoRoster(start), [start]);
  const live = useLiveEvents(start);
  const history = useMemo(() => recordedHistory(start, 4), [start]);
  const replay = useReplay(history);
  const replaying = mode === "replay";
  const events = (replaying ? replay.events : live) as DemoEvent[];
  const replayRoster = useMemo(() => demoRoster(replay.start), [replay.start]);

  useEffect(() => {
    document.title = "办公室 · 演示";
    document.body.style.background = "#1E2027";
  }, []);

  return (
    <div style={page}>
      <section aria-label="办公室场景" style={{ flex: "999 1 820px", minWidth: 0, padding: 18, boxSizing: "border-box", display: "flex", flexDirection: "column", gap: 12 }}>
        <Header focus={focus} setFocus={setFocus} replaying={replaying} />
        {hot.error && <div style={{ color: "#F2A65E", fontSize: 12 }}>布局未生效：{hot.error}</div>}
        <Office
          key={mode}
          layout={hot.layout}
          employees={replaying ? replayRoster : roster}
          threads={DEMO_THREADS}
          events={events}
          focus={focus}
          onFocusThread={setFocus}
          selected={selected}
          onSelect={setSelected}
          data={DEMO_DATA}
          taskTitles={DEMO_TITLES}
          speed={replaying ? replay.speed : 1}
          now={replaying ? replay.time : undefined}
          seekKey={replaying ? replay.seekKey : undefined}
          onAction={(a) => console.info("office action", a)}
        />
      </section>
      <SidePanel
        mode={mode}
        setMode={setMode}
        focus={focus}
        events={events}
        selected={selected}
        roster={roster}
        layout={hot.layout}
        replay={replay}
        alt={alt}
        toggleAlt={() => setAlt((v) => !v)}
      />
    </div>
  );
}

const page: CSSProperties = {
  minHeight: "100vh", display: "flex", flexWrap: "wrap", background: "#1E2027", color: "#F0EDE6",
  fontFamily: "Inter, 'PingFang SC', 'Noto Sans SC', 'Microsoft YaHei', sans-serif", fontSize: 14,
};

function Header({ focus, setFocus, replaying }: { focus: number | null; setFocus: (n: number | null) => void; replaying: boolean }) {
  const scopes: [number | null, string][] = [[null, "全部讨论串"], [12, "#12"], [13, "#13"]];
  return (
    <header style={{ display: "flex", flexWrap: "wrap", justifyContent: "space-between", alignItems: "center", gap: 10 }}>
      <span style={{ display: "flex", alignItems: "center", gap: 10 }}>
        <span style={{ fontSize: 13, color: "#A8A49B" }}>agent team</span>
        <span style={{ color: "#5C5F6B" }}>/</span>
        <span style={{ fontWeight: 600, fontSize: 16 }}>办公室</span>
        <span style={{ display: "flex", alignItems: "center", gap: 6, fontSize: 12, color: replaying ? "#F2C14E" : "#8FD3A2" }}>
          <span style={{ width: 7, height: 7, borderRadius: "50%", background: replaying ? "#F2C14E" : "#5FD08A" }} />
          {replaying ? "回放" : "实时"}
        </span>
      </span>
      <div role="group" aria-label="显示范围" style={{ display: "flex", gap: 4, padding: 3, borderRadius: 10, background: "#2A2D36" }}>
        {scopes.map(([id, label]) => {
          const on = focus === id;
          return (
            <button
              key={label}
              type="button"
              aria-pressed={on}
              onClick={() => setFocus(id)}
              style={{ height: 30, padding: "0 12px", borderRadius: 8, border: "none", background: on ? "#F0EDE6" : "transparent", font: "inherit", fontSize: 13, fontWeight: on ? 600 : 400, color: on ? "#1F1E1C" : "#D8D4CB", cursor: "pointer" }}
            >
              {label}
            </button>
          );
        })}
      </div>
    </header>
  );
}

interface SideProps {
  mode: "live" | "replay";
  setMode: (m: "live" | "replay") => void;
  focus: number | null;
  events: DemoEvent[];
  selected: string | null;
  roster: ReturnType<typeof demoRoster>;
  layout: OfficeLayout;
  replay: ReturnType<typeof useReplay>;
  alt: boolean;
  toggleAlt: () => void;
}

function SidePanel(p: SideProps) {
  const board = BOARDS[p.focus === null ? "all" : String(p.focus)];
  const dim = (no: number | null) => (p.focus !== null && no !== null && no !== p.focus ? 0.3 : 1);
  const recent = [...p.events].reverse().slice(0, 12);
  const btn = (on: boolean): CSSProperties => ({
    height: 30, padding: "0 10px", borderRadius: 8, border: on ? "none" : "1px solid #4A4D59", background: on ? "#C8622A" : "transparent",
    color: on ? "#FFFFFF" : "#E6E3DC", font: "inherit", fontSize: 12, cursor: "pointer",
  });
  return (
    <aside aria-label="事件流" style={{ flex: "1 1 320px", maxWidth: 380, minWidth: 0, background: "#262931", borderLeft: "1px solid #353844", padding: 18, boxSizing: "border-box", display: "flex", flexDirection: "column", gap: 14, minHeight: "100vh" }}>
      <span style={{ display: "flex", justifyContent: "space-between", alignItems: "center" }}>
        <span style={{ fontWeight: 600, fontSize: 16 }}>调度看板</span>
        <span style={{ display: "flex", gap: 6 }}>
          <button type="button" style={btn(p.mode === "live")} onClick={() => p.setMode("live")}>实时</button>
          <button type="button" style={btn(p.mode === "replay")} onClick={() => p.setMode("replay")}>回放…</button>
        </span>
      </span>
      {p.mode === "replay" && <ReplayBar replay={p.replay} style={{ background: "#1E2027", border: "1px solid #353844", borderRadius: 10, padding: "10px 12px" }} />}
      <div style={{ background: "#1E2027", border: "1px solid #353844", borderRadius: 10, padding: "10px 12px", display: "flex", flexDirection: "column", gap: 6, fontSize: 12 }}>
        <span style={{ display: "flex", justifyContent: "space-between" }}>
          <span style={{ color: "#F2C14E" }}>{board.title}</span>
          <span style={{ color: "#A8A49B" }}>{board.count}</span>
        </span>
        <span style={{ height: 4, background: "#353844", display: "flex" }}>
          <span style={{ width: board.pct, background: "#5FD08A" }} />
        </span>
        {board.items.map((i) => (
          <span key={i.text} style={{ display: "flex", gap: 8, lineHeight: "18px" }}>
            <span style={{ color: i.c, minWidth: 40 }}>{i.state}</span>
            <span style={{ color: "#D8D4CB" }}>{i.text}</span>
          </span>
        ))}
      </div>
      <SelectedCard id={p.selected} roster={p.roster} events={p.events} />
      <span style={{ fontSize: 12, color: "#A8A49B" }}>点人物看他在做什么；点工作组门牌聚焦那个讨论串。</span>
      <div style={{ flex: 1, display: "flex", flexDirection: "column", gap: 10, overflowY: "auto", minHeight: 120 }}>
        {recent.length === 0 && <span style={{ fontSize: 12, color: "#7E8190" }}>等待事件…</span>}
        {recent.map((e) => {
          const who = typeof e.employee === "string" ? e.employee : e.employee.id;
          const role = p.roster.find((r) => r.id === who)?.role ?? "dev";
          return (
            <div key={e.seq} style={{ display: "flex", flexDirection: "column", gap: 2, fontSize: 12, lineHeight: "18px", opacity: dim(e.thread) }}>
              <span style={{ color: "#7E8190", fontFamily: "'JetBrains Mono', monospace" }}>{hhmmss(e.at as number)} · {e.thread ? `#${e.thread}` : "统筹"}</span>
              <span style={{ color: "#D8D4CB" }}>
                <span style={{ color: WHO_COLOR[role] ?? "#7AA8FF", fontFamily: "'JetBrains Mono', monospace" }}>{who}</span> {e.text}
              </span>
            </div>
          );
        })}
      </div>
      <div style={{ borderTop: "1px solid #353844", paddingTop: 12, display: "flex", flexWrap: "wrap", gap: "6px 12px", fontSize: 12, color: "#D8D4CB" }}>
        {(p.layout.legend ?? []).map((l) => (
          <span key={l.label} style={{ display: "flex", alignItems: "center", gap: 6 }}>
            <span style={{ width: 10, height: 10, background: l.color }} />
            {l.label}
          </span>
        ))}
      </div>
      <button type="button" onClick={p.toggleAlt} style={{ ...btn(false), alignSelf: "flex-start", fontSize: 11, color: "#A8A49B" }}>
        {p.alt ? "还原布局（热更新）" : "换开发区地板（热更新）"}
      </button>
    </aside>
  );
}

function SelectedCard({ id, roster, events }: { id: string | null; roster: ReturnType<typeof demoRoster>; events: DemoEvent[] }) {
  if (!id) return null;
  const e = roster.find((r) => r.id === id);
  if (!e) return null;
  const last = [...events].reverse().find((ev) => ev.employee === id);
  const status = last?.status ?? e.status;
  return (
    <div style={{ background: "#1E2027", border: "1px solid #4A4D59", borderRadius: 10, padding: "10px 12px", fontSize: 12, display: "flex", flexDirection: "column", gap: 4 }}>
      <span style={{ display: "flex", justifyContent: "space-between" }}>
        <span style={{ fontWeight: 600, fontSize: 13 }}>{e.name}</span>
        <span style={{ color: "#A8A49B" }}>{e.thread_no ? `#${e.thread_no}` : "统筹"} · {e.role}</span>
      </span>
      <span style={{ color: "#8FD3A2" }}>{STATE_LABELS[status?.state ?? "idle"]}{status?.bubble ? `：${status.bubble}` : ""}</span>
      {last && <span style={{ color: "#7E8190" }}>{hhmmss(last.at as number)} {last.text}</span>}
    </div>
  );
}
