// Pure scene compiler (office-layout.md §4.4): layout + visible threads +
// employees → rooms, furniture, pod instances (work tables), staff desks, seat
// table and merged graph. Members sit at their own staff desk (shared-members.md
// §9); a thread's table seats its lead (and visiting members with
// `members.visitPods`). Layouts without `staffArea` seat members in the pods.
import { addEdge, addNode, createGraph, nearestNode, type NavGraph } from "./nav";
import { sortMembers } from "./people";
import { placeStaff } from "./staff";
import { fitTemplate } from "./template";
import type {
  Anchor, DeskDef, OfficeEmployee, OfficeLayout, OfficeThread, Point, PodTemplate, Rect, Room, Sprite, TextStyle,
} from "./types";

export interface PlacedSprite {
  id: string;
  sprite: Sprite;
  at: Point;
  z: number;
  slots: Record<string, string>;
  flip?: boolean;
  scale: number;
  threadNo?: number;
}

export interface DeskInstance extends PlacedSprite {
  key: string;
  deskId: string;
  seat: Point;
  timer?: Point;
  /** The pod's thread; undefined for staff desks. */
  threadNo?: number;
  /** Staff desks: the member who owns it, and where its `#18 · t5` work tag goes. */
  owner?: string;
  tag?: Point;
}

export interface ParticipantStrip {
  at: Point;
  size: number;
  step: Point;
  max: number;
}

export interface PodInstance {
  threadNo: number;
  thread: OfficeThread;
  color: string;
  template: string;
  rect: Rect;
  scale: number;
  frame: PodTemplate["frame"];
  plate?: { at: Point; text: string; style: TextStyle };
  desks: DeskInstance[];
  participants?: ParticipantStrip;
  /** Where visitors stand (place `pod`) and the nav node it hangs off. */
  visit: Point;
  visitNode?: string;
  /** The thread just ended (since when, animation clock ms): the table fades out while its lead leaves. */
  leaving?: number;
}

export interface SeatAssignment {
  point: Point;
  deskKey?: string;
  accessNode?: string;
  /** The pod the seat belongs to; undefined for staff desks. */
  threadNo?: number;
}

export interface Scene {
  layout: OfficeLayout;
  width: number;
  height: number;
  rooms: Room[];
  furniture: PlacedSprite[];
  pods: PodInstance[];
  /** Every desk: pod desks and staff desks. */
  desks: Map<string, DeskInstance>;
  /** Staff desks by owner (employee id). */
  staff: Map<string, DeskInstance>;
  seats: Map<string, SeatAssignment>;
  graph: NavGraph;
  anchors: Record<string, Anchor>;
  threadColors: Map<number, string>;
  /** Threads that pass `layout.show`, in display order (all pages). */
  visible: OfficeThread[];
  /** Thread numbers drawn as pods on the current page. */
  onPage: Set<number>;
  page: { index: number; count: number; perPage: number };
}

export interface CompileInput {
  threads: OfficeThread[];
  employees: OfficeEmployee[];
  focus?: number | null;
  page?: number;
  /** Threads that just ended → since when (animation clock ms): still drawn, fading, while their leads leave. */
  leaving?: Map<number, number>;
}

const STACK_STEP = 6;

export { instanceOf, sortMembers } from "./people";

export function compileScene(layout: OfficeLayout, input: CompileInput): Scene {
  const tp = layout.threadPods;
  const visible = visibleThreads(layout, input.threads, input.focus ?? null);
  const plan = planPods(layout, visible.length);
  const pageCount = Math.max(1, Math.ceil(visible.length / plan.perPage));
  const index = clamp(input.page ?? 0, 0, pageCount - 1);
  const pageThreads = visible.slice(index * plan.perPage, (index + 1) * plan.perPage);
  const threadColors = new Map(input.threads.map((t) => [t.no, threadColor(layout, t.no)]));
  const graph = baseGraph(layout);
  const pods = pageThreads.map((t, i) => instantiatePod(layout, t, plan, i, threadColors.get(t.no)!, input.leaving?.get(t.no)));
  const seats = new Map<string, SeatAssignment>();
  for (const pod of pods) {
    mergePodNav(graph, tp.templates[pod.template], pod);
    seatPod(layout, pod, podPeople(layout, input.employees, pod.threadNo), graph, seats);
  }
  const staff = placeStaff(layout, input.employees, graph);
  for (const [id, seat] of staff.seats) if (!seats.has(id)) seats.set(id, seat);
  const desks = new Map([...pods.flatMap((p) => p.desks), ...staff.desks.values()].map((d) => [d.key, d] as const));
  return {
    layout,
    width: layout.canvas.width,
    height: layout.canvas.height,
    rooms: [...layout.rooms].sort((a, b) => (a.z ?? 0) - (b.z ?? 0)),
    furniture: placeFurniture(layout),
    pods,
    desks,
    staff: staff.desks,
    seats,
    graph,
    anchors: layout.anchors,
    threadColors,
    visible,
    onPage: new Set(pageThreads.map((t) => t.no)),
    page: { index, count: pageCount, perPage: plan.perPage },
  };
}

