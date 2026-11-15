import { describe, expect, it } from "vitest";
import { compileScene } from "../compile";
import { addEdge, addNode, advance, astar, createGraph, pathLength, route } from "../nav";
import { crew, defaultLayout, twoThreads } from "./fixtures";

describe("astar", () => {
  it("finds the shortest path by Euclidean length", () => {
    const g = createGraph();
    addNode(g, "a", [0, 0]);
    addNode(g, "b", [10, 0]);
    addNode(g, "c", [5, 1]);
    addNode(g, "d", [5, 50]);
    addEdge(g, "a", "d");
    addEdge(g, "d", "b");
    addEdge(g, "a", "c");
    addEdge(g, "c", "b");
    expect(astar(g, "a", "b")).toEqual(["a", "c", "b"]);
  });

  it("returns undefined when unreachable", () => {
    const g = createGraph();
    addNode(g, "a", [0, 0]);
    addNode(g, "b", [1, 0]);
    expect(astar(g, "a", "b")).toBeUndefined();
  });
});

describe("route through the office", () => {
  it("desk → task board goes up the pod aisle and out of the pod door", () => {
    const l = defaultLayout();
    const scene = compileScene(l, { threads: twoThreads, employees: crew });
    const seat = scene.seats.get("dev-2")!;
    const board = l.anchors.boardClaim;
    const path = route(scene.graph, seat.point, seat.accessNode, board.points[0], board.nav);
    expect(path[0]).toEqual([76, 266]);
    expect(path.slice(1, 4)).toEqual([[184, 238], [184, 88], [184, 42]]);
    expect(path[path.length - 1]).toEqual([352, 46]);
  });

  it("seat → lounge leaves the dev room by the south door", () => {
    const l = defaultLayout();
    const scene = compileScene(l, { threads: twoThreads, employees: crew });
    const seat = scene.seats.get("dev-1")!;
    const path = route(scene.graph, seat.point, seat.accessNode, l.anchors.lounge.points[0], l.anchors.lounge.nav);
    const nodes = path.map((p) => p.join(","));
    expect(nodes).toContain("368,438");
    expect(nodes).toContain("236,476");
  });
});

describe("advance", () => {
  it("moves along the polyline at constant speed and reports direction", () => {
    const pts: [number, number][] = [[10, 0], [10, 10]];
    const a = advance([0, 0], pts, 5);
    expect(a.pos).toEqual([5, 0]);
    expect(a.dx).toBe(1);
    const b = advance(a.pos, a.rest, 10);
    expect(b.pos).toEqual([10, 5]);
    expect(b.rest).toEqual([[10, 10]]);
    const c = advance(b.pos, b.rest, 100);
    expect(c.rest).toEqual([]);
    expect(c.pos).toEqual([10, 10]);
    expect(pathLength([[0, 0], ...pts])).toBe(20);
  });
});
