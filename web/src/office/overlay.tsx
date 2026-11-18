// DOM overlay (office-layout.md §5): every text in the office — room names,
// pod plates, name tags, bubbles, timers, widget text — plus the click
// targets. Same transform as the canvas (scene px × scale). Vue JSX port of
// web/src/office/overlay.tsx: function components, styles through sx() (Vue adds no px).
import type { CSSProperties, VNode } from "vue";
import { colorOr, type ColorContext } from "./color";
import type { DeskInstance, PodInstance, Scene } from "./compile";
import type { ActorView, DeskState } from "./director";
import type { ResolvedLook } from "./looks";
import { isMember } from "./people";
import { blinkOn, dimFor, participantsByPod, rackPosition, stripPoint, stripSlots, visibleRacks } from "./render";
import { fillTemplate } from "./template";
import { sx } from "./sx";
import type { BoardCards, OfficeLayout, OfficeQueueCard, OfficeWidgetData, PipelineSection, TextStyle, Widget } from "./types";

export type OfficeAction =
  | { type: "openTaskPool" }
  | { type: "openNode"; nodeId: string }
  | { type: "openMergeQueue" }
  | { type: "openThread"; threadNo: number };

export const STATE_LABELS: Record<string, string> = {
  typing: "写代码", reading: "看代码", running: "跑命令", thinking: "思考", walking_to_board: "去任务板",
  claiming: "领任务", returning: "回工位", raising_hand: "举手", idle: "空闲", waiting: "等待",
  queued: "排队等工位", offline: "离线", broken: "出错",
};

/** Person box of the design (40 × 80): name tag and bubble offsets are relative to it. */
export const PERSON_BOX_H = 80;

export interface OverlayProps {
  scene: Scene;
  actors: ActorView[];
  desks: Map<string, DeskState>;
  looks: Map<string, ResolvedLook>;
  data: OfficeWidgetData;
  focus: number | null;
  selected: string | null;
  now: number;
  /** Animation clock for blinking lights. */
  clock: number;
  reducedMotion: boolean;
  registerActor: (id: string, el: HTMLElement | null) => void;
  onSelect: (id: string) => void;
  onPlate: (no: number) => void;
  onAction?: (a: OfficeAction) => void;
  onPage: (page: number) => void;
}

const ctxOf = (l: OfficeLayout): ColorContext => ({ palette: l.palette });

/** Overlay colours a layout's palette may override (`palette.labelText`…); these are the defaults. */
const TOKENS = { labelText: "#FFFFFF", labelDark: "#23262D", labelMuted: "#5C5F6B", roomLabel: "#8A6A45" } as const;

export function token(l: OfficeLayout, name: keyof typeof TOKENS): string {
  return colorOr(`$${name}`, ctxOf(l), TOKENS[name]);
}

/** Stacking inside the overlay: walking people's name tags pass under the room plates, seated ones stay on top. */
const Z = { walkingTag: 1, roomLabel: 2, tag: 3 } as const;
const font = (l: OfficeLayout, which: "label" | "bubble" | undefined) => (which === "bubble" ? l.fonts?.bubble : l.fonts?.label) ?? "monospace";

function textBox(l: OfficeLayout, s: TextStyle | undefined, extra: ColorContext = {}): CSSProperties {
  const c = { ...ctxOf(l), ...extra };
  const [py, px] = s?.padding ?? [1, 4];
  return {
    fontFamily: font(l, s?.font),
    fontSize: s?.size ?? 10,
    lineHeight: `${Math.round((s?.size ?? 10) * 1.4)}px`,
    color: colorOr(s?.color, c, "#1F1E1C"),
    background: s?.bg ? colorOr(s.bg, c) : undefined,
    border: s?.border ? `2px solid ${colorOr(s.border, c)}` : undefined,
    padding: `${py}px ${px}px`,
    whiteSpace: "nowrap",
  };
}

export function formatTimer(ms: number): string {
  const s = Math.max(0, Math.floor(ms / 1000));
  const two = (n: number) => String(n).padStart(2, "0");
  if (s >= 6000) return `${Math.floor(s / 3600)}h${two(Math.floor((s % 3600) / 60))}`;
  return `${two(Math.floor(s / 60))}:${two(s % 60)}`;
}