export function threadColor(layout: OfficeLayout, no: number): string {
  const colors = layout.threadPods.threadColors ?? ["#2F6FDB"];
  return colors[((no % colors.length) + colors.length) % colors.length];
}

const STATUS_ORDER = ["waiting", "review", "running", "queued", "paused", "done", "failed", "archived"];

export function visibleThreads(layout: OfficeLayout, threads: OfficeThread[], focus: number | null): OfficeThread[] {
  const lay = layout.threadPods.layout;
  const show = new Set(lay.show ?? ["running", "waiting", "review", "paused", "queued"]);
  const list = threads.filter((t) => show.has(t.status)).sort(threadComparator(lay.order ?? "no"));
  if (!lay.pinFocused || focus === null) return list;
  const i = list.findIndex((t) => t.no === focus);
  return i <= 0 ? list : [list[i], ...list.slice(0, i), ...list.slice(i + 1)];
}

function threadComparator(order: string): (a: OfficeThread, b: OfficeThread) => number {
  if (order === "updated") return (a, b) => (b.updated_at ?? "").localeCompare(a.updated_at ?? "") || a.no - b.no;
  if (order === "status") return (a, b) => STATUS_ORDER.indexOf(a.status) - STATUS_ORDER.indexOf(b.status) || a.no - b.no;
  return (a, b) => a.no - b.no;
}

export interface PodPlan {
  template: string;
  perPage: number;
  columns: number;
  scale: number;
}

/** Picks the template, columns, page size and scale for `count` visible threads. */
export function planPods(layout: OfficeLayout, count: number): PodPlan {
  const lay = layout.threadPods.layout;
  const fit = lay.use.find((u) => u.maxThreads >= count);
  const entry = fit ?? lay.use[lay.use.length - 1];
  const size = layout.threadPods.templates[entry.template].size;
  const columns = columnsFor(layout, size, 1);
  if (fit || lay.overflow !== "scale") return { template: entry.template, perPage: fit ? Math.max(count, 1) : entry.maxThreads, columns, scale: 1 };
  return scalePlan(layout, entry.template, size, count);
}

function scalePlan(layout: OfficeLayout, template: string, size: Point, count: number): PodPlan {
  const [, , aw, ah] = layout.threadPods.area.rect;
  const [gx, gy] = layout.threadPods.layout.gap ?? [0, 0];
  for (let s = 1; s >= 0.5; s -= 0.05) {
    const columns = columnsFor(layout, size, s);
    const rows = Math.ceil(count / columns);
    const h = rows * size[1] * s + (rows - 1) * gy;
    if (columns * size[0] * s + (columns - 1) * gx <= aw && h <= ah) return { template, perPage: count, columns, scale: round2(s) };
  }
  const columns = columnsFor(layout, size, 0.5);
  const rows = Math.max(1, Math.floor((ah + gy) / (size[1] * 0.5 + gy)));
  return { template, perPage: columns * rows, columns, scale: 0.5 };
}

function columnsFor(layout: OfficeLayout, size: Point, scale: number): number {
  const cols = layout.threadPods.layout.columns ?? "auto";
  if (cols !== "auto") return cols;
  const [, , aw] = layout.threadPods.area.rect;
  const gx = layout.threadPods.layout.gap?.[0] ?? 0;
  return Math.max(1, Math.floor((aw + gx) / (size[0] * scale + gx)));
}

