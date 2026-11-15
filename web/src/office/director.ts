// Director (office-layout.md §4.5, §4.7, §4.8): status → rule → place,
// animation, desk screen and bubble; anchor slot allocation; walking along
// the graph; scripted sequences such as "claim". Pure logic, no DOM.
import type { Scene } from "./compile";
import { hashString } from "./looks";
import { advance, dist, route } from "./nav";
import { bubbleFor, matchRule, sequenceFor, subjectOf, toMs, type Bubble } from "./rules";
import { fillTemplate } from "./template";
import type { EmployeeStatus, OfficeEmployee, Point, SequenceStep, StateRule } from "./types";

export interface Place {
  point: Point;
  accessNode?: string;
  deskKey?: string;
  hidden?: boolean;
  anchor?: string;
  pose?: "stand" | "sit";
}

interface SeqRun {
  steps: SequenceStep[];
  index: number;
  waitLeft: number;
  pending?: EmployeeStatus;
}

interface Actor {
  employee: OfficeEmployee;
  status: EmployeeStatus;
  rule: StateRule;
  pos: Point;
  path: Point[];
  accessNode?: string;
  dest?: Place;
  destPlace?: string;
  claim?: { anchor: string; index: number };
  anim: string;
  restAnim: string;
  animStart: number;
  flip: boolean;
  bubble?: Bubble;
  seq?: SeqRun;
}

export interface ActorView {
  id: string;
  employee: OfficeEmployee;
  status: EmployeeStatus;
  pos: Point;
  anim: string;
  animStart: number;
  flip: boolean;
  walking: boolean;
  overlay?: string;
  opacity: number;
  grayscale: boolean;
  bubble?: Bubble;
  hidden: boolean;
  /** Thread number badge for people of off-page threads shown at shared anchors. */
  badge?: number;
}

export interface DeskState {
  screen?: string;
  timerSince?: number;
}

export interface DirectorOptions {
  now?: () => number;
  taskTitle?: (taskId: string) => string | undefined;
  /** Animation clock in ms (frame selection); defaults to `now`. */
  clock?: () => number;
}

const IDLE: EmployeeStatus = { state: "idle" };
const BEHIND = 24;
const STACK = 6;

export class Director {
  private scene: Scene;
  private actorsById = new Map<string, Actor>();
  private occupancy = new Map<string, Map<number, string>>();
  /** Slot to keep while rehoming after a scene change. */
  private preferred?: { anchor: string; index: number };
  private opts: Required<Pick<DirectorOptions, "now">> & DirectorOptions;
  /** Teleport instead of walking (hidden tab, reduced motion). */
  noWalk = false;
  /** Increments whenever something other than a position changes. */
  version = 0;

  constructor(scene: Scene, opts: DirectorOptions = {}) {
    this.scene = scene;
    this.opts = { now: () => Date.now(), ...opts };
  }

  get layout() {
    return this.scene.layout;
  }

  setOptions(opts: DirectorOptions): void {
    this.opts = { ...this.opts, ...opts };
  }

  /**
   * Hot reload / page / focus / roster change. Everyone keeps their status;
   * people whose place is unchanged keep walking (or sitting) as they were,
   * the others teleport to the place recomputed from the new scene.
   */
  setScene(scene: Scene): void {
    this.scene = scene;
    this.occupancy.clear();
    for (const a of this.claimOrder()) this.rehome(a);
    this.preferred = undefined;
    this.version++;
  }

  private rehome(a: Actor): void {
    const prev = a.dest;
    this.preferred = a.claim;
    a.claim = undefined;
    if (a.seq) {
      const place = this.resolvePlace(a, a.destPlace ?? "seat");
      if (samePlace(prev, place)) return void (a.dest = place);
      a.status = a.seq.pending ?? a.status;
      a.seq = undefined;
      return this.applyRule(a, true);
    }
    const rule = matchRule(this.layout.states.rules, subjectOf(a.status, a.employee));
    const place = this.resolvePlace(a, rule.place);
    if (!samePlace(prev, place)) return this.applyRule(a, true);
    a.rule = rule;
    a.dest = place;
    a.bubble = bubbleFor(a.status, rule, this.opts.now(), this.layout.bubbles?.ttlMs ?? 600_000);
  }

  /** Syncs the roster: adds new people at their place, drops missing ones. */
  setEmployees(list: OfficeEmployee[]): void {
    const ids = new Set(list.map((e) => e.id));
    for (const id of [...this.actorsById.keys()]) if (!ids.has(id)) this.remove(id);
    for (const e of list) this.upsert(e);
    this.version++;
  }

  private upsert(e: OfficeEmployee): void {
    const existing = this.actorsById.get(e.id);
    if (existing) {
      existing.employee = e;
      return;
    }
    const a: Actor = {
      employee: e,
      status: e.status ?? IDLE,
      rule: { match: {}, place: "seat" },
      pos: this.scene.seats.get(e.id)?.point ?? [0, 0],
      path: [],
      anim: "stand",
      restAnim: "stand",
      animStart: 0,
      flip: false,
    };
    this.actorsById.set(e.id, a);
    this.applyRule(a, true);
  }