export function OfficeOverlay(p: OverlayProps) {
  const l = p.scene.layout;
  return (
    <div style={sx({ position: "absolute", inset: 0, pointerEvents: "none", fontFamily: font(l, "label") })}>
      <RoomLabels layout={l} scene={p.scene} />
      {p.scene.pods.map((pod) => <Plate key={pod.threadNo} pod={pod} {...p} />)}
      <StripMore {...p} />
      <DeskTags {...p} />
      <DeskTimers {...p} />
      {(l.widgets ?? []).map((w) => <WidgetOverlay key={w.id} widget={w} {...p} />)}
      <Pager {...p} />
      {p.actors.map((a) => <ActorTag key={a.id} actor={a} {...p} />)}
    </div>
  );
}

function RoomLabels({ layout, scene }: { layout: OfficeLayout; scene: Scene }) {
  const style = layout.labels?.room;
  return (
    <>
      {scene.rooms.filter((r) => !r.labelStyle?.hidden).map((r) => {
        const [ox, oy] = r.labelStyle?.offset ?? style?.offset ?? [8, 6];
        return (
          <span
            key={r.id}
            style={sx({
              position: "absolute", left: r.rect[0] + ox, top: r.rect[1] + oy, zIndex: Z.roomLabel,
              ...textBox(layout, { ...style, bg: r.labelStyle?.bg ?? token(layout, "roomLabel"), color: r.labelStyle?.fg ?? token(layout, "labelText") }),
              lineHeight: "normal",
            })}
          >
            {r.label}
          </span>
        );
      })}
    </>
  );
}

function Plate({ pod, scene, focus, onPlate }: { pod: PodInstance } & OverlayProps) {
  const l = scene.layout;
  if (!pod.plate) return null;
  const clickable = (l.focus?.clickPlate ?? "focusThread") !== "none";
  return (
    <button
      type="button"
      disabled={!clickable}
      onClick={() => onPlate(pod.threadNo)}
      aria-pressed={focus === pod.threadNo}
      aria-label={`讨论串 ${pod.plate.text}`}
      style={sx({
        position: "absolute", left: pod.plate.at[0], top: pod.plate.at[1], pointerEvents: "auto", cursor: clickable ? "pointer" : "default",
        ...textBox(l, pod.plate.style, { vars: { threadColor: pod.color } }),
        lineHeight: "normal",
        opacity: dimFor(l, focus, pod.threadNo) * (pod.leaving === undefined ? 1 : 0.4),
      })}
    >
      {pod.plate.text}
    </button>
  );
}

/** "+N" after a table's participant strip when more members work for it than it shows. */
function StripMore({ scene, actors, focus }: OverlayProps) {
  const l = scene.layout;
  const out: VNode[] = [];
  for (const [pod, people] of participantsByPod(scene, actors)) {
    const strip = pod.participants!;
    const { shown, more } = stripSlots(people, strip.max);
    if (!more) continue;
    const [x, y] = stripPoint(strip, shown.length);
    out.push(
      <span
        key={pod.threadNo}
        title={people.map((a) => a.employee.name).join("、")}
        style={sx({ position: "absolute", left: x, top: y, width: strip.size, height: strip.size, display: "flex", alignItems: "center", justifyContent: "center", background: pod.color, color: token(l, "labelText"), fontSize: 8, lineHeight: "normal", opacity: dimFor(l, focus, pod.threadNo) })}
      >
        +{more}
      </span>,
    );
  }
  return <>{out}</>;
}

/** `#18 · t5` on a member's own desk while it works on a task (wherever it is right now). */
function DeskTags({ scene, actors, focus }: OverlayProps) {
  const l = scene.layout;
  const byId = new Map(actors.map((a) => [a.id, a]));
  const out: VNode[] = [];
  for (const d of scene.staff.values()) {
    const a = d.owner ? byId.get(d.owner) : undefined;
    const tag = a && workTag(a);
    if (!tag || !d.tag) continue;
    out.push(
      <span
        key={d.key}
        style={sx({
          position: "absolute", left: d.tag[0], top: d.tag[1], transform: "translateX(-50%)", padding: "0 3px", whiteSpace: "nowrap", background: scene.threadColors.get(a.thread!) ?? token(l, "labelDark"),
          color: token(l, "labelText"), fontFamily: font(l, "label"), fontSize: 8, lineHeight: "11px", opacity: dimFor(l, focus, a.thread),
        })}
      >
        {tag}
      </span>,
    );
  }
  return <>{out}</>;
}

