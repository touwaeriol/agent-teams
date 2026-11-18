// Semantic checks beyond the JSON Schema (office-layout.md §6.3). The Go
// control plane runs the same checks; the admin editor runs this copy.
import { compileScene } from "./compile";
import { component, type NavGraph } from "./nav";
import { capacity } from "./staff";
import type { OfficeEmployee, OfficeLayout, OfficeThread, Point, Room, StateName } from "./types";

export interface SemanticReport {
  errors: string[];
  warnings: string[];
}

const STATES: StateName[] = [
  "typing", "reading", "running", "thinking", "walking_to_board", "claiming", "returning",
  "raising_hand", "idle", "waiting", "queued", "offline", "broken",
];
const MAX_BYTES = 512 * 1024;

export function validateSemantics(layout: OfficeLayout): SemanticReport {
  const r: SemanticReport = { errors: [], warnings: [] };
  checkSize(layout, r);
  checkReferences(layout, r);
  checkPixels(layout, r);
  checkRules(layout, r);
  checkBounds(layout, r);
  checkDoors(layout, r);
  if (r.errors.length) return r;
  const scene = compileScene(layout, maxThreadsInput(layout));
  checkConnectivity(layout, scene.graph, r);
  checkWalls(layout, scene, r);
  return r;
}

function checkSize(layout: OfficeLayout, r: SemanticReport): void {
  const bytes = new TextEncoder().encode(JSON.stringify(layout)).length;
  if (bytes > MAX_BYTES) r.errors.push(`layout is ${bytes} bytes, limit ${MAX_BYTES}`);
}

function refCheck(r: SemanticReport, table: Record<string, unknown> | undefined, key: string | undefined, where: string): void {
  if (key !== undefined && !(table && key in table)) r.errors.push(`${where}: unknown "${key}"`);
}

function checkReferences(l: OfficeLayout, r: SemanticReport): void {
  const rooms = Object.fromEntries(l.rooms.map((x) => [x.id, x]));
  const ch = l.characters.person;
  for (const room of l.rooms) refCheck(r, l.floors, room.floor, `rooms[${room.id}].floor`);
  for (const f of l.furniture ?? []) {
    refCheck(r, l.sprites, f.sprite, `furniture[${f.id}].sprite`);
    refCheck(r, rooms, f.room, `furniture[${f.id}].room`);
  }
  for (const w of l.widgets ?? []) {
    refCheck(r, l.sprites, w.sprite, `widgets[${w.id}].sprite`);
    refCheck(r, l.sprites, w.slot?.sprite, `widgets[${w.id}].slot.sprite`);
    refCheck(r, rooms, w.room, `widgets[${w.id}].room`);
  }
  refCheck(r, rooms, l.threadPods.area.room, "threadPods.area.room");
  checkStaffRefs(l, r, rooms);
  for (const u of l.threadPods.layout.use) refCheck(r, l.threadPods.templates, u.template, "threadPods.layout.use");
  for (const [id, t] of Object.entries(l.threadPods.templates)) {
    for (const d of t.desks) refCheck(r, l.sprites, d.sprite, `templates.${id}.desks[${d.id}].sprite`);
    for (const g of Object.values(t.nav.exits)) refCheck(r, l.nav.nodes, g, `templates.${id}.nav.exits`);
    for (const n of [...Object.keys(t.nav.exits), ...Object.values(t.nav.seatLinks), ...t.nav.edges.flat()]) refCheck(r, t.nav.nodes, n, `templates.${id}.nav`);
  }
  for (const [id, a] of Object.entries(l.anchors)) refCheck(r, l.nav.nodes, a.nav, `anchors.${id}.nav`);
  for (const [a, b] of l.nav.edges) [a, b].forEach((n) => refCheck(r, l.nav.nodes, n, "nav.edges"));
  for (const [i, rule] of l.states.rules.entries()) {
    checkPlace(l, r, rule.place, `states.rules[${i}].place`);
    if (rule.anim && rule.anim !== "keep") refCheck(r, ch.animations, rule.anim, `states.rules[${i}].anim`);
    refCheck(r, ch.overlays, rule.overlay, `states.rules[${i}].overlay`);
  }
  for (const [id, seq] of Object.entries(l.states.sequences ?? {}))
    for (const s of seq.steps) {
      if (s.goto) checkPlace(l, r, s.goto, `states.sequences.${id}.goto`);
      if (s.anim) refCheck(r, ch.animations, s.anim, `states.sequences.${id}.anim`);
    }
  refCheck(r, ch.animations, l.states.walkAnim, "states.walkAnim");
  for (const [id, anim] of Object.entries(ch.animations)) for (const f of anim.frames) refCheck(r, ch.frames, f, `animations.${id}`);
  for (const [id, parts] of Object.entries(ch.frames)) for (const p of parts) refCheck(r, ch.parts, p, `frames.${id}`);
}