function instantiatePod(layout: OfficeLayout, thread: OfficeThread, plan: PodPlan, slot: number, color: string, leaving?: number): PodInstance {
  const tpl = layout.threadPods.templates[plan.template];
  const [ax, ay] = layout.threadPods.area.rect;
  const [gx, gy] = layout.threadPods.layout.gap ?? [0, 0];
  const s = plan.scale;
  const col = slot % plan.columns;
  const row = Math.floor(slot / plan.columns);
  const x = ax + col * (tpl.size[0] * s + gx);
  const y = ay + row * (tpl.size[1] * s + gy);
  const at = (p: Point): Point => [x + p[0] * s, y + p[1] * s];
  return {
    threadNo: thread.no,
    thread,
    color,
    template: plan.template,
    rect: [x, y, tpl.size[0] * s, tpl.size[1] * s],
    scale: s,
    frame: tpl.frame,
    plate: tpl.plate && plateFor(tpl.plate, thread, at(tpl.plate.at ?? [6, 6])),
    desks: tpl.desks.map((d) => deskFor(layout, d, thread.no, at, s)),
    participants: tpl.participants && stripFor(tpl.participants, at, s),
    ...visitOf(tpl, thread.no, at),
    ...(leaving === undefined ? {} : { leaving }),
  };
}

function stripFor(p: NonNullable<PodTemplate["participants"]>, at: (p: Point) => Point, s: number): ParticipantStrip {
  const size = p.size ?? 16;
  const step = p.step ?? [size + 2, 0];
  return { at: at(p.at), size: size * s, step: [step[0] * s, step[1] * s], max: p.max };
}

/** The visit spot: the template's `visit`, else its first exit node (which it hangs off). */
function visitOf(tpl: PodTemplate, no: number, at: (p: Point) => Point): { visit: Point; visitNode?: string } {
  const exit = Object.keys(tpl.nav.exits)[0];
  const node = exit ? podNodeId(no, exit) : undefined;
  if (tpl.visit) return { visit: at(tpl.visit), visitNode: node };
  const local = exit ? tpl.nav.nodes[exit] : undefined;
  return { visit: at(local ?? [tpl.size[0] / 2, tpl.size[1] / 2]), visitNode: node };
}

function plateFor(style: TextStyle, thread: OfficeThread, at: Point): PodInstance["plate"] {
  const vars = {
    no: thread.no,
    title: thread.title,
    mainDone: thread.counts?.main_done ?? 0,
    mainTotal: thread.counts?.main_total ?? 0,
    status: thread.status,
  };
  return { at, style, text: fitTemplate(style.value ?? "#{no} {title}", vars, "title", style.maxChars) };
}

function deskFor(layout: OfficeLayout, d: DeskDef, no: number, at: (p: Point) => Point, s: number): DeskInstance {
  const sprite = layout.sprites[d.sprite];
  const origin = at(d.at);
  return {
    id: `pod${no}.${d.id}`,
    key: `pod${no}.${d.id}`,
    deskId: d.id,
    sprite,
    at: origin,
    z: 0,
    slots: { ...(sprite?.slots ?? {}) },
    scale: s,
    seat: at(d.seat),
    timer: d.timer && [origin[0] + d.timer[0] * s, origin[1] + d.timer[1] * s],
    threadNo: no,
  };
}

function baseGraph(layout: OfficeLayout): NavGraph {
  const g = createGraph();
  for (const [id, p] of Object.entries(layout.nav.nodes)) addNode(g, id, p);
  for (const [a, b] of layout.nav.edges) addEdge(g, a, b);
  return g;
}

export function podNodeId(no: number, local: string): string {
  return `pod${no}.${local}`;
}

function mergePodNav(g: NavGraph, tpl: PodTemplate, pod: PodInstance): void {
  const [x, y] = pod.rect;
  for (const [id, p] of Object.entries(tpl.nav.nodes)) addNode(g, podNodeId(pod.threadNo, id), [x + p[0] * pod.scale, y + p[1] * pod.scale]);
  for (const [a, b] of tpl.nav.edges) addEdge(g, podNodeId(pod.threadNo, a), podNodeId(pod.threadNo, b));
  for (const [local, global] of Object.entries(tpl.nav.exits)) addEdge(g, podNodeId(pod.threadNo, local), global);
}

