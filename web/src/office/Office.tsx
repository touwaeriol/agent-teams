// <Office/>: the pixel-art project office (docs/v2/office-layout.md), Vue JSX port of
// web/src/office/Office.tsx. Props are documented on OfficeProps; see README.md in this folder.
// Callbacks are props (onSelect, onFocusThread, onAction), so `@select="…"` binds them and the
// overlay can tell whether anyone listens (click targets are only drawn then).
import {
  computed,
  defineComponent,
  onBeforeUnmount,
  onMounted,
  ref,
  shallowRef,
  toRaw,
  watch,
  type CSSProperties,
  type PropType,
  type Ref,
  type ShallowRef,
} from "vue";
import { colorOr } from "./color";
import { compileScene, type Scene } from "./compile";
import { Director, type ActorView } from "./director";
import { useLeaving, withLeaving } from "./leaving";
import { hasTask } from "./people";
import { resolveLook, type ResolvedLook } from "./looks";
import { OfficeOverlay, STATE_LABELS, type OfficeAction } from "./overlay";
import { cursorOf, diffFeed, eventEmployee, foldStatuses, type FeedCursor } from "./replay";
import { drawDynamic, drawStatic, prepareCanvas, setImageLoadHandler } from "./render";
import { sx } from "./sx";
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

function useReducedMotion(): Ref<boolean> {
  const query = typeof window !== "undefined" && window.matchMedia ? window.matchMedia("(prefers-reduced-motion: reduce)") : undefined;
  const reduced = ref(Boolean(query?.matches));
  const on = () => (reduced.value = Boolean(query?.matches));
  query?.addEventListener("change", on);
  onBeforeUnmount(() => query?.removeEventListener("change", on));
  return reduced;
}

function useWidth(el: Ref<HTMLElement | null>): Ref<number> {
  const width = ref(0);
  let ro: ResizeObserver | undefined;
  onMounted(() => {
    if (!el.value) return;
    width.value = el.value.clientWidth;
    if (typeof ResizeObserver === "undefined") return;
    ro = new ResizeObserver(() => (width.value = el.value?.clientWidth ?? 0));
    ro.observe(el.value);
  });
  onBeforeUnmount(() => ro?.disconnect());
  return width;
}

const loadedFonts = new Set<string>();

function loadGoogleFonts(families: string[] | undefined): void {
  for (const f of families ?? []) {
    if (loadedFonts.has(f)) continue;
    loadedFonts.add(f);
    const link = document.createElement("link");
    link.rel = "stylesheet";
    link.href = `https://fonts.googleapis.com/css2?family=${encodeURIComponent(f).replace(/%20/g, "+")}&display=swap`;
    document.head.appendChild(link);
  }
}

function hasLiveAnimation(scene: Scene, actors: ActorView[], data: OfficeWidgetData): boolean {
  const layout = scene.layout;
  if (scene.pods.some((p) => p.leaving !== undefined)) return true;
  const anims = layout.characters.person.animations;
  const animated = actors.some((a) => !a.hidden && ((anims[a.anim]?.frames.length ?? 1) > 1 || anims[a.anim]?.bob || a.overlay));
  const racks = (layout.widgets ?? []).some((w) => w.type === "nodeRacks" && Object.values(w.slotColors ?? {}).some((c) => c?.blink));
  return animated || (racks && Boolean(data.nodes?.length));
}

const EMPTY: OfficeStatusEvent[] = [];
const EMPTY_DATA: OfficeWidgetData = {};

/** What the animation loop reads each frame; refreshed by the component. */
interface LoopState {
  scene: Scene;
  looks: Map<string, ResolvedLook>;
  data: OfficeWidgetData;
  focus: number | null;
  speed: number;
  reducedMotion: boolean;
}

export interface Loop {
  kick: () => void;
  /** Increments when the overlay should re-render. */
  version: Ref<number>;
  dynamic: ShallowRef<HTMLCanvasElement | null>;
  actorEls: Map<string, HTMLElement>;
  scale: { current: number };
  setScale: (scale: number) => void;
}

