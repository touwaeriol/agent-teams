// DOM overlay (office-layout.md §5): every text in the office — room names,
// pod plates, name tags, bubbles, timers, widget text — plus the click
// targets. Same transform as the canvas (scene px × scale).
import { memo, type CSSProperties, type ReactNode } from "react";
import { colorOr, type ColorContext } from "./color";
import type { PodInstance, Scene } from "./compile";
import type { ActorView, DeskState } from "./director";
import type { ResolvedLook } from "./looks";
import { blinkOn, dimFor, rackPosition, visibleRacks } from "./render";
import { fillTemplate } from "./template";
import type { OfficeLayout, OfficeWidgetData, PipelineSection, TextStyle, Widget } from "./types";

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

export const OfficeOverlay = memo(function OfficeOverlay(p: OverlayProps) {
  const l = p.scene.layout;
  return (
    <div style={{ position: "absolute", inset: 0, pointerEvents: "none", fontFamily: font(l, "label") }}>
      <RoomLabels layout={l} scene={p.scene} />
      {p.scene.pods.map((pod) => <Plate key={pod.threadNo} pod={pod} {...p} />)}
      <DeskTimers {...p} />
      {(l.widgets ?? []).map((w) => <WidgetOverlay key={w.id} widget={w} {...p} />)}
      <Pager {...p} />
      {p.actors.map((a) => <ActorTag key={a.id} actor={a} {...p} />)}
    </div>
  );
});

function RoomLabels({ layout, scene }: { layout: OfficeLayout; scene: Scene }) {
  const style = layout.labels?.room;
  const [ox, oy] = style?.offset ?? [8, 6];
  return (
    <>
      {scene.rooms.filter((r) => !r.labelStyle?.hidden).map((r) => (
        <span
          key={r.id}
          style={{
            position: "absolute", left: r.rect[0] + ox, top: r.rect[1] + oy,
            ...textBox(layout, { ...style, bg: r.labelStyle?.bg ?? "#8A6A45", color: r.labelStyle?.fg ?? "#FFFFFF" }),
            lineHeight: "normal",
          }}
        >
          {r.label}
        </span>
      ))}
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
      style={{
        position: "absolute", left: pod.plate.at[0], top: pod.plate.at[1], pointerEvents: "auto", cursor: clickable ? "pointer" : "default",
        ...textBox(l, pod.plate.style, { vars: { threadColor: pod.color } }),
        lineHeight: "normal",
        opacity: dimFor(l, focus, pod.threadNo),
      }}
    >
      {pod.plate.text}
    </button>
  );
}

