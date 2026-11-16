// Canvas 2D renderer (office-layout.md §5): a static layer (floors, walls,
// pod frames, furniture) drawn once per scene, and a dynamic layer per frame
// (desk screens, rack LEDs, people). Pixel crisp: smoothing off.
import { colorOr, resolveColor, type ColorContext } from "./color";
import type { DeskInstance, PlacedSprite, PodInstance, Scene } from "./compile";
import type { ActorView, DeskState } from "./director";
import { frameBitmap, type ResolvedLook } from "./looks";
import type { Floor, OfficeLayout, OfficeNode, Point, Room, Shape, Sprite, Widget } from "./types";

type Ctx = CanvasRenderingContext2D;

export interface ViewOptions {
  focus: number | null;
  reducedMotion?: boolean;
}

export function palette(layout: OfficeLayout): ColorContext {
  return { palette: layout.palette };
}

export function dimFor(layout: OfficeLayout, focus: number | null, threadNo: number | null | undefined): number {
  if (focus === null || threadNo === null || threadNo === undefined || threadNo === focus) return 1;
  return layout.focus?.dimOpacity ?? 0.3;
}

// ---------- shapes & sprites ----------

function fillShape(ctx: Ctx, s: Shape, x: number, y: number, k: number, color: string): void {
  ctx.fillStyle = color;
  if (s.type === "ellipse") {
    ctx.beginPath();
    ctx.ellipse(x + (s.w * k) / 2, y + (s.h * k) / 2, (s.w * k) / 2, (s.h * k) / 2, 0, 0, Math.PI * 2);
    ctx.fill();
    return;
  }
  if (s.radius) {
    ctx.beginPath();
    ctx.roundRect(x, y, s.w * k, s.h * k, Array.isArray(s.radius) ? s.radius.map((r) => r * k) : s.radius * k);
    ctx.fill();
    return;
  }
  ctx.fillRect(x, y, s.w * k, s.h * k);
}

/** Border inside the box (CSS box-sizing: border-box), like the design. */
function strokeShape(ctx: Ctx, s: Shape, x: number, y: number, k: number, color: string): void {
  const w = (s.strokeWidth ?? 1) * k;
  ctx.fillStyle = color;
  if (s.type === "ellipse" || s.radius) {
    ctx.strokeStyle = color;
    ctx.lineWidth = w;
    ctx.beginPath();
    if (s.type === "ellipse") ctx.ellipse(x + (s.w * k) / 2, y + (s.h * k) / 2, (s.w * k - w) / 2, (s.h * k - w) / 2, 0, 0, Math.PI * 2);
    else ctx.roundRect(x + w / 2, y + w / 2, s.w * k - w, s.h * k - w, Array.isArray(s.radius) ? s.radius.map((r) => r * k) : (s.radius ?? 0) * k);
    ctx.stroke();
    return;
  }
  const [W, H] = [s.w * k, s.h * k];
  ctx.fillRect(x, y, W, w);
  ctx.fillRect(x, y + H - w, W, w);
  ctx.fillRect(x, y, w, H);
  ctx.fillRect(x + W - w, y, w, H);
}

/** True when a shape uses a runtime slot (redrawn on the dynamic layer). */
export function isDynamicShape(s: Shape, runtime: Set<string>): boolean {
  return [s.fill, s.stroke].some((c) => c?.startsWith("@") && runtime.has(c.slice(1)));
}

export function drawSprite(
  ctx: Ctx,
  sprite: Sprite,
  at: Point,
  color: ColorContext,
  opts: { scale?: number; flip?: boolean; only?: "static" | "dynamic"; runtime?: Set<string> } = {},
): void {
  const k = opts.scale ?? 1;
  ctx.save();
  if (opts.flip) {
    ctx.translate(at[0] * 2 + sprite.w * k, 0);
    ctx.scale(-1, 1);
  }
  if (sprite.kind === "pixels") drawPixelSprite(ctx, sprite, at, color, k);
  if (sprite.kind === "image") drawImageSprite(ctx, sprite, at, k);
  for (const s of sprite.shapes ?? []) {
    const dynamic = opts.runtime ? isDynamicShape(s, opts.runtime) : false;
    if ((opts.only === "static" && dynamic) || (opts.only === "dynamic" && !dynamic)) continue;
    const x = at[0] + s.x * k;
    const y = at[1] + s.y * k;
    const fill = resolveColor(s.fill, color);
    if (fill) fillShape(ctx, s, x, y, k, fill);
    const stroke = resolveColor(s.stroke, color);
    if (stroke && (s.strokeWidth ?? 1) > 0) strokeShape(ctx, s, x, y, k, stroke);
  }
  ctx.restore();
}