/** requestAnimationFrame loop: ticks the director, draws the dynamic layer, moves overlay tags; idles when nothing moves. */
function useAnimationLoop(director: Director, state: () => LoopState): Loop {
  const scale = { current: 1 };
  const loop: Loop = { kick: () => undefined, version: ref(0), dynamic: shallowRef(null), actorEls: new Map(), scale, setScale: (n) => (scale.current = n) };
  let running = false;
  let seen = -1;
  let alive = true;
  const step = (t: number, last: number) => {
    if (!alive) return void (running = false);
    const s = state();
    director.tick(Math.min(t - last, 100) * s.speed);
    const actors = director.actors();
    const canvas = loop.dynamic.value;
    if (canvas) {
      const ctx = prepareCanvas(canvas, s.scene.width, s.scene.height, loop.scale.current);
      drawDynamic(ctx, { scene: s.scene, actors, desks: director.desks(), looks: s.looks, nodes: s.data.nodes ?? [], time: t, view: { focus: s.focus, reducedMotion: s.reducedMotion } });
    }
    for (const a of actors) {
      const el = loop.actorEls.get(a.id);
      if (el) el.style.transform = `translate(${Math.round(a.pos[0])}px, ${Math.round(a.pos[1])}px)`;
    }
    if (director.version !== seen) {
      seen = director.version;
      loop.version.value += 1;
    }
    const more = director.busy || (!s.reducedMotion && hasLiveAnimation(s.scene, actors, s.data));
    if (!more) return void (running = false);
    requestAnimationFrame((t2) => step(t2, t));
  };
  loop.kick = () => {
    if (running || !alive) return;
    running = true;
    requestAnimationFrame((t) => step(t, t));
  };
  onBeforeUnmount(() => (alive = false));
  return loop;
}

/** Applies status events (append → walk, otherwise rebuild) and changed roster statuses. */
function useStatusFeed(director: Director, props: { employees: OfficeEmployee[]; events: OfficeStatusEvent[]; seekKey?: number | string }, loop: Loop): void {
  let cursor: FeedCursor | null = null;
  let lastSeek = props.seekKey;
  watch(
    () => [props.events, props.seekKey] as const,
    () => {
      const events = toRaw(props.events);
      const first = cursor === null;
      const seeked = lastSeek !== props.seekKey;
      lastSeek = props.seekKey;
      const change = first || seeked ? { kind: "reset" as const } : diffFeed(cursor!, events);
      cursor = cursorOf(events);
      if (change.kind === "none") return;
      if (change.kind === "reset") director.reset(foldStatuses(toRaw(props.employees), events));
      else for (const ev of events.slice(change.from)) director.apply(eventEmployee(ev), { since: ev.at, ...ev.status });
      loop.kick();
    },
    { immediate: true },
  );

  let rosterStatus = new Map<string, string>();
  watch(
    () => props.employees,
    (employees) => {
      const next = new Map<string, string>();
      for (const e of toRaw(employees)) {
        const key = JSON.stringify(e.status ?? null);
        next.set(e.id, key);
        const prev = rosterStatus.get(e.id);
        if (prev !== undefined && prev !== key && e.status) director.apply(e.id, e.status as EmployeeStatus);
      }
      rosterStatus = next;
      loop.kick();
    },
    { immediate: true },
  );
}