function DeskTimers({ scene, desks, now, focus }: OverlayProps) {
  const l = scene.layout;
  const style = l.labels?.timer;
  const out: VNode[] = [];
  for (const d of scene.desks.values()) {
    const since = desks.get(d.key)?.timerSince;
    if (since === undefined || !d.timer) continue;
    const thread = d.threadNo ?? desks.get(d.key)?.thread;
    out.push(
      <span key={d.key} style={sx({ position: "absolute", left: d.timer[0], top: d.timer[1], ...textBox(l, { ...style, padding: [0, 3] }), lineHeight: "13px", opacity: dimFor(l, focus, thread) })}>
        {formatTimer(now - since)}
      </span>,
    );
  }
  return <>{out}</>;
}

function WidgetOverlay(p: { widget: Widget } & OverlayProps) {
  if (p.widget.type === "taskBoard") return <TaskBoardText {...p} />;
  if (p.widget.type === "nodeRacks") return <RackLabels {...p} />;
  return <PipelinePanel {...p} />;
}

function clickTarget(onClick: (() => void) | undefined, label: string, rect: [number, number, number, number]) {
  if (!onClick) return null;
  return (
    <button
      type="button"
      aria-label={label}
      onClick={onClick}
      style={sx({ position: "absolute", left: rect[0], top: rect[1], width: rect[2], height: rect[3], pointerEvents: "auto", background: "transparent", border: "none", cursor: "pointer" })}
    />
  );
}

/** Pending tasks on the board: the focused thread's, or the whole project's. */
export function claimableCount(board: OfficeWidgetData["board"], focus: number | null): number {
  if (focus !== null) return board?.by_thread?.[String(focus)]?.pending ?? 0;
  return board?.claimable ?? 0;
}

function TaskBoardText({ widget: w, scene, data, focus, onAction }: { widget: Widget } & OverlayProps) {
  const l = scene.layout;
  const sprite = w.sprite ? l.sprites[w.sprite] : undefined;
  const claimable = claimableCount(data.board, focus);
  const text = fillTemplate(w.text?.value ?? "{claimable}", { claimable });
  const [tx, ty] = w.text?.at ?? [0, 0];
  return (
    <>
      <span style={sx({ position: "absolute", left: w.at[0] + tx, top: w.at[1] + ty, ...textBox(l, { ...w.text, padding: [0, 0] }), lineHeight: "normal" })}>{text}</span>
      {w.cards && <QueueCards widget={w} cards={w.cards} scene={scene} queue={boardQueue(data.board, focus)} />}
      {w.click !== "none" && sprite && clickTarget(onAction && (() => onAction({ type: "openTaskPool" })), text, [w.at[0], w.at[1], sprite.w, sprite.h])}
    </>
  );
}

/** The project queue on the board: the focused thread's pending tasks, or everyone's. */
export function boardQueue(board: OfficeWidgetData["board"], focus: number | null): OfficeQueueCard[] {
  const queue = board?.queue ?? [];
  return focus === null ? queue : queue.filter((c) => c.thread_no === focus);
}

export interface CardSlot {
  at: [number, number];
  card?: OfficeQueueCard;
  /** The last slot shows how many more cards wait. */
  more?: number;
}

/** Where each card goes on the board (scene px), the last one turning into +N when more wait. */
export function cardSlots(w: Widget, cards: BoardCards, queue: OfficeQueueCard[]): CardSlot[] {
  const { shown, more } = stripSlots(queue, cards.max);
  const [gx, gy] = cards.gap ?? [4, 4];
  const at = (i: number): [number, number] => [
    w.at[0] + cards.at[0] + (i % cards.columns) * (cards.size[0] + gx),
    w.at[1] + cards.at[1] + Math.floor(i / cards.columns) * (cards.size[1] + gy),
  ];
  const slots: CardSlot[] = shown.map((card, i) => ({ at: at(i), card }));
  return more ? [...slots, { at: at(shown.length), more }] : slots;
}