function drawPixelSprite(ctx: Ctx, sprite: Sprite, at: Point, color: ColorContext, k: number): void {
  const px = (sprite.pixel ?? 1) * k;
  (sprite.rows ?? []).forEach((row, y) =>
    [...row].forEach((ch, x) => {
      const c = ch === "." ? undefined : resolveColor(sprite.colors?.[ch], color);
      if (!c) return;
      ctx.fillStyle = c;
      ctx.fillRect(at[0] + x * px, at[1] + y * px, px, px);
    }),
  );
}

const images = new Map<string, HTMLImageElement>();
let onImageLoad: (() => void) | undefined;

/** Called when an image used by a sprite or floor finishes loading. */
export function setImageLoadHandler(fn: (() => void) | undefined): void {
  onImageLoad = fn;
}

function image(src: string): HTMLImageElement | undefined {
  let img = images.get(src);
  if (!img) {
    img = new Image();
    img.onload = () => onImageLoad?.();
    img.src = src;
    images.set(src, img);
  }
  return img.complete && img.naturalWidth ? img : undefined;
}

function drawImageSprite(ctx: Ctx, sprite: Sprite, at: Point, k: number, time = performance.now()): void {
  const img = sprite.src ? image(sprite.src) : undefined;
  if (!img) return;
  const frames = sprite.frames ?? 1;
  const fw = img.naturalWidth / frames;
  const f = frames > 1 ? Math.floor((time / 1000) * (sprite.fps ?? 4)) % frames : 0;
  ctx.drawImage(img, f * fw, 0, fw, img.naturalHeight, at[0], at[1], sprite.w * k, sprite.h * k);
}

// ---------- static layer ----------

function drawFloor(ctx: Ctx, floor: Floor | undefined, rect: number[], color: ColorContext): void {
  const [x, y, w, h] = rect;
  ctx.fillStyle = colorOr(floor?.base, color, "#EDE6D8");
  ctx.fillRect(x, y, w, h);
  if (!floor) return;
  ctx.save();
  ctx.beginPath();
  ctx.rect(x, y, w, h);
  ctx.clip();
  floorPatterns[floor.pattern]?.(ctx, floor, rect, color);
  ctx.restore();
}

const floorPatterns: Record<Floor["pattern"], (ctx: Ctx, f: Floor, r: number[], c: ColorContext) => void> = {
  plain: () => {},
  planks: (ctx, f, r, c) => {
    lines(ctx, r, "y", f.stepY ?? 28, f.lineWidth ?? 2, colorOr(f.line, c));
    lines(ctx, r, "x", f.stepX ?? 88, f.lineWidth ?? 2, colorOr(f.line2 ?? f.line, c));
  },
  grid: (ctx, f, r, c) => {
    lines(ctx, r, "y", f.stepY ?? 24, f.lineWidth ?? 2, colorOr(f.line, c));
    lines(ctx, r, "x", f.stepX ?? 24, f.lineWidth ?? 2, colorOr(f.line2 ?? f.line, c));
  },
  checker: (ctx, f, r, c) => {
    const [sx, sy] = [f.stepX ?? 16, f.stepY ?? 16];
    ctx.fillStyle = colorOr(f.line, c);
    for (let y = r[1], j = 0; y < r[1] + r[3]; y += sy, j++)
      for (let x = r[0], i = 0; x < r[0] + r[2]; x += sx, i++) if ((i + j) % 2 === 0) ctx.fillRect(x, y, sx, sy);
  },
  image: (ctx, f, r) => {
    const img = f.src ? image(f.src) : undefined;
    if (!img) return;
    ctx.fillStyle = ctx.createPattern(img, "repeat") ?? "transparent";
    ctx.fillRect(r[0], r[1], r[2], r[3]);
  },
};