function DeskTimers({ scene, desks, now, focus }: OverlayProps) {
  const l = scene.layout;
  const style = l.labels?.timer;
  const out: ReactNode[] = [];
  for (const d of scene.desks.values()) {
    const since = desks.get(d.key)?.timerSince;
    if (since === undefined || !d.timer) continue;
    out.push(
      <span key={d.key} style={{ position: "absolute", left: d.timer[0], top: d.timer[1], ...textBox(l, { ...style, padding: [0, 3] }), lineHeight: "13px", opacity: dimFor(l, focus, d.threadNo) }}>
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
      style={{ position: "absolute", left: rect[0], top: rect[1], width: rect[2], height: rect[3], pointerEvents: "auto", background: "transparent", border: "none", cursor: "pointer" }}
    />
  );
}

function TaskBoardText({ widget: w, scene, data, focus, onAction }: { widget: Widget } & OverlayProps) {
  const l = scene.layout;
  const sprite = w.sprite ? l.sprites[w.sprite] : undefined;
  const claimable = focus !== null ? data.board?.by_thread?.[String(focus)] ?? 0 : data.board?.claimable ?? 0;
  const text = fillTemplate(w.text?.value ?? "{claimable}", { claimable });
  const [tx, ty] = w.text?.at ?? [0, 0];
  return (
    <>
      <span style={{ position: "absolute", left: w.at[0] + tx, top: w.at[1] + ty, ...textBox(l, { ...w.text, padding: [0, 0] }), lineHeight: "normal" }}>{text}</span>
      {w.click !== "none" && sprite && clickTarget(onAction && (() => onAction({ type: "openTaskPool" })), text, [w.at[0], w.at[1], sprite.w, sprite.h])}
    </>
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
            <span style={{ position: "absolute", left: x + lx, top: y + ly, width, display: "flex", justifyContent: "center" }}>
              <span style={{ ...textBox(l, { ...w.label, padding: [1, 4] }), lineHeight: "normal", opacity: n.online ? 1 : 0.6 }}>{label}</span>
            </span>
            {w.click !== "none" && clickTarget(onAction && (() => onAction({ type: "openNode", nodeId: n.id })), label, [x, y, sprite?.w ?? 56, sprite?.h ?? 120])}
          </span>
        );
      })}
      {rest > 0 && (
        <span style={{ position: "absolute", left: last[0], top: last[1], ...textBox(l, { ...w.label, padding: [1, 4] }) }}>
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
    <div style={style} onClick={click} role={click ? "button" : undefined} aria-label="合并队列与 CI">
      {(w.sections ?? []).map((s, i) => (
        <PipelineSectionView key={i} section={s} first={i === 0} titleColor={colorOr(st.title, c, "#F2C14E")} layout={l} data={data} clock={clock} still={reducedMotion} />
      ))}
    </div>
  );
}