/** Card text: `{taskId}` by default; the reserved member after an arrow. */
export function cardText(cards: BoardCards, c: OfficeQueueCard): string {
  const base = fillTemplate(cards.text?.value ?? "{taskId}", { threadNo: c.thread_no, taskId: c.id, role: c.role ?? "", reservedFor: c.reserved_for ?? "" });
  return c.reserved_for && !(cards.text?.value ?? "").includes("{reservedFor}") ? `${base}→${c.reserved_for}` : base;
}

function cardLabel(c: OfficeQueueCard): string {
  return [`#${c.thread_no} ${c.id}`, c.locked ? "等依赖" : "", c.reserved_for ? `预留给 ${c.reserved_for}` : "", c.assignee ? `点名 ${c.assignee}` : ""].filter(Boolean).join("，");
}

function QueueCards({ widget: w, cards, scene, queue }: { widget: Widget; cards: BoardCards; scene: Scene; queue: OfficeQueueCard[] }) {
  const l = scene.layout;
  const c = ctxOf(l);
  const [w0, h0] = cards.size;
  const box = (at: [number, number], bg: string): CSSProperties => ({
    position: "absolute", left: at[0], top: at[1], width: w0, height: h0, boxSizing: "border-box", display: "flex", alignItems: "center", gap: 2,
    padding: "0 3px", overflow: "hidden", whiteSpace: "nowrap", background: bg, color: colorOr(cards.text?.color, c, token(l, "labelText")),
    fontFamily: font(l, cards.text?.font), fontSize: cards.text?.size ?? 8, lineHeight: "normal",
  });
  const muted = token(l, "labelMuted");
  return (
    <div role="list" aria-label="任务队列" style={sx({ position: "absolute", left: 0, top: 0 })}>
      {cardSlots(w, cards, queue).map((s, i) =>
        s.card ? (
          <span role="listitem" key={`${s.card.thread_no}-${s.card.id}`} title={cardLabel(s.card)} aria-label={cardLabel(s.card)} style={sx({ ...box(s.at, colorOr(cards.color ?? "{threadColor}", { ...c, vars: { threadColor: scene.threadColors.get(s.card.thread_no) ?? muted } })), opacity: s.card.locked ? 0.75 : 1 })}>
            {s.card.locked && <LockIcon />}
            {cardText(cards, s.card)}
          </span>
        ) : (
          <span role="listitem" key={`more-${i}`} aria-label={`还有 ${s.more} 个`} style={sx({ ...box(s.at, muted), justifyContent: "center" })}>+{s.more}</span>
        ),
      )}
    </div>
  );
}

function LockIcon() {
  return (
    <svg width="7" height="8" viewBox="0 0 7 8" aria-hidden="true" style={sx({ flex: "none" })}>
      <rect x="0" y="3" width="7" height="5" fill="currentColor" />
      <rect x="1.5" y="0" width="4" height="4" fill="none" stroke="currentColor" stroke-width="1.2" />
    </svg>
  );
}

function RackLabels({ widget: w, scene, data, onAction }: { widget: Widget } & OverlayProps) {
  const l = scene.layout;
  const nodes = data.nodes ?? [];
  const shown = visibleRacks(w, nodes);
  const sprite = w.sprite ? l.sprites[w.sprite] : undefined;
  const [lx, ly] = w.label?.at ?? [0, (sprite?.h ?? 100) + 6];
  const rest = nodes.length - shown.length;
  const last = rackPosition(w, shown.length);
  return (
    <>
      {shown.map((n, i) => {
        const [x, y] = rackPosition(w, i);
        const label = fillTemplate(w.label?.value ?? "{name}", { name: n.name, active: n.active, capacity: n.capacity });
        const width = (sprite?.w ?? 56) - lx * 2;
        return (
          <span key={n.id}>
            <span style={sx({ position: "absolute", left: x + lx, top: y + ly, width, display: "flex", justifyContent: "center" })}>
              <span style={sx({ ...textBox(l, { ...w.label, padding: [1, 4] }), lineHeight: "normal", opacity: n.online ? 1 : 0.6 })}>{label}</span>
            </span>
            {w.click !== "none" && clickTarget(onAction && (() => onAction({ type: "openNode", nodeId: n.id })), label, [x, y, sprite?.w ?? 56, sprite?.h ?? 120])}
          </span>
        );
      })}
      {rest > 0 && (
        <span style={sx({ position: "absolute", left: last[0], top: last[1], ...textBox(l, { ...w.label, padding: [1, 4] }) })}>
          {fillTemplate(w.overflowLabel ?? "+{rest}", { rest })}
        </span>
      )}
    </>
  );
}