function lines(ctx: Ctx, r: number[], axis: "x" | "y", step: number, width: number, color: string): void {
  ctx.fillStyle = color;
  const [x, y, w, h] = r;
  if (axis === "y") for (let p = y; p < y + h; p += step) ctx.fillRect(x, p, w, width);
  else for (let p = x; p < x + w; p += step) ctx.fillRect(p, y, width, h);
}

/** Wall segments of one side with door gaps cut out: [start, end] along the side. */
export function wallSpans(room: Room, side: "n" | "s" | "e" | "w"): [number, number][] {
  const len = side === "n" || side === "s" ? room.rect[2] : room.rect[3];
  const gaps = (room.doors ?? [])
    .filter((d) => d.side === side && d.draw !== false)
    .map((d): [number, number] => [d.offset - (d.width ?? 24) / 2, d.offset + (d.width ?? 24) / 2])
    .sort((a, b) => a[0] - b[0]);
  const spans: [number, number][] = [];
  let cur = 0;
  for (const [a, b] of gaps) {
    if (a > cur) spans.push([cur, a]);
    cur = Math.max(cur, b);
  }
  if (cur < len) spans.push([cur, len]);
  return spans;
}

function drawWalls(ctx: Ctx, room: Room, color: ColorContext): void {
  const ww = room.wall?.width ?? 4;
  if (!ww) return;
  ctx.fillStyle = colorOr(room.wall?.color, color, "#8A6A45");
  const dashed = room.wall?.style === "dashed";
  const [x, y, w, h] = room.rect;
  const rects: Record<string, (a: number, b: number) => number[]> = {
    n: (a, b) => [x + a, y, b - a, ww],
    s: (a, b) => [x + a, y + h - ww, b - a, ww],
    w: (a, b) => [x, y + a, ww, b - a],
    e: (a, b) => [x + w - ww, y + a, ww, b - a],
  };
  for (const side of ["n", "s", "w", "e"] as const)
    for (const [a, b] of wallSpans(room, side)) for (const [sa, sb] of dashed ? dashes(a, b, ww) : [[a, b]]) {
      const [rx, ry, rw, rh] = rects[side](sa, sb);
      ctx.fillRect(rx, ry, rw, rh);
    }
}

function dashes(a: number, b: number, w: number): [number, number][] {
  const out: [number, number][] = [];
  for (let p = a; p < b; p += w * 5) out.push([p, Math.min(b, p + w * 3)]);
  return out;
}

function drawPodFrame(ctx: Ctx, pod: PodInstance, focused: boolean, color: ColorContext): void {
  const f = pod.frame;
  const w = f?.width ?? 2;
  if (!w) return;
  const vars = { ...color, vars: { threadColor: pod.color } };
  ctx.fillStyle = colorOr(focused ? f?.focusColor ?? f?.color : f?.color, vars, "#B08A5C");
  const [x, y, W, H] = pod.rect;
  const segs = (len: number) => (f?.style === "solid" ? [[0, len] as [number, number]] : dashes(0, len, w));
  for (const [a, b] of segs(W)) {
    ctx.fillRect(x + a, y, b - a, w);
    ctx.fillRect(x + a, y + H - w, b - a, w);
  }
  for (const [a, b] of segs(H)) {
    ctx.fillRect(x, y + a, w, b - a);
    ctx.fillRect(x + W - w, y + a, w, b - a);
  }
}

function runtimeSlots(item: PlacedSprite): Set<string> {
  return new Set(Object.keys(item.sprite.slots ?? {}));
}