/**
 * Who sits at a thread's table: its lead; the members working for the thread too when the layout
 * has no staff area (older layouts) or lets members visit the tables (`members.visitPods`).
 */
export function podPeople(layout: OfficeLayout, employees: OfficeEmployee[], no: number): OfficeEmployee[] {
  const visit = !layout.staffArea || Boolean(layout.members?.visitPods);
  return employees.filter((e) => e.thread_no === no && (e.tier === "lead" || (visit && e.tier === "member")));
}

function seatPod(layout: OfficeLayout, pod: PodInstance, members: OfficeEmployee[], g: NavGraph, seats: Map<string, SeatAssignment>): void {
  const tp = layout.threadPods;
  const tpl = tp.templates[pod.template];
  const leadDeskId = tp.seating.leadDesk ?? tpl.desks.find((d) => d.for === "lead")?.id;
  const lead = members.find((e) => e.tier === "lead");
  const others = sortMembers(members.filter((e) => e !== lead), tp.seating.order ?? ["roleSort", "instance"]);
  const free = pod.desks.filter((d) => d.deskId !== leadDeskId && tpl.desks.find((t) => t.id === d.deskId)?.for !== "lead");
  const leadDesk = pod.desks.find((d) => d.deskId === leadDeskId);
  const assignDesk = (e: OfficeEmployee, d: DeskInstance) =>
    seats.set(e.id, { point: d.seat, deskKey: d.key, threadNo: pod.threadNo, accessNode: seatAccess(layout, tpl, pod, d.deskId, d.seat, g) });
  if (lead) {
    const d = leadDesk ?? free.shift();
    if (d) assignDesk(lead, d);
    else others.unshift(lead);
  }
  const overflow = (tpl.overflowSeats ?? []).map((p): Point => [pod.rect[0] + p[0] * pod.scale, pod.rect[1] + p[1] * pod.scale]);
  others.forEach((e, i) => {
    if (i < free.length) return assignDesk(e, free[i]);
    const point = overflowPoint(overflow, i - free.length, pod);
    seats.set(e.id, { point, threadNo: pod.threadNo, accessNode: nearestPodNode(g, pod.threadNo, point) });
  });
}

function overflowPoint(points: Point[], i: number, pod: PodInstance): Point {
  if (i < points.length) return points[i];
  const last = points[points.length - 1] ?? [pod.rect[0] + pod.rect[2] - 40, pod.rect[1] + pod.rect[3] - 70];
  const k = i - points.length + 1;
  return [last[0] + k * STACK_STEP, last[1] + k * STACK_STEP];
}

function seatAccess(layout: OfficeLayout, tpl: PodTemplate, pod: PodInstance, deskId: string, seat: Point, g: NavGraph): string | undefined {
  const row = layout.threadPods.seating.rowOf?.[deskId];
  const local = row ? tpl.nav.seatLinks[row] : undefined;
  return local ? podNodeId(pod.threadNo, local) : nearestPodNode(g, pod.threadNo, seat);
}

function nearestPodNode(g: NavGraph, no: number, p: Point): string | undefined {
  const prefix = `pod${no}.`;
  const sub = createGraph();
  for (const [id, q] of g.nodes) if (id.startsWith(prefix)) addNode(sub, id, q);
  return nearestNode(sub, p) ?? nearestNode(g, p);
}

function placeFurniture(layout: OfficeLayout): PlacedSprite[] {
  const items: PlacedSprite[] = (layout.furniture ?? []).map((f) => ({
    id: f.id,
    sprite: layout.sprites[f.sprite],
    at: f.at,
    z: f.z ?? 0,
    slots: { ...(layout.sprites[f.sprite]?.slots ?? {}), ...(f.slots ?? {}) },
    flip: f.flip,
    scale: 1,
  }));
  return items.filter((f) => f.sprite).sort((a, b) => a.z - b.z || a.at[1] - b.at[1]);
}

function clamp(v: number, lo: number, hi: number): number {
  return Math.max(lo, Math.min(hi, v));
}

function round2(v: number): number {
  return Math.round(v * 100) / 100;
}