  private remove(id: string): void {
    const a = this.actorsById.get(id);
    if (a) this.release(a);
    this.actorsById.delete(id);
  }

  /** Applies a status change; walks unless `instant`. */
  apply(id: string, status: EmployeeStatus, instant = false): void {
    const a = this.actorsById.get(id);
    if (!a) return;
    this.version++;
    if (a.seq && !instant) {
      a.seq.pending = status;
      return;
    }
    a.seq = undefined;
    a.status = status;
    const seq = instant ? undefined : sequenceFor(this.layout.states.sequences, status);
    if (seq) return this.startSequence(a, seq.steps);
    this.applyRule(a, instant);
  }

  /** Sets every status at once and teleports (replay seek, snapshot). */
  reset(statuses: Map<string, EmployeeStatus>): void {
    this.occupancy.clear();
    for (const a of this.claimOrder()) {
      a.claim = undefined;
      a.seq = undefined;
      a.status = statuses.get(a.employee.id) ?? a.employee.status ?? IDLE;
      this.applyRule(a, true);
    }
    this.version++;
  }

  private claimOrder(): Actor[] {
    const key = (a: Actor) => (a.claim ? a.claim.index : 1e9);
    return [...this.actorsById.values()].sort((x, y) => key(x) - key(y) || x.employee.id.localeCompare(y.employee.id));
  }

  private applyRule(a: Actor, instant: boolean): void {
    const rule = matchRule(this.layout.states.rules, subjectOf(a.status, a.employee));
    a.rule = rule;
    a.bubble = bubbleFor(a.status, rule, this.opts.now(), this.layout.bubbles?.ttlMs ?? 600_000);
    const place = this.resolvePlace(a, rule.place);
    const anim = rule.anim === "keep" ? a.restAnim : rule.anim ?? place.pose ?? "stand";
    a.restAnim = anim;
    this.moveTo(a, place, instant);
  }

  /** Resolves `seat`, `current` or `anchor:<name>` for this actor. */
  private resolvePlace(a: Actor, place: string): Place {
    a.destPlace = place === "current" ? a.destPlace : place;
    if (place === "current") return a.dest ? { ...a.dest, point: a.path.length ? a.dest.point : a.pos } : { point: a.pos, accessNode: a.accessNode };
    if (place.startsWith("anchor:")) return this.claimAnchor(a, place.slice(7));
    this.release(a);
    const seat = this.scene.seats.get(a.employee.id);
    if (seat) return { point: seat.point, accessNode: seat.accessNode, deskKey: seat.deskKey };
    return { point: a.pos, accessNode: a.accessNode, hidden: true };
  }

  private claimAnchor(a: Actor, name: string): Place {
    const anchor = this.scene.anchors[name];
    if (!anchor) return { point: a.pos, accessNode: a.accessNode };
    if (a.claim?.anchor !== name) {
      this.release(a);
      a.claim = { anchor: name, index: this.freeIndex(name, this.preferred) };
      this.occupancy.get(name)!.set(a.claim.index, a.employee.id);
    }
    return { point: anchorPoint(anchor, a.claim.index, a.employee.id), accessNode: anchor.nav, anchor: name, pose: anchor.pose };
  }

  private freeIndex(name: string, preferred?: { anchor: string; index: number }): number {
    if (!this.occupancy.has(name)) this.occupancy.set(name, new Map());
    const used = this.occupancy.get(name)!;
    if (preferred?.anchor === name && !used.has(preferred.index)) return preferred.index;
    let i = 0;
    while (used.has(i)) i++;
    return i;
  }

  private release(a: Actor): void {
    if (!a.claim) return;
    this.occupancy.get(a.claim.anchor)?.delete(a.claim.index);
    a.claim = undefined;
  }

  private walkingCount(): number {
    let n = 0;
    for (const a of this.actorsById.values()) if (a.path.length) n++;
    return n;
  }

  private moveTo(a: Actor, place: Place, instant: boolean): void {
    const wasHidden = Boolean(a.dest?.hidden);
    a.dest = place;
    if (place.hidden) {
      a.path = [];
      this.setAnim(a, a.restAnim);
      return;
    }
    const cap = this.layout.states.maxConcurrentWalks ?? 12;
    const far = dist(a.pos, place.point) > 0.5;
    const teleport = instant || this.noWalk || (!a.path.length && this.walkingCount() >= cap) || wasHidden;
    if (!far || teleport) return this.arrive(a, place);
    a.path = route(this.scene.graph, a.pos, a.accessNode, place.point, place.accessNode).slice(1);
    this.setAnim(a, this.layout.states.walkAnim ?? "walk");
  }

  private arrive(a: Actor, place: Place): void {
    a.pos = [place.point[0], place.point[1]];
    a.path = [];
    a.accessNode = place.accessNode;
    this.setAnim(a, a.seq ? a.anim : a.restAnim);
  }

  private setAnim(a: Actor, anim: string): void {
    if (a.anim === anim) return;
    a.anim = anim;
    a.animStart = (this.opts.clock ?? this.opts.now)();
  }