export const Office = defineComponent({
  name: "Office",
  props: {
    layout: { type: Object as PropType<OfficeLayout>, required: true },
    employees: { type: Array as PropType<OfficeEmployee[]>, required: true },
    threads: { type: Array as PropType<OfficeThread[]>, required: true },
    events: { type: Array as PropType<OfficeStatusEvent[]>, default: () => EMPTY },
    focus: { type: Number as PropType<number | null>, default: null },
    selected: { type: String as PropType<string | null>, default: undefined },
    data: { type: Object as PropType<OfficeWidgetData>, default: () => EMPTY_DATA },
    taskTitles: { type: Object as PropType<Record<string, string>>, default: undefined },
    speed: { type: Number, default: 1 },
    now: { type: Number, default: undefined },
    seekKey: { type: [Number, String] as PropType<number | string>, default: undefined },
    onSelect: { type: Function as PropType<OfficeProps["onSelect"]>, default: undefined },
    onFocusThread: { type: Function as PropType<OfficeProps["onFocusThread"]>, default: undefined },
    onAction: { type: Function as PropType<OfficeProps["onAction"]>, default: undefined },
  },
  setup(props) {
    const page = ref(0);
    const localSel = ref<string | null>(null);
    const reducedMotion = useReducedMotion();
    const layout = computed(() => toRaw(props.layout));
    watch(() => layout.value.fonts?.googleFonts, loadGoogleFonts, { immediate: true });

    // Threads that just ended stay (fading) while their leads walk out.
    const leaving = useLeaving(() => layout.value, () => toRaw(props.threads), () => toRaw(props.employees), (id) => director.hasLeft(id));
    const input = () => withLeaving(toRaw(props.threads), toRaw(props.employees), leaving.value);
    // Recompile only when what the scene depends on changes (not on every new array identity).
    // Holding a task or not decides a visiting member's seat (members.visitPods).
    const rosterKey = computed(() => signature(props.employees, (e) => [e.id, e.name, e.thread_no, e.tier, e.role, e.sort, e.instance, hasTask(e.status)]));
    const threadsKey = computed(() => signature(props.threads, (t) => [t.no, t.title, t.status, t.counts?.main_done, t.counts?.main_total, t.updated_at]));
    const compile = () => {
      const x = input();
      return compileScene(layout.value, { threads: x.threads, employees: x.employees, focus: props.focus, page: page.value, leaving: x.since });
    };
    const scene = shallowRef<Scene>(compile());
    const looksKey = computed(() => signature(props.employees, (e) => [e.id, e.name, e.tier, e.role, e.look, e.instance]));
    const resolveLooks = () => new Map<string, ResolvedLook>(input().employees.map((e) => [e.id, resolveLook(layout.value, e)]));
    const looks = shallowRef(resolveLooks());
    watch([layout, looksKey, leaving], () => (looks.value = resolveLooks()));

    const director = new Director(scene.value, {
      now: () => props.now ?? Date.now(),
      clock: () => performance.now(),
      taskTitle: (id) => props.taskTitles?.[id],
    });
    director.setEmployees(input().employees);
    watch(reducedMotion, (r) => (director.noWalk = r), { immediate: true });

    const loop = useAnimationLoop(director, () => ({
      scene: scene.value,
      looks: looks.value,
      data: toRaw(props.data),
      focus: props.focus,
      speed: props.speed,
      reducedMotion: reducedMotion.value,
    }));

    watch([layout, rosterKey, threadsKey, () => props.focus, page, leaving], () => {
      scene.value = compile();
      director.setScene(scene.value);
      director.setEmployees(input().employees);
      loop.kick();
    });
    useStatusFeed(director, props, loop);
    watch([scene, looks, () => props.data, () => props.focus, () => props.speed, reducedMotion], () => loop.kick(), { immediate: true });

    const onVisibility = () => (director.noWalk = reducedMotion.value || document.hidden);
    document.addEventListener("visibilitychange", onVisibility);
    onBeforeUnmount(() => document.removeEventListener("visibilitychange", onVisibility));

    const selected = computed(() => (props.selected !== undefined ? props.selected : localSel.value));
    const select = (id: string | null) => {
      localSel.value = id;
      props.onSelect?.(id);
    };
    const plate = (no: number) => {
      if ((layout.value.focus?.clickPlate ?? "focusThread") === "openThread") return props.onAction?.({ type: "openThread", threadNo: no });
      props.onFocusThread?.(props.focus === no ? null : no);
    };

    const outer = ref<HTMLElement | null>(null);
    const width = useWidth(outer);
    const scale = computed(() => {
      const frameW = layout.value.canvas.frame?.width ?? 4;
      return width.value ? pickScale(width.value - frameW * 2, scene.value.width) : 1;
    });

    return () => (
      <div ref={outer} style={{ width: "100%", minWidth: "0" }}>
        {scale.value === 0 ? (
          <OfficeList scene={scene.value} director={director} onSelect={select} selected={selected.value} version={loop.version.value} />
        ) : (
          <Stage
            scene={scene.value}
            scale={scale.value}
            director={director}
            looks={looks.value}
            data={toRaw(props.data)}
            focus={props.focus}
            selected={selected.value}
            reducedMotion={reducedMotion.value}
            loop={loop}
            version={loop.version.value}
            now={props.now}
            onSelect={select}
            onPlate={plate}
            onAction={props.onAction}
            onPage={(p: number) => (page.value = p)}
          />
        )}
      </div>
    );
  },
});