export function drawStatic(ctx: Ctx, scene: Scene, view: ViewOptions): void {
  const layout = scene.layout;
  const color = palette(layout);
  ctx.fillStyle = colorOr(layout.canvas.background, color, "#EDE6D8");
  ctx.fillRect(0, 0, scene.width, scene.height);
  for (const room of scene.rooms) drawFloor(ctx, layout.floors?.[room.floor], room.rect, color);
  for (const room of scene.rooms) drawWalls(ctx, room, color);
  for (const pod of scene.pods) {
    ctx.globalAlpha = dimFor(layout, view.focus, pod.threadNo);
    drawPodFrame(ctx, pod, view.focus === pod.threadNo, color);
    for (const d of pod.desks) drawSprite(ctx, d.sprite, d.at, { ...color, slots: d.slots }, { scale: d.scale, only: "static", runtime: runtimeSlots(d) });
    ctx.globalAlpha = 1;
  }
  for (const f of scene.furniture) drawSprite(ctx, f.sprite, f.at, { ...color, slots: f.slots }, { flip: f.flip });
  for (const w of layout.widgets ?? []) if (w.type === "taskBoard" && w.sprite) drawSprite(ctx, layout.sprites[w.sprite], w.at, color);
}

// ---------- dynamic layer ----------

export interface DynamicInput {
  scene: Scene;
  actors: ActorView[];
  desks: Map<string, DeskState>;
  looks: Map<string, ResolvedLook>;
  nodes: OfficeNode[];
  time: number;
  view: ViewOptions;
}

export function drawDynamic(ctx: Ctx, input: DynamicInput): void {
  const { scene } = input;
  ctx.clearRect(0, 0, scene.width, scene.height);
  for (const pod of scene.pods) for (const d of pod.desks) drawDeskDynamic(ctx, input, d);
  for (const w of scene.layout.widgets ?? []) if (w.type === "nodeRacks") drawRacks(ctx, input, w);
  const anchorY = scene.layout.characters.person.anchor?.y ?? 0;
  const order = input.actors.filter((a) => !a.hidden).sort((a, b) => a.pos[1] + anchorY - (b.pos[1] + anchorY));
  for (const a of order) drawActor(ctx, input, a);
}

function drawDeskDynamic(ctx: Ctx, input: DynamicInput, d: DeskInstance): void {
  const color = palette(input.scene.layout);
  const screen = input.desks.get(d.key)?.screen;
  const slots = { ...d.slots, ...(screen ? { screen } : {}) };
  ctx.globalAlpha = dimFor(input.scene.layout, input.view.focus, d.threadNo);
  drawSprite(ctx, d.sprite, d.at, { ...color, slots }, { scale: d.scale, only: "dynamic", runtime: runtimeSlots(d) });
  ctx.globalAlpha = 1;
}

export type SlotState = "active" | "parked" | "queued" | "empty" | "offline";

/** LED states of one rack: active, parked, queued, then empty; extra capacity merges into the last LED. */
export function rackLights(node: OfficeNode, max: number): SlotState[] {
  const cap = Math.max(1, node.capacity);
  if (!node.online) return Array.from({ length: Math.min(cap, max) }, () => "offline");
  const seq: SlotState[] = [
    ...Array<SlotState>(node.active).fill("active"),
    ...Array<SlotState>(node.parked ?? 0).fill("parked"),
    ...Array<SlotState>(node.queued ?? 0).fill("queued"),
  ];
  while (seq.length < cap) seq.push("empty");
  if (seq.length <= max) return seq;
  const rest = seq.slice(max - 1);
  return [...seq.slice(0, max - 1), rest.find((s) => s !== "empty") ?? "empty"];
}

export function rackPosition(w: Widget, i: number): Point {
  const g = w.grid!;
  return [w.at[0] + (i % g.columns) * g.stepX, w.at[1] + Math.floor(i / g.columns) * g.stepY];
}

export function visibleRacks(w: Widget, nodes: OfficeNode[]): OfficeNode[] {
  return nodes.slice(0, w.grid?.maxItems ?? nodes.length);
}