function checkStaffRefs(l: OfficeLayout, r: SemanticReport, rooms: Record<string, Room>): void {
  const s = l.staffArea;
  if (!s) return;
  refCheck(r, rooms, s.room, "staffArea.room");
  refCheck(r, l.sprites, s.desk.sprite, "staffArea.desk.sprite");
  refCheck(r, l.nav.nodes, s.nav.entry, "staffArea.nav.entry");
}

function checkPlace(l: OfficeLayout, r: SemanticReport, place: string, where: string): void {
  if (place.startsWith("anchor:")) refCheck(r, l.anchors, place.slice(7), where);
}

function checkPixels(l: OfficeLayout, r: SemanticReport): void {
  const ch = l.characters.person;
  const width = (rows: string[], where: string) =>
    rows.forEach((row, i) => row.length !== ch.width && r.errors.push(`${where}[${i}] is ${row.length} wide, expected ${ch.width}`));
  for (const [id, rows] of Object.entries(ch.parts)) width(rows, `characters.person.parts.${id}`);
  const pieces = { ...prefix("outfits", l.looks.outfits), ...prefix("accessories", l.looks.accessories) };
  for (const [id, piece] of Object.entries(pieces))
    for (const [part, rows] of Object.entries(piece.patch ?? {})) width(Object.values(rows), `looks.${id}.patch.${part}`);
}

function prefix<T>(p: string, o: Record<string, T>): Record<string, T> {
  return Object.fromEntries(Object.entries(o).map(([k, v]) => [`${p}.${k}`, v]));
}

function checkRules(l: OfficeLayout, r: SemanticReport): void {
  const rules = l.states.rules;
  const last = rules[rules.length - 1];
  if (!last || Object.keys(last.match).length) r.errors.push("states.rules: the last rule must be the catch-all {\"match\": {}}");
  const covered = new Set(rules.flatMap((x) => x.match.state ?? STATES));
  for (const s of STATES) if (!covered.has(s)) r.errors.push(`states.rules: no rule covers state "${s}"`);
}

function inside(rect: number[], w: number, h: number): boolean {
  return rect[0] >= 0 && rect[1] >= 0 && rect[0] + rect[2] <= w && rect[1] + rect[3] <= h;
}

function checkBounds(l: OfficeLayout, r: SemanticReport): void {
  const { width: w, height: h } = l.canvas;
  for (const room of l.rooms) if (!inside(room.rect, w, h)) r.errors.push(`rooms[${room.id}] is outside the canvas`);
  if (!inside(l.threadPods.area.rect, w, h)) r.errors.push("threadPods.area is outside the canvas");
  if (l.staffArea && !inside(l.staffArea.rect, w, h)) r.errors.push("staffArea is outside the canvas");
  for (const f of l.furniture ?? []) {
    const s = l.sprites[f.sprite];
    if (s && !inside([f.at[0], f.at[1], s.w, s.h], w, h)) r.errors.push(`furniture[${f.id}] is outside the canvas`);
  }
  const [, , aw, ah] = l.threadPods.area.rect;
  const fits = Object.values(l.threadPods.templates).some((t) => t.size[0] * 0.5 <= aw && t.size[1] * 0.5 <= ah);
  if (!fits) r.errors.push("threadPods.area cannot hold the smallest template");
}

function wallLength(room: Room, side: string): number {
  return side === "n" || side === "s" ? room.rect[2] : room.rect[3];
}