function PipelinePanel({ widget: w, scene, data, clock, reducedMotion, onAction }: { widget: Widget } & OverlayProps) {
  const l = scene.layout;
  const c = ctxOf(l);
  const st = w.style ?? {};
  const style: CSSProperties = {
    position: "absolute", left: w.at[0], top: w.at[1], width: w.w, boxSizing: "border-box",
    background: colorOr(st.bg, c, "#14161B"), border: `2px solid ${colorOr(st.border, c, "#5C5F6B")}`,
    color: colorOr(st.color, c, "#E6E3DC"), padding: st.padding ?? 8, display: "flex", flexDirection: "column",
    gap: st.gap ?? 6, fontSize: st.size ?? 10, lineHeight: "normal", pointerEvents: "auto",
    cursor: w.click !== "none" && onAction ? "pointer" : "default",
  };
  const click = w.click !== "none" && onAction ? () => onAction({ type: "openMergeQueue" }) : undefined;
  return (
    <div style={sx(style)} onClick={click} role={click ? "button" : undefined} aria-label="合并队列与 CI">
      {(w.sections ?? []).map((s, i) => (
        <PipelineSectionView key={i} section={s} first={i === 0} titleColor={colorOr(st.title, c, "#F2C14E")} layout={l} data={data} clock={clock} still={reducedMotion} />
      ))}
    </div>
  );
}

function PipelineSectionView(p: { section: PipelineSection; first: boolean; titleColor: string; layout: OfficeLayout; data: OfficeWidgetData; clock: number; still: boolean }) {
  const { section: s, layout } = p;
  const c = ctxOf(layout);
  const title = <span style={sx({ color: p.titleColor, marginTop: p.first ? 0 : 4 })}>{s.title}</span>;
  if (s.kind === "mergeQueue") {
    const rows = (p.data.mergeQueue ?? []).slice(0, s.maxItems ?? 4);
    return (
      <>
        {title}
        {rows.length === 0 && <span style={sx({ color: "#7E8190" })}>空</span>}
        {rows.map((r, i) => (
          <span key={i} style={sx({ display: "flex", justifyContent: "space-between", gap: 6 })}>
            <span>{fillTemplate(s.row ?? "#{threadNo} {employee}", { threadNo: r.thread_no, employee: r.employee, taskId: r.task_id })}</span>
            <span style={sx({ color: colorOr(s.stateColors?.[r.status], c, "#A8A49B") })}>{s.stateLabels?.[r.status] ?? r.status}</span>
          </span>
        ))}
      </>
    );
  }
  const checks = (p.data.ci ?? []).slice(0, s.maxItems ?? 4);
  return (
    <>
      {title}
      <span style={sx({ display: "flex", flexWrap: "wrap", gap: 8, alignItems: "center" })}>
        {checks.length === 0 && <span style={sx({ color: "#7E8190" })}>无</span>}
        {checks.map((ch) => {
          const key = ch.conclusion || (ch.status === "completed" ? "none" : ch.status) || "none";
          const blinking = (s.blink ?? []).includes(key) && !p.still && !blinkOn(0.5, p.clock);
          const color = blinking ? colorOr(s.colors?.none, c, "#5C5F6B") : colorOr(s.colors?.[key] ?? s.colors?.none, c, "#5C5F6B");
          return (
            <span key={ch.name} style={sx({ display: "flex", alignItems: "center", gap: 4 })}>
              <span style={sx({ width: 10, height: 10, background: color })} />
              {ch.name}
            </span>
          );
        })}
      </span>
    </>
  );
}

