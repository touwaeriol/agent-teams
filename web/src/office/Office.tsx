// <Office/>: the pixel-art project office (docs/v2/office-layout.md).
// Props are documented on OfficeProps; see README.md in this folder.
import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState, type CSSProperties } from "react";
import { colorOr } from "./color";
import { compileScene, type Scene } from "./compile";
import { Director, type ActorView } from "./director";
import { resolveLook, type ResolvedLook } from "./looks";
import { OfficeOverlay, STATE_LABELS, type OfficeAction } from "./overlay";
import { cursorOf, diffFeed, eventEmployee, foldStatuses, type FeedCursor } from "./replay";
import { drawDynamic, drawStatic, prepareCanvas, setImageLoadHandler } from "./render";
import type { EmployeeStatus, OfficeEmployee, OfficeLayout, OfficeStatusEvent, OfficeThread, OfficeWidgetData } from "./types";

export interface OfficeProps {
  /** A validated layout (see load.ts / useHotLayout). A new object recompiles the scene in place: people keep their state and position. */
  layout: OfficeLayout;
  /** Roster: EmployeeBrief + thread_no. `status` is the snapshot status; later changes come from `events` (or a changed `status`). */
  employees: OfficeEmployee[];
  /** Threads (ThreadView subset); those in `layout.threadPods.layout.show` get a work area. */
  threads: OfficeThread[];
  /** Status events in seq order. Appending walks people to their new place; any other change (e.g. a replay scrub backwards) rebuilds instantly. */
  events?: OfficeStatusEvent[];
  /** Thread number to focus (others dim to `focus.dimOpacity`), or null for all threads. */
  focus?: number | null;
  /** Called when a person is clicked (null when the empty floor is clicked). */
  onSelect?: (employeeId: string | null) => void;
  /** Controlled selection; omit to let the office track it. */
  selected?: string | null;
  /** Called when a pod plate is clicked (layout `focus.clickPlate = focusThread`), with the new focus. */
  onFocusThread?: (threadNo: number | null) => void;
  /** Task board, racks, merge queue and (`clickPlate = openThread`) plate clicks. */
  onAction?: (action: OfficeAction) => void;
  /** Widget data: task board count, nodes, merge queue, CI checks. */
  data?: OfficeWidgetData;
  /** task id → title for sequence bubbles ("领取 {taskId} {taskTitle}"). */
  taskTitles?: Record<string, string>;
  /** Replay: walking speed multiplier (1, 4, 16). */
  speed?: number;
  /** Replay: the current replay time (epoch ms) for desk timers and bubble expiry; defaults to the wall clock. */
  now?: number;
  /** Replay: change it on every scrub so everyone teleports to the state at the new time. */
  seekKey?: number | string;
  className?: string;
  style?: CSSProperties;
}

const SCALES = [4, 3, 2, 1];

/** Integer or 0.5 scale for the available width (office-layout.md §1); 0 = too narrow, use the list view. */
export function pickScale(available: number, sceneWidth: number): number {
  const ratio = available / sceneWidth;
  const whole = SCALES.find((s) => s <= ratio);
  if (whole) return whole;
  if (ratio >= 0.75) return 1;
  return ratio >= 0.5 ? 0.5 : 0;
}

function signature<T>(list: T[], pick: (x: T) => unknown): string {
  return JSON.stringify(list.map(pick));
}

function useReducedMotion(): boolean {
  const query = typeof window !== "undefined" && window.matchMedia ? window.matchMedia("(prefers-reduced-motion: reduce)") : undefined;
  const [reduced, setReduced] = useState(Boolean(query?.matches));
  useEffect(() => {
    if (!query) return;
    const on = () => setReduced(query.matches);
    query.addEventListener("change", on);
    return () => query.removeEventListener("change", on);
  }, [query]);
  return reduced;
}

function useWidth(ref: React.RefObject<HTMLElement | null>): number {
  const [w, setW] = useState(0);
  useLayoutEffect(() => {
    const el = ref.current;
    if (!el) return;
    setW(el.clientWidth);
    const ro = new ResizeObserver(() => setW(el.clientWidth));
    ro.observe(el);
    return () => ro.disconnect();
  }, [ref]);
  return w;
}

const loadedFonts = new Set<string>();

function useGoogleFonts(families: string[] | undefined): void {
  useEffect(() => {
    for (const f of families ?? []) {
      if (loadedFonts.has(f)) continue;
      loadedFonts.add(f);
      const link = document.createElement("link");
      link.rel = "stylesheet";
      link.href = `https://fonts.googleapis.com/css2?family=${encodeURIComponent(f).replace(/%20/g, "+")}&display=swap`;
      document.head.appendChild(link);
    }
  }, [families]);
}