const Stage = defineComponent({
  name: "OfficeStage",
  props: {
    scene: { type: Object as PropType<Scene>, required: true },
    scale: { type: Number, required: true },
    director: { type: Object as PropType<Director>, required: true },
    looks: { type: Object as PropType<Map<string, ResolvedLook>>, required: true },
    data: { type: Object as PropType<OfficeWidgetData>, required: true },
    focus: { type: Number as PropType<number | null>, default: null },
    selected: { type: String as PropType<string | null>, default: null },
    reducedMotion: { type: Boolean, required: true },
    loop: { type: Object as PropType<Loop>, required: true },
    /** Read during render so the overlay redraws when the director changes. */
    version: { type: Number, required: true },
    now: { type: Number, default: undefined },
    onSelect: { type: Function as PropType<(id: string | null) => void>, required: true },
    onPlate: { type: Function as PropType<(no: number) => void>, required: true },
    onAction: { type: Function as PropType<(a: OfficeAction) => void>, default: undefined },
    onPage: { type: Function as PropType<(p: number) => void>, required: true },
  },
  setup(p) {
    const staticCanvas = ref<HTMLCanvasElement | null>(null);
    const imagesLoaded = ref(0);
    const second = ref(Date.now());
    let timer: ReturnType<typeof setInterval> | undefined;
    onMounted(() => {
      setImageLoadHandler(() => (imagesLoaded.value += 1));
      timer = setInterval(() => (second.value = Date.now()), 1000);
    });
    onBeforeUnmount(() => {
      setImageLoadHandler(undefined);
      clearInterval(timer);
    });
    watch(
      [() => p.scene, () => p.scale, () => p.focus, imagesLoaded, staticCanvas],
      () => {
        p.loop.setScale(p.scale);
        if (!staticCanvas.value) return;
        drawStatic(prepareCanvas(staticCanvas.value, p.scene.width, p.scene.height, p.scale), p.scene, { focus: p.focus });
        p.loop.kick();
      },
      { flush: "post", immediate: true },
    );
    const registerActor = (id: string, el: HTMLElement | null) => {
      if (el) p.loop.actorEls.set(id, el);
      else p.loop.actorEls.delete(id);
    };
    const canvasStyle: CSSProperties = { position: "absolute", left: "0", top: "0", imageRendering: "pixelated" };

    return () => {
      void p.version;
      p.loop.setScale(p.scale);
      const layout = p.scene.layout;
      const c = { palette: layout.palette };
      const frame = layout.canvas.frame;
      return (
        <div
          style={sx({
            overflow: "auto",
            borderRadius: frame?.radius ?? 12,
            border: `${frame?.width ?? 4}px solid ${colorOr(frame?.color, c, "#3A3D48")}`,
            background: colorOr(layout.canvas.background, c, "#EDE6D8"),
            maxWidth: "100%",
          })}
        >
          <div style={sx({ position: "relative", width: p.scene.width * p.scale, height: p.scene.height * p.scale })} onClick={() => p.onSelect(null)}>
            <canvas ref={staticCanvas} style={canvasStyle} aria-hidden="true" />
            <canvas ref={p.loop.dynamic} style={canvasStyle} aria-hidden="true" />
            <div style={sx({ position: "absolute", left: 0, top: 0, width: p.scene.width, height: p.scene.height, transform: `scale(${p.scale})`, transformOrigin: "0 0" })}>
              <OfficeOverlay
                scene={p.scene}
                actors={p.director.actors()}
                desks={p.director.desks()}
                looks={p.looks}
                data={p.data}
                focus={p.focus}
                selected={p.selected}
                now={p.now ?? second.value}
                clock={second.value}
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
    };
  },
});

/** Phone fallback (office-layout.md §5): the coordinator, each thread's lead and the members working for it, then the free members. */
export function listGroups(scene: Scene, actors: ActorView[]): { key: string; title: string; list: ActorView[] }[] {
  const shown = new Set(scene.visible.map((t) => t.no));
  return [
    { key: "coord", title: "项目统筹", list: actors.filter((a) => a.employee.tier === "coordinator") },
    ...scene.visible.map((t) => ({ key: String(t.no), title: `#${t.no} ${t.title}`, list: actors.filter((a) => a.thread === t.no) })),
    { key: "free", title: "空闲成员", list: actors.filter((a) => a.employee.tier === "member" && (a.thread === null || !shown.has(a.thread))) },
  ].filter((g) => g.list.length);
}

function OfficeList(p: { scene: Scene; director: Director; onSelect: (id: string) => void; selected: string | null; version: number }) {
  const groups = listGroups(p.scene, p.director.actors());
  return (
    <div style={sx({ display: "flex", flexDirection: "column", gap: 12, color: "#E6E3DC", fontSize: 13 })}>
      {groups.map((g) => (
        <section key={g.key} style={sx({ background: "#262931", borderRadius: 10, padding: 10 })}>
          <div style={sx({ fontWeight: 600, marginBottom: 6 })}>{g.title}</div>
          {g.list.map((a) => (
            <button
              key={a.id}
              type="button"
              onClick={() => p.onSelect(a.id)}
              style={sx({
                display: "flex", gap: 8, width: "100%", textAlign: "left", background: p.selected === a.id ? "#353844" : "transparent",
                border: "none", color: "inherit", padding: "4px 2px", font: "inherit",
              })}
            >
              <span style={sx({ minWidth: 64, fontFamily: "monospace" })}>{a.employee.name}</span>
              <span style={sx({ color: "#A8A49B" })}>{STATE_LABELS[a.status.state] ?? a.status.state}</span>
              {a.bubble && <span>{a.bubble.text}</span>}
            </button>
          ))}
        </section>
      ))}
    </div>
  );
}