function Pager({ scene, actors, onPage }: OverlayProps) {
  if (scene.page.count < 2) return null;
  const [ax, ay, aw] = scene.layout.threadPods.area.rect;
  const offPage = actors.filter((a) => a.badge !== undefined);
  const hand = offPage.some((a) => a.status.state === "raising_hand");
  const hidden = offPage.filter((a) => a.hidden).length;
  const btn: CSSProperties = { pointerEvents: "auto", border: "none", background: "transparent", cursor: "pointer", font: "inherit", color: "inherit", padding: "0 3px" };
  const { index, count } = scene.page;
  return (
    <span style={sx({ position: "absolute", right: scene.width - (ax + aw), top: ay - 26, display: "flex", alignItems: "center", gap: 4, padding: "1px 6px", background: token(scene.layout, "labelDark"), color: "#E6E3DC", fontSize: 10 })}>
      {hand && <span title="其他页有人举手" style={sx({ width: 6, height: 6, background: "#F2A65E" })} />}
      {hidden > 0 && <span style={sx({ color: "#A8A49B" })}>+{hidden} 人</span>}
      <button type="button" style={sx(btn)} aria-label="上一页" disabled={index === 0} onClick={() => onPage(index - 1)}>‹</button>
      {index + 1}/{count}
      <button type="button" style={sx(btn)} aria-label="下一页" disabled={index === count - 1} onClick={() => onPage(index + 1)}>›</button>
    </span>
  );
}

function ActorTag({ actor: a, scene, looks, focus, selected, registerActor, onSelect }: { actor: ActorView } & OverlayProps) {
  const l = scene.layout;
  const ch = l.characters.person;
  const look = looks.get(a.id);
  if (a.hidden || !look) return null;
  const tag = l.labels?.nameTag;
  const ax = ch.anchor?.x ?? (ch.width * ch.pixel) / 2;
  const tagBg = colorOr(tag?.bg ?? "@tag", { ...ctxOf(l), slots: look.slots }, look.look.color);
  const work = workTag(a);
  // Seated, the desk (its own `#18 · t5` tag, or the thread's table) says what it works for; away, the name tag does.
  const away = Boolean(work) && !a.atDesk;
  const label = `${a.employee.name}，${work ? `在 ${work} ` : ""}${STATE_LABELS[a.status.state] ?? a.status.state}${a.bubble ? `：${a.bubble.text}` : ""}`;
  const isSel = selected === a.id;
  const desk = a.atDesk ? scene.staff.get(a.id) : undefined;
  const dark = token(l, "labelDark");
  const light = token(l, "labelText");
  return (
    <div
      ref={(el) => registerActor(a.id, el as HTMLElement | null)}
      style={sx({
        position: "absolute", left: 0, top: 0, transform: `translate(${a.pos[0]}px, ${a.pos[1]}px)`, zIndex: a.walking ? Z.walkingTag : Z.tag,
        opacity: a.opacity * dimFor(l, focus, a.thread), willChange: "transform",
      })}
    >
      <button
        type="button"
        aria-label={label}
        aria-pressed={isSel}
        onClick={(e) => {
          e.stopPropagation();
          onSelect(a.id);
        }}
        style={sx({
          position: "absolute", left: 0, top: -4, width: ch.width * ch.pixel, height: PERSON_BOX_H - 16, pointerEvents: "auto", cursor: "pointer",
          background: "transparent", border: "none", padding: 0,
          outline: isSel ? "2px solid #F2C14E" : "none", outlineOffset: 2,
        })}
      />
      {a.bubble && <SpeechBubble layout={l} bubble={a.bubble} x={ax} fit={desk && staffBubbleFit(scene, desk)} />}
      <span
        style={sx({
          position: "absolute", top: tag?.offset?.[1] ?? 60, left: ax + (tag?.offset?.[0] ?? 0), transform: "translateX(-50%)",
          display: "flex", alignItems: "center", gap: 3, whiteSpace: "nowrap", padding: "1px 4px",
          background: a.grayscale ? "#8C8A84" : tagBg, color: colorOr(tag?.color, ctxOf(l), "#FFFFFF"),
          fontFamily: font(l, tag?.font), fontSize: tag?.size ?? 9, lineHeight: "13px",
          boxShadow: isSel ? "0 0 0 2px #F2C14E" : undefined,
        })}
      >
        {tag?.dot !== undefined && <span style={sx({ width: 6, height: 6, background: colorOr(tag.dot, ctxOf(l), "#FFFFFF") })} />}
        {a.employee.name}
        {a.badge !== undefined && !work && (
          <span style={sx({ marginLeft: 2, padding: "0 3px", background: scene.threadColors.get(a.badge) ?? dark, color: light })}>#{a.badge}</span>
        )}
        {away && (
          <span style={sx({ marginLeft: 2, padding: "0 3px", background: scene.threadColors.get(a.thread!) ?? dark, color: light, boxShadow: "0 0 0 1px rgba(255,255,255,0.6)" })}>#{a.thread}</span>
        )}
      </span>
    </div>
  );
}