function hasLiveAnimation(layout: OfficeLayout, actors: ActorView[], data: OfficeWidgetData): boolean {
  const anims = layout.characters.person.animations;
  const animated = actors.some((a) => !a.hidden && ((anims[a.anim]?.frames.length ?? 1) > 1 || anims[a.anim]?.bob || a.overlay));
  const racks = (layout.widgets ?? []).some((w) => w.type === "nodeRacks" && Object.values(w.slotColors ?? {}).some((c) => c?.blink));
  return animated || (racks && Boolean(data.nodes?.length));
}

export function Office(props: OfficeProps) {
  const { layout, employees, threads, events = EMPTY, focus = null, data = EMPTY_DATA, speed = 1 } = props;
  const [page, setPage] = useState(0);
  const [localSel, setLocalSel] = useState<string | null>(null);
  const selected = props.selected !== undefined ? props.selected : localSel;
  const reducedMotion = useReducedMotion();
  useGoogleFonts(layout.fonts?.googleFonts);

  const rosterKey = signature(employees, (e) => [e.id, e.name, e.thread_no, e.tier, e.role, e.sort, e.instance]);
  const threadsKey = signature(threads, (t) => [t.no, t.title, t.status, t.counts?.main_done, t.counts?.main_total, t.updated_at]);
  // eslint-disable-next-line react-hooks/exhaustive-deps
  const scene = useMemo(() => compileScene(layout, { threads, employees, focus, page }), [layout, rosterKey, threadsKey, focus, page]);
  const looksKey = signature(employees, (e) => [e.id, e.name, e.tier, e.role, e.look, e.instance]);
  // eslint-disable-next-line react-hooks/exhaustive-deps
  const looks = useMemo(() => new Map<string, ResolvedLook>(employees.map((e) => [e.id, resolveLook(layout, e)])), [layout, looksKey]);

  const nowRef = useRef<number | undefined>(props.now);
  nowRef.current = props.now;
  const titlesRef = useRef(props.taskTitles);
  titlesRef.current = props.taskTitles;
  const directorRef = useRef<Director | null>(null);
  if (!directorRef.current) {
    directorRef.current = new Director(scene, {
      now: () => nowRef.current ?? Date.now(),
      clock: () => performance.now(),
      taskTitle: (id) => titlesRef.current?.[id],
    });
    directorRef.current.setEmployees(employees);
  }
  const director = directorRef.current;
  director.noWalk = reducedMotion;

  const kick = useAnimationLoop(director, scene, looks, data, focus, speed, reducedMotion);

  const sceneRef = useRef(scene);
  useLayoutEffect(() => {
    if (sceneRef.current === scene) return;
    sceneRef.current = scene;
    director.setScene(scene);
    director.setEmployees(employees);
    kick.current();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [scene]);

  useStatusFeed(director, employees, events, props.seekKey, kick);

  useEffect(() => {
    const on = () => (director.noWalk = reducedMotion || document.hidden);
    document.addEventListener("visibilitychange", on);
    return () => document.removeEventListener("visibilitychange", on);
  }, [director, reducedMotion]);

  const select = useCallback((id: string | null) => {
    setLocalSel(id);
    props.onSelect?.(id);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [props.onSelect]);

  const plate = useCallback((no: number) => {
    if ((layout.focus?.clickPlate ?? "focusThread") === "openThread") return props.onAction?.({ type: "openThread", threadNo: no });
    props.onFocusThread?.(focus === no ? null : no);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [layout, focus, props.onAction, props.onFocusThread]);

  const outer = useRef<HTMLDivElement>(null);
  const frameW = layout.canvas.frame?.width ?? 4;
  const width = useWidth(outer);
  const scale = width ? pickScale(width - frameW * 2, scene.width) : 1;

  return (
    <div ref={outer} className={props.className} style={{ width: "100%", minWidth: 0, ...props.style }}>
      {scale === 0 ? (
        <OfficeList scene={scene} director={director} onSelect={select} selected={selected} version={kick.version} />
      ) : (
        <Stage
          scene={scene}
          scale={scale}
          director={director}
          looks={looks}
          data={data}
          focus={focus}
          selected={selected}
          reducedMotion={reducedMotion}
          kick={kick}
          now={props.now}
          onSelect={select}
          onPlate={plate}
          onAction={props.onAction}
          onPage={setPage}
        />
      )}
    </div>
  );
}

const EMPTY: OfficeStatusEvent[] = [];
const EMPTY_DATA: OfficeWidgetData = {};

interface Kick {
  (): void;
}

interface LoopHandle {
  current: Kick;
  /** Increments when the overlay should re-render. */
  version: number;
  dynamic: React.MutableRefObject<HTMLCanvasElement | null>;
  actorEls: Map<string, HTMLElement>;
  scale: React.MutableRefObject<number>;
}

/** requestAnimationFrame loop: ticks the director, draws the dynamic layer, moves overlay tags; idles when nothing moves. */
function useAnimationLoop(
  director: Director,
  scene: Scene,
  looks: Map<string, ResolvedLook>,
  data: OfficeWidgetData,
  focus: number | null,
  speed: number,
  reducedMotion: boolean,
): LoopHandle {
  const [version, setVersion] = useState(0);
  const state = useRef({ scene, looks, data, focus, speed, reducedMotion });
  state.current = { scene, looks, data, focus, speed, reducedMotion };
  const handle = useRef<LoopHandle>({ current: () => {}, version: 0, dynamic: { current: null }, actorEls: new Map(), scale: { current: 1 } }).current;
  handle.version = version;
  const running = useRef(false);
  const seen = useRef(-1);

  const frame = useCallback(
    function step(t: number, last: number) {
      const s = state.current;
      director.tick(Math.min(t - last, 100) * s.speed);
      const actors = director.actors();
      const canvas = handle.dynamic.current;
      if (canvas) {
        const ctx = prepareCanvas(canvas, s.scene.width, s.scene.height, handle.scale.current);
        drawDynamic(ctx, { scene: s.scene, actors, desks: director.desks(), looks: s.looks, nodes: s.data.nodes ?? [], time: t, view: { focus: s.focus, reducedMotion: s.reducedMotion } });
      }
      for (const a of actors) {
        const el = handle.actorEls.get(a.id);
        if (el) el.style.transform = `translate(${Math.round(a.pos[0])}px, ${Math.round(a.pos[1])}px)`;
      }
      if (director.version !== seen.current) {
        seen.current = director.version;
        setVersion((v) => v + 1);
      }
      const more = director.busy || (!s.reducedMotion && hasLiveAnimation(s.scene.layout, actors, s.data));
      if (!more) return void (running.current = false);
      requestAnimationFrame((t2) => step(t2, t));
    },
    [director, handle],
  );

  handle.current = () => {
    if (running.current) return;
    running.current = true;
    requestAnimationFrame((t) => frame(t, t));
  };
  useEffect(() => handle.current(), [scene, looks, data, focus, speed, reducedMotion, handle]);
  return handle;
}

/** Applies status events (append → walk, otherwise rebuild) and changed roster statuses. */
function useStatusFeed(director: Director, employees: OfficeEmployee[], events: OfficeStatusEvent[], seekKey: number | string | undefined, kick: LoopHandle): void {
  const cursor = useRef<FeedCursor | null>(null);
  const lastSeek = useRef(seekKey);
  const rosterStatus = useRef(new Map<string, string>());

  useLayoutEffect(() => {
    const first = cursor.current === null;
    const seeked = lastSeek.current !== seekKey;
    lastSeek.current = seekKey;
    const change = first || seeked ? { kind: "reset" as const } : diffFeed(cursor.current!, events);
    cursor.current = cursorOf(events);
    if (change.kind === "none") return;
    if (change.kind === "reset") director.reset(foldStatuses(employees, events));
    else for (const ev of events.slice(change.from)) director.apply(eventEmployee(ev), { since: ev.at, ...ev.status });
    kick.current();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [events, seekKey]);

  useLayoutEffect(() => {
    const seenBefore = rosterStatus.current;
    const next = new Map<string, string>();
    for (const e of employees) {
      const key = JSON.stringify(e.status ?? null);
      next.set(e.id, key);
      const prev = seenBefore.get(e.id);
      if (prev !== undefined && prev !== key && e.status) director.apply(e.id, e.status as EmployeeStatus);
    }
    rosterStatus.current = next;
    kick.current();
  }, [employees, director, kick]);
}

interface StageProps {
  scene: Scene;
  scale: number;
  director: Director;
  looks: Map<string, ResolvedLook>;
  data: OfficeWidgetData;
  focus: number | null;
  selected: string | null;
  reducedMotion: boolean;
  kick: LoopHandle;
  now?: number;
  onSelect: (id: string | null) => void;
  onPlate: (no: number) => void;
  onAction?: (a: OfficeAction) => void;
  onPage: (p: number) => void;
}

function Stage(p: StageProps) {
  const { scene, scale, kick } = p;
  const layout = scene.layout;
  const staticRef = useRef<HTMLCanvasElement>(null);
  const [imagesLoaded, setImagesLoaded] = useState(0);
  const [second, setSecond] = useState(() => Date.now());
  kick.scale.current = scale;

  useEffect(() => {
    setImageLoadHandler(() => setImagesLoaded((n) => n + 1));
    const t = setInterval(() => setSecond(Date.now()), 1000);
    return () => {
      setImageLoadHandler(undefined);
      clearInterval(t);
    };
  }, []);

  useLayoutEffect(() => {
    const canvas = staticRef.current;
    if (!canvas) return;
    drawStatic(prepareCanvas(canvas, scene.width, scene.height, scale), scene, { focus: p.focus });
    kick.current();
  }, [scene, scale, p.focus, imagesLoaded, kick]);

  const registerActor = useCallback((id: string, el: HTMLElement | null) => {
    if (el) kick.actorEls.set(id, el);
    else kick.actorEls.delete(id);
  }, [kick]);

  const c = { palette: layout.palette };
  const frame = layout.canvas.frame;
  const actors = p.director.actors();
  const canvasStyle: CSSProperties = { position: "absolute", left: 0, top: 0, imageRendering: "pixelated" };
  return (
    <div
      style={{
        overflow: "auto", borderRadius: frame?.radius ?? 12, border: `${frame?.width ?? 4}px solid ${colorOr(frame?.color, c, "#3A3D48")}`,
        background: colorOr(layout.canvas.background, c, "#EDE6D8"), maxWidth: "100%",
      }}
    >
      <div style={{ position: "relative", width: scene.width * scale, height: scene.height * scale }} onClick={() => p.onSelect(null)}>
        <canvas ref={staticRef} style={canvasStyle} aria-hidden />
        <canvas ref={kick.dynamic} style={canvasStyle} aria-hidden />
        <div style={{ position: "absolute", left: 0, top: 0, width: scene.width, height: scene.height, transform: `scale(${scale})`, transformOrigin: "0 0" }}>
          <OfficeOverlay
            scene={scene}
            actors={actors}
            desks={p.director.desks()}
            looks={p.looks}
            data={p.data}
            focus={p.focus}
            selected={p.selected}
            now={p.now ?? second}
            clock={second}
            reducedMotion={p.reducedMotion}
            registerActor={registerActor}
            onSelect={p.onSelect}
            onPlate={p.onPlate}
            onAction={p.onAction}
            onPage={p.onPage}
          />
        </div>
      </div>
    </div>
  );
}

/** Phone fallback (office-layout.md §5): people grouped by thread with their state. */
function OfficeList({ scene, director, onSelect, selected }: { scene: Scene; director: Director; onSelect: (id: string) => void; selected: string | null; version: number }) {
  const actors = director.actors();
  const groups = [
    { key: "coord", title: "项目统筹", list: actors.filter((a) => a.employee.thread_no == null) },
    ...scene.visible.map((t) => ({ key: String(t.no), title: `#${t.no} ${t.title}`, list: actors.filter((a) => a.employee.thread_no === t.no) })),
  ].filter((g) => g.list.length);
  return (
    <div style={{ display: "flex", flexDirection: "column", gap: 12, color: "#E6E3DC", fontSize: 13 }}>
      {groups.map((g) => (
        <section key={g.key} style={{ background: "#262931", borderRadius: 10, padding: 10 }}>
          <div style={{ fontWeight: 600, marginBottom: 6 }}>{g.title}</div>
          {g.list.map((a) => (
            <button
              key={a.id}
              type="button"
              onClick={() => onSelect(a.id)}
              style={{ display: "flex", gap: 8, width: "100%", textAlign: "left", background: selected === a.id ? "#353844" : "transparent", border: "none", color: "inherit", padding: "4px 2px", font: "inherit" }}
            >
              <span style={{ minWidth: 64, fontFamily: "monospace" }}>{a.employee.name}</span>
              <span style={{ color: "#A8A49B" }}>{STATE_LABELS[a.status.state] ?? a.status.state}</span>
              {a.bubble && <span>{a.bubble.text}</span>}
            </button>
          ))}
        </section>
      ))}
    </div>
  );
}