function drawRacks(ctx: Ctx, input: DynamicInput, w: Widget): void {
  const layout = input.scene.layout;
  const color = palette(layout);
  const rack = w.sprite ? layout.sprites[w.sprite] : undefined;
  const slot = w.slot ? layout.sprites[w.slot.sprite] : undefined;
  visibleRacks(w, input.nodes).forEach((node, i) => {
    const at = rackPosition(w, i);
    if (rack) drawSprite(ctx, rack, at, color);
    if (!slot || !w.slot) return;
    rackLights(node, w.slot.max).forEach((state, j) => {
      const slots = ledSlots(w, state, input.time + i * 300, j, input.view.reducedMotion);
      drawSprite(ctx, slot, [at[0] + w.slot!.at[0], at[1] + w.slot!.at[1] + j * w.slot!.stepY], { ...color, slots });
    });
  });
}

function ledSlots(w: Widget, state: SlotState, time: number, j: number, still?: boolean): Record<string, string> {
  const c = w.slotColors?.[state] ?? w.slotColors?.empty ?? {};
  const off = w.slotColors?.empty?.led ?? "#2E323C";
  const slots: Record<string, string> = { led: c.led ?? off, led2: c.led2 ?? off };
  const blinkOff = !still && Math.floor(time / 700 + j) % 3 === 0;
  if (c.blink && blinkOff) slots[c.blink] = off;
  return slots;
}

/** Frame name of an animation at `time`, and its bob offset in CSS px. */
export function animFrame(layout: OfficeLayout, anim: string, start: number, time: number): { frame: string; bob: number } {
  const a = layout.characters.person.animations[anim] ?? layout.characters.person.animations.stand;
  const fps = a.fps ?? 1;
  const tick = Math.floor(((time - start) / 1000) * Math.max(fps, 0.0001));
  const frame = a.frames[((tick % a.frames.length) + a.frames.length) % a.frames.length];
  const bob = a.bob ? (Math.floor(((time - start) / 1000) * 2) % 2 ? -a.bob : 0) : 0;
  return { frame, bob };
}

export function blinkOn(fps: number | undefined, time: number): boolean {
  return !fps || Math.floor((time / 1000) * fps * 2) % 2 === 0;
}

function drawActor(ctx: Ctx, input: DynamicInput, a: ActorView): void {
  const layout = input.scene.layout;
  const look = input.looks.get(a.id);
  if (!look) return;
  const ch = layout.characters.person;
  const still = input.view.reducedMotion;
  const { frame, bob } = animFrame(layout, a.anim, a.animStart, still ? a.animStart : input.time);
  const ov = a.overlay ? ch.overlays?.[a.overlay] : undefined;
  const overlay = ov && (still || blinkOn(ov.blinkFps, input.time)) ? a.overlay : undefined;
  const bmp = frameBitmap(layout, look, frame, { overlay, grayscale: a.grayscale });
  const px = ch.pixel;
  ctx.globalAlpha = a.opacity * dimFor(layout, input.view.focus, a.employee.thread_no);
  ctx.save();
  const x = Math.round(a.pos[0]);
  const y = Math.round(a.pos[1]) + bob;
  if (a.flip) {
    ctx.translate(x * 2 + ch.width * px, 0);
    ctx.scale(-1, 1);
  }
  ctx.drawImage(bmp.canvas, x + bmp.bounds.minX * px, y + bmp.bounds.minY * px, bmp.bounds.w * px, bmp.bounds.h * px);
  ctx.restore();
  ctx.globalAlpha = 1;
}

/** Sets up a canvas for `scale` × devicePixelRatio with smoothing off. */
export function prepareCanvas(canvas: HTMLCanvasElement, w: number, h: number, scale: number): Ctx {
  const dpr = window.devicePixelRatio || 1;
  const bw = Math.round(w * scale * dpr);
  const bh = Math.round(h * scale * dpr);
  if (canvas.width !== bw) canvas.width = bw;
  if (canvas.height !== bh) canvas.height = bh;
  canvas.style.width = `${w * scale}px`;
  canvas.style.height = `${h * scale}px`;
  const ctx = canvas.getContext("2d")!;
  ctx.setTransform(scale * dpr, 0, 0, scale * dpr, 0, 0);
  ctx.imageSmoothingEnabled = false;
  return ctx;
}