/** `#18 · t5`: the thread and task a member works on now (none for the coordinator and leads). */
export function workTag(a: Pick<ActorView, "employee" | "status" | "thread">): string | undefined {
  if (!isMember(a.employee) || a.thread === null || !a.status.task_id) return undefined;
  return `#${a.thread} · ${a.status.task_id}`;
}

export interface BubbleFit {
  /** Widest the bubble may be (scene px); longer text ends in an ellipsis, the full text is its title. */
  maxWidth: number;
  /** Raised this much (scene px) so the bubbles of neighbours do not overlap. */
  lift: number;
}

const BUBBLE_LIFT = 20;

/**
 * A bubble over a staff desk, so the bubbles of neighbours (one cell apart) stay readable: about
 * two cells wide at most, every other column raised. The first row has the thread tables' name
 * tags right above it, so there nothing is raised and bubbles stay one cell wide.
 */
export function staffBubbleFit(scene: Scene, desk: DeskInstance): BubbleFit | undefined {
  const area = scene.layout.staffArea;
  if (!area) return undefined;
  const cell = area.cell[0] * desk.scale;
  if (!desk.row) return { maxWidth: Math.max(40, cell - 6), lift: 0 };
  return { maxWidth: Math.max(40, 2 * cell - 6), lift: (desk.col ?? 0) % 2 ? BUBBLE_LIFT : 0 };
}

function SpeechBubble({ layout: l, bubble, x, fit }: { layout: OfficeLayout; bubble: NonNullable<ActorView["bubble"]>; x: number; fit?: BubbleFit }) {
  const b = l.bubbles;
  const kind = b?.kinds?.[bubble.kind] ?? b?.kinds?.plain ?? { bg: "#FFFFFF", border: "#3B3A36" };
  const c = ctxOf(l);
  const [ox, oy] = b?.offset ?? [0, -82];
  const max = b?.maxChars ?? 24;
  const text = [...bubble.text].length > max ? [...bubble.text].slice(0, max - 1).join("") + "…" : bubble.text;
  return (
    <span
      style={sx({
        position: "absolute", top: PERSON_BOX_H + oy - (fit?.lift ?? 0), left: x + ox, transform: "translate(-50%, -100%)", whiteSpace: "nowrap",
        // Capped bubbles take the pointer so their title (the full text) shows on hover.
        ...(fit ? { maxWidth: fit.maxWidth, overflow: "hidden", textOverflow: "ellipsis", boxSizing: "border-box", pointerEvents: "auto" } : {}),
        padding: "3px 7px", background: colorOr(kind.bg, c), border: `2px solid ${colorOr(kind.border, c)}`,
        color: colorOr(kind.color, c, "#1F1E1C"), fontFamily: font(l, b?.font ?? "bubble"), fontSize: b?.size ?? 11, lineHeight: "15px",
        boxShadow: b?.shadow ?? "2px 2px 0 rgba(0,0,0,0.15)",
      })}
      title={bubble.text}
    >
      {text}
    </span>
  );
}