  private startSequence(a: Actor, steps: SequenceStep[]): void {
    a.seq = { steps, index: -1, waitLeft: 0 };
    this.nextStep(a);
  }

  private nextStep(a: Actor): void {
    const run = a.seq!;
    run.index++;
    const step = run.steps[run.index];
    if (!step) return this.endSequence(a);
    this.version++;
    a.bubble = step.bubble ? { text: this.stepBubble(a, step.bubble), kind: "plain" } : a.bubble;
    if (step.anim) this.setAnim(a, step.anim);
    if (step.goto) return this.stepGoto(a, step.goto);
    run.waitLeft = step.wait ?? 0;
  }

  private stepGoto(a: Actor, goto: string): void {
    const place = this.resolvePlace(a, goto);
    this.moveTo(a, place, false);
    if (!a.path.length) this.nextStep(a);
  }

  private stepBubble(a: Actor, template: string): string {
    const id = a.status.task_id ?? "";
    return fillTemplate(template, { taskId: id, taskTitle: (id && this.opts.taskTitle?.(id)) || "" });
  }

  private endSequence(a: Actor): void {
    const latest = a.seq?.pending ?? a.status;
    a.seq = undefined;
    a.status = latest;
    this.applyRule(a, false);
  }

  /** Advances walks and sequence waits by `dtMs` (already scaled by replay speed). Returns true while anything moves. */
  tick(dtMs: number): boolean {
    let busy = false;
    const speed = this.layout.nav.walkSpeed ?? 90;
    for (const a of this.actorsById.values()) {
      if (a.path.length) {
        busy = true;
        this.walk(a, (speed * dtMs) / 1000);
      } else if (a.seq) {
        busy = true;
        this.waitStep(a, dtMs);
      }
    }
    return busy;
  }

  private walk(a: Actor, step: number): void {
    const r = advance(a.pos, a.path, step);
    a.pos = r.pos;
    a.path = r.rest;
    if (r.dx) a.flip = r.dx < 0;
    if (r.rest.length) return;
    this.arrive(a, a.dest!);
    if (a.seq) this.nextStep(a);
  }

  private waitStep(a: Actor, dtMs: number): void {
    a.seq!.waitLeft -= dtMs;
    if (a.seq!.waitLeft <= 0) this.nextStep(a);
  }

  /** True while someone walks or a sequence runs. */
  get busy(): boolean {
    for (const a of this.actorsById.values()) if (a.path.length || a.seq) return true;
    return false;
  }

  actor(id: string): ActorView | undefined {
    const a = this.actorsById.get(id);
    return a && this.view(a);
  }

  actors(): ActorView[] {
    return [...this.actorsById.values()].map((a) => this.view(a));
  }

  private view(a: Actor): ActorView {
    const no = a.employee.thread_no ?? undefined;
    const offPage = no !== undefined && !this.scene.onPage.has(no);
    const hideOffPage = offPage && (this.layout.threadPods.layout.offPage ?? "anchorsWithBadge") === "hide";
    const inSeq = Boolean(a.seq);
    return {
      id: a.employee.id,
      employee: a.employee,
      status: a.status,
      pos: a.pos,
      anim: a.anim,
      animStart: a.animStart,
      flip: a.flip,
      walking: a.path.length > 0,
      overlay: inSeq ? undefined : a.rule.overlay,
      opacity: inSeq ? 1 : a.rule.opacity ?? 1,
      grayscale: !inSeq && Boolean(a.rule.grayscale),
      bubble: a.bubble,
      hidden: Boolean(a.dest?.hidden) || hideOffPage,
      badge: offPage ? no : undefined,
    };
  }

  /** Screen colour and timer per desk, from the people seated there. */
  desks(): Map<string, DeskState> {
    const out = new Map<string, DeskState>();
    for (const a of this.actorsById.values()) {
      const key = a.dest?.deskKey;
      if (!key || a.path.length || a.seq) continue;
      out.set(key, { screen: a.rule.screen, timerSince: a.rule.timer ? toMs(a.status.since) : undefined });
    }
    return out;
  }
}

function samePlace(a: Place | undefined, b: Place): boolean {
  return Boolean(a) && Boolean(a!.hidden) === Boolean(b.hidden) && dist(a!.point, b.point) < 0.5;
}

/** Slot `index` of an anchor group, applying its overflow rule when full. */
export function anchorPoint(anchor: Scene["anchors"][string], index: number, id: string): Point {
  const pts = anchor.points;
  if (index < pts.length) return pts[index];
  const extra = index - pts.length;
  const mode = anchor.overflow ?? "stack";
  if (mode === "standBehind") {
    const p = pts[extra % pts.length];
    return [p[0], p[1] + BEHIND * (1 + Math.floor(extra / pts.length))];
  }
  if (mode === "jitter") {
    const p = pts[pts.length - 1 - (extra % Math.min(3, pts.length))];
    const h = hashString(`${id}:${index}`);
    return [p[0] + ((h % 25) - 12), p[1] + (((h >> 8) % 17) - 8)];
  }
  const last = pts[pts.length - 1];
  return [last[0] + STACK * (extra + 1), last[1] + STACK * (extra + 1)];
}