function PipelineSectionView(p: { section: PipelineSection; first: boolean; titleColor: string; layout: OfficeLayout; data: OfficeWidgetData; clock: number; still: boolean }) {
  const { section: s, layout } = p;
  const c = ctxOf(layout);
  const title = <span style={{ color: p.titleColor, marginTop: p.first ? 0 : 4 }}>{s.title}</span>;
  if (s.kind === "mergeQueue") {
    const rows = (p.data.mergeQueue ?? []).slice(0, s.maxItems ?? 4);
    return (
      <>
        {title}
        {rows.length === 0 && <span style={{ color: "#7E8190" }}>空</span>}
        {rows.map((r, i) => (
          <span key={i} style={{ display: "flex", justifyContent: "space-between", gap: 6 }}>
            <span>{fillTemplate(s.row ?? "#{threadNo} {employee}", { threadNo: r.thread_no, employee: r.employee, taskId: r.task_id })}</span>
            <span style={{ color: colorOr(s.stateColors?.[r.status], c, "#A8A49B") }}>{s.stateLabels?.[r.status] ?? r.status}</span>
          </span>
        ))}
      </>
    );
  }
  const checks = (p.data.ci ?? []).slice(0, s.maxItems ?? 4);
  return (
    <>
      {title}
      <span style={{ display: "flex", flexWrap: "wrap", gap: 8, alignItems: "center" }}>
        {checks.length === 0 && <span style={{ color: "#7E8190" }}>无</span>}
        {checks.map((ch) => {
          const key = ch.conclusion || (ch.status === "completed" ? "none" : ch.status) || "none";
          const blinking = (s.blink ?? []).includes(key) && !p.still && !blinkOn(0.5, p.clock);
          const color = blinking ? colorOr(s.colors?.none, c, "#5C5F6B") : colorOr(s.colors?.[key] ?? s.colors?.none, c, "#5C5F6B");
          return (
            <span key={ch.name} style={{ display: "flex", alignItems: "center", gap: 4 }}>
              <span style={{ width: 10, height: 10, background: color }} />
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
    <span style={{ position: "absolute", right: scene.width - (ax + aw), top: ay - 26, display: "flex", alignItems: "center", gap: 4, padding: "1px 6px", background: "#23262D", color: "#E6E3DC", fontSize: 10 }}>
      {hand && <span title="其他页有人举手" style={{ width: 6, height: 6, background: "#F2A65E" }} />}
      {hidden > 0 && <span style={{ color: "#A8A49B" }}>+{hidden} 人</span>}
      <button type="button" style={btn} aria-label="上一页" disabled={index === 0} onClick={() => onPage(index - 1)}>‹</button>
      {index + 1}/{count}
      <button type="button" style={btn} aria-label="下一页" disabled={index === count - 1} onClick={() => onPage(index + 1)}>›</button>
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
  const label = `${a.employee.name}，${STATE_LABELS[a.status.state] ?? a.status.state}${a.bubble ? `：${a.bubble.text}` : ""}`;
  const isSel = selected === a.id;
  return (
    <div
      ref={(el) => registerActor(a.id, el)}
      style={{ position: "absolute", left: 0, top: 0, transform: `translate(${a.pos[0]}px, ${a.pos[1]}px)`, opacity: a.opacity * dimFor(l, focus, a.employee.thread_no), willChange: "transform" }}
    >
      <button
        type="button"
        aria-label={label}
        aria-pressed={isSel}
        onClick={(e) => {
          e.stopPropagation();
          onSelect(a.id);
        }}
        style={{
          position: "absolute", left: 0, top: -4, width: ch.width * ch.pixel, height: PERSON_BOX_H - 16, pointerEvents: "auto", cursor: "pointer",
          background: "transparent", border: "none", padding: 0,
          outline: isSel ? "2px solid #F2C14E" : "none", outlineOffset: 2,
        }}
      />
      {a.bubble && <SpeechBubble layout={l} bubble={a.bubble} x={ax} />}
      <span
        style={{
          position: "absolute", top: tag?.offset?.[1] ?? 60, left: ax + (tag?.offset?.[0] ?? 0), transform: "translateX(-50%)",
          display: "flex", alignItems: "center", gap: 3, whiteSpace: "nowrap", padding: "1px 4px",
          background: a.grayscale ? "#8C8A84" : tagBg, color: colorOr(tag?.color, ctxOf(l), "#FFFFFF"),
          fontFamily: font(l, tag?.font), fontSize: tag?.size ?? 9, lineHeight: "13px",
          boxShadow: isSel ? "0 0 0 2px #F2C14E" : undefined,
        }}
      >
        {tag?.dot !== undefined && <span style={{ width: 6, height: 6, background: colorOr(tag.dot, ctxOf(l), "#FFFFFF") }} />}
        {a.employee.name}
        {a.badge !== undefined && (
          <span style={{ marginLeft: 2, padding: "0 3px", background: scene.threadColors.get(a.badge) ?? "#23262D", color: "#FFFFFF" }}>#{a.badge}</span>
        )}
      </span>
    </div>
  );
}

function SpeechBubble({ layout: l, bubble, x }: { layout: OfficeLayout; bubble: NonNullable<ActorView["bubble"]>; x: number }) {
  const b = l.bubbles;
  const kind = b?.kinds?.[bubble.kind] ?? b?.kinds?.plain ?? { bg: "#FFFFFF", border: "#3B3A36" };
  const c = ctxOf(l);
  const [ox, oy] = b?.offset ?? [0, -82];
  const max = b?.maxChars ?? 24;
  const text = [...bubble.text].length > max ? [...bubble.text].slice(0, max - 1).join("") + "…" : bubble.text;
  return (
    <span
      style={{
        position: "absolute", top: PERSON_BOX_H + oy, left: x + ox, transform: "translate(-50%, -100%)", whiteSpace: "nowrap",
        padding: "3px 7px", background: colorOr(kind.bg, c), border: `2px solid ${colorOr(kind.border, c)}`,
        color: colorOr(kind.color, c, "#1F1E1C"), fontFamily: font(l, b?.font ?? "bubble"), fontSize: b?.size ?? 11, lineHeight: "15px",
        boxShadow: b?.shadow ?? "2px 2px 0 rgba(0,0,0,0.15)",
      }}
      title={bubble.text}
    >
      {text}
    </span>
  );
}
