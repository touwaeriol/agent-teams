// Walking graph: A* over undirected straight edges (office-layout.md §4.8).
import type { Point } from "./types";

export interface NavGraph {
  nodes: Map<string, Point>;
  adj: Map<string, Set<string>>;
}

export function createGraph(): NavGraph {
  return { nodes: new Map(), adj: new Map() };
}

export function addNode(g: NavGraph, id: string, p: Point): void {
  g.nodes.set(id, p);
  if (!g.adj.has(id)) g.adj.set(id, new Set());
}

export function addEdge(g: NavGraph, a: string, b: string): void {
  if (!g.nodes.has(a) || !g.nodes.has(b)) return;
  g.adj.get(a)!.add(b);
  g.adj.get(b)!.add(a);
}

export function dist(a: Point, b: Point): number {
  return Math.hypot(a[0] - b[0], a[1] - b[1]);
}

export function nearestNode(g: NavGraph, p: Point): string | undefined {
  let best: string | undefined;
  let bestD = Infinity;
  for (const [id, q] of g.nodes) {
    const d = dist(p, q);
    if (d < bestD) [best, bestD] = [id, d];
  }
  return best;
}

/** A* with Euclidean edge weights; returns node ids from start to goal, or undefined. */
export function astar(g: NavGraph, start: string, goal: string): string[] | undefined {
  const goalP = g.nodes.get(goal);
  if (!g.nodes.has(start) || !goalP) return undefined;
  const gScore = new Map<string, number>([[start, 0]]);
  const fScore = new Map<string, number>([[start, dist(g.nodes.get(start)!, goalP)]]);
  const came = new Map<string, string>();
  const open = new Set([start]);
  while (open.size) {
    const cur = lowest(open, fScore);
    if (cur === goal) return rebuild(came, cur);
    open.delete(cur);
    relax(g, cur, goalP, { gScore, fScore, came, open });
  }
  return undefined;
}

interface AStarState {
  gScore: Map<string, number>;
  fScore: Map<string, number>;
  came: Map<string, string>;
  open: Set<string>;
}

function relax(g: NavGraph, cur: string, goalP: Point, s: AStarState): void {
  const curP = g.nodes.get(cur)!;
  for (const next of g.adj.get(cur) ?? []) {
    const nextP = g.nodes.get(next)!;
    const tentative = s.gScore.get(cur)! + dist(curP, nextP);
    if (tentative >= (s.gScore.get(next) ?? Infinity)) continue;
    s.came.set(next, cur);
    s.gScore.set(next, tentative);
    s.fScore.set(next, tentative + dist(nextP, goalP));
    s.open.add(next);
  }
}

function lowest(open: Set<string>, f: Map<string, number>): string {
  let best = "";
  let bestF = Infinity;
  for (const id of open) {
    const v = f.get(id) ?? Infinity;
    if (v < bestF || best === "") [best, bestF] = [id, v];
  }
  return best;
}

function rebuild(came: Map<string, string>, end: string): string[] {
  const path = [end];
  let cur = end;
  while (came.has(cur)) {
    cur = came.get(cur)!;
    path.unshift(cur);
  }
  return path;
}

/**
 * Polyline from `from` to `to`: from → its access node → A* → target access
 * node → to. Falls back to the nearest node when an access node is unknown and
 * to a straight line when the graph has no path.
 */
export function route(g: NavGraph, from: Point, fromNode: string | undefined, to: Point, toNode: string | undefined): Point[] {
  const a = fromNode && g.nodes.has(fromNode) ? fromNode : nearestNode(g, from);
  const b = toNode && g.nodes.has(toNode) ? toNode : nearestNode(g, to);
  const ids = a && b ? astar(g, a, b) : undefined;
  const middle = ids ? ids.map((id) => g.nodes.get(id)!) : [];
  return dedupe([from, ...middle, to]);
}

function dedupe(points: Point[]): Point[] {
  return points.filter((p, i) => i === 0 || dist(p, points[i - 1]) > 0.01);
}

export function pathLength(points: Point[]): number {
  let total = 0;
  for (let i = 1; i < points.length; i++) total += dist(points[i - 1], points[i]);
  return total;
}

export interface Advance {
  pos: Point;
  /** Remaining waypoints, first one is the next target. Empty = arrived. */
  rest: Point[];
  /** Horizontal direction of the last movement: -1 left, 1 right, 0 none. */
  dx: number;
}

/** Moves `step` px along `waypoints` (not including `pos`). */
export function advance(pos: Point, waypoints: Point[], step: number): Advance {
  let cur: Point = [pos[0], pos[1]];
  let left = step;
  let dx = 0;
  const rest = [...waypoints];
  while (rest.length && left > 0) {
    const next = rest[0];
    const d = dist(cur, next);
    if (next[0] !== cur[0]) dx = Math.sign(next[0] - cur[0]);
    if (d <= left) {
      cur = [next[0], next[1]];
      left -= d;
      rest.shift();
      continue;
    }
    const t = left / d;
    cur = [cur[0] + (next[0] - cur[0]) * t, cur[1] + (next[1] - cur[1]) * t];
    left = 0;
  }
  return { pos: cur, rest, dx };
}

/** Node ids reachable from `start`. */
export function component(g: NavGraph, start: string): Set<string> {
  const seen = new Set<string>([start]);
  const stack = [start];
  while (stack.length) {
    for (const n of g.adj.get(stack.pop()!) ?? []) {
      if (seen.has(n)) continue;
      seen.add(n);
      stack.push(n);
    }
  }
  return seen;
}