function checkDoors(l: OfficeLayout, r: SemanticReport): void {
  for (const room of l.rooms) {
    const doors = room.doors ?? [];
    for (const d of doors) {
      const half = (d.width ?? 24) / 2;
      if (d.offset - half < 0 || d.offset + half > wallLength(room, d.side)) r.errors.push(`rooms[${room.id}]: door on ${d.side} at ${d.offset} is outside the wall`);
    }
    for (let i = 0; i < doors.length; i++)
      for (let j = i + 1; j < doors.length; j++)
        if (doors[i].side === doors[j].side && Math.abs(doors[i].offset - doors[j].offset) < ((doors[i].width ?? 24) + (doors[j].width ?? 24)) / 2)
          r.errors.push(`rooms[${room.id}]: doors overlap on ${doors[i].side}`);
  }
}

/** Compiles with the largest thread count the layout shows without paging, and a full staff area (its walkways get checked too). */
function maxThreadsInput(l: OfficeLayout): { threads: OfficeThread[]; employees: OfficeEmployee[] } {
  const max = Math.max(...l.threadPods.layout.use.map((u) => u.maxThreads));
  const threads = Array.from({ length: max }, (_, i): OfficeThread => ({ no: i + 1, title: `t${i + 1}`, status: "running" }));
  const { perRow, maxRows } = l.staffArea ? capacity(l.staffArea, 1) : { perRow: 0, maxRows: 0 };
  const employees = Array.from({ length: perRow * maxRows }, (_, i): OfficeEmployee => ({ id: `m${i}`, name: `m-${i + 1}`, tier: "member", role: "m" }));
  return { threads, employees };
}

function checkConnectivity(l: OfficeLayout, g: NavGraph, r: SemanticReport): void {
  const targets = [
    ...Object.entries(l.anchors).flatMap(([id, a]) => (a.nav ? [[`anchors.${id}`, a.nav]] : [])),
    ...Object.values(l.threadPods.templates).flatMap((t) => Object.values(t.nav.exits).map((n) => ["pod exit", n])),
  ];
  if (!targets.length) return;
  const reach = component(g, targets[0][1]);
  for (const [what, node] of targets) if (!reach.has(node)) r.errors.push(`nav: ${what} (node ${node}) is not connected to ${targets[0][1]}`);
}

interface Segment {
  a: Point;
  b: Point;
  what: string;
}

function checkWalls(l: OfficeLayout, scene: ReturnType<typeof compileScene>, r: SemanticReport): void {
  const segs: Segment[] = [];
  for (const [a, b] of scene.graph.adj.entries())
    for (const n of b) if (a < n) segs.push({ a: scene.graph.nodes.get(a)!, b: scene.graph.nodes.get(n)!, what: `${a} – ${n}` });
  for (const [id, anchor] of Object.entries(l.anchors)) {
    const nav = anchor.nav && l.nav.nodes[anchor.nav];
    if (nav) anchor.points.forEach((p, i) => segs.push({ a: p, b: nav, what: `anchors.${id}[${i}] – ${anchor.nav}` }));
  }
  for (const room of l.rooms) {
    if (!room.doors?.length) {
      r.warnings.push(`rooms[${room.id}] has no doors; wall crossing not checked`);
      continue;
    }
    for (const s of segs) for (const hit of wallHits(room, s)) r.errors.push(`nav: ${s.what} crosses the ${hit} wall of ${room.id} outside a door`);
  }
}

/** Walls of `room` the segment crosses outside every door. */
export function wallHits(room: Room, s: Segment): string[] {
  const [x, y, w, h] = room.rect;
  const sides: [string, "x" | "y", number, number][] = [["w", "x", x, y], ["e", "x", x + w, y], ["n", "y", y, x], ["s", "y", y + h, x]];
  const out: string[] = [];
  for (const [side, axis, line, origin] of sides) {
    const at = crossing(s, axis, line);
    if (at === undefined) continue;
    const along = at - origin;
    if (along < 0 || along > wallLength(room, side)) continue;
    const ok = (room.doors ?? []).some((d) => d.side === side && Math.abs(along - d.offset) <= (d.width ?? 24) / 2);
    if (!ok) out.push(side);
  }
  return out;
}

/** Where the segment strictly crosses the line axis=value (the other coordinate), if it does. */
function crossing(s: Segment, axis: "x" | "y", value: number): number | undefined {
  const i = axis === "x" ? 0 : 1;
  const j = 1 - i;
  const da = s.a[i] - value;
  const db = s.b[i] - value;
  if (da * db >= 0) return undefined;
  const t = da / (da - db);
  return s.a[j] + (s.b[j] - s.a[j]) * t;
}
