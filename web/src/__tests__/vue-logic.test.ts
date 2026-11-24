// The Vue-only pieces: SSE stream core, Markdown pipeline, router guard, sidebar grouping,
// main-page helpers and the office style helper.
import { describe, expect, it, vi } from "vitest";
import { openEventStream, type StreamState } from "@/api/sse";
import { sidebarGroups } from "@/components/shell/projects";
import { defaultUrlTransform, markdownTree } from "@/lib/markdown";
import { coordinatorBusy, coordinatorState, sidePanelOf, threadPath, waitingIndex, waitingSummary } from "@/pages/main/logic";
import { sx } from "@/office/sx";
import { guard, safeFrom } from "@/router";
import { question, thread } from "./fixtures";

class FakeSource {
  static all: FakeSource[] = [];
  readyState = 0;
  onopen: (() => void) | null = null;
  onerror: (() => void) | null = null;
  listeners = new Map<string, (e: MessageEvent) => void>();
  closed = false;
  constructor(public url: string) {
    FakeSource.all.push(this);
  }
  addEventListener(name: string, fn: (e: MessageEvent) => void) {
    this.listeners.set(name, fn);
  }
  close() {
    this.closed = true;
  }
  emit(name: string, data: string, id = "") {
    this.listeners.get(name)?.({ data, lastEventId: id } as MessageEvent);
  }
}

function stream(startId: string | null = null) {
  FakeSource.all = [];
  const states: StreamState[] = [];
  const events: [string, unknown, string][] = [];
  const close = openEventStream({
    url: "/api/projects/p1/events",
    events: ["message", "thread"],
    startId,
    onEvent: (t, d, id) => events.push([t, d, id]),
    onState: (s) => states.push(s),
    create: (u) => new FakeSource(u) as unknown as EventSource,
  });
  return { states, events, close };
}

describe("openEventStream", () => {
  it("resumes from the snapshot id and parses frames", () => {
    const s = stream("41");
    const src = FakeSource.all[0];
    expect(src.url).toBe("/api/projects/p1/events?last_event_id=41");
    src.onopen?.();
    src.emit("message", '{"a":1}', "42");
    src.emit("thread", "not json", "43");
    expect(s.states).toEqual(["live"]);
    expect(s.events).toEqual([
      ["message", { a: 1 }, "42"],
      ["thread", "not json", "43"],
    ]);
    s.close();
    expect(src.closed).toBe(true);
  });
  it("reopens with the last seen id after the browser gives up, with backoff", () => {
    vi.useFakeTimers();
    const s = stream();
    const first = FakeSource.all[0];
    first.emit("message", "{}", "7");
    first.readyState = 0;
    first.onerror?.();
    expect(s.states).toEqual(["connecting"]);
    first.readyState = 2;
    first.onerror?.();
    expect(s.states.at(-1)).toBe("down");
    vi.advanceTimersByTime(999);
    expect(FakeSource.all).toHaveLength(1);
    vi.advanceTimersByTime(1);
    expect(FakeSource.all[1].url).toBe("/api/projects/p1/events?last_event_id=7");
    s.close();
    FakeSource.all[1].readyState = 2;
    FakeSource.all[1].onerror?.();
    vi.advanceTimersByTime(60_000);
    expect(FakeSource.all).toHaveLength(2);
    vi.useRealTimers();
  });
});

type El = { type: string; tagName?: string; properties?: Record<string, unknown>; children?: El[]; value?: string };
const find = (n: El, tag: string): El[] => [...(n.tagName === tag ? [n] : []), ...(n.children ?? []).flatMap((c) => find(c, tag))];
const text = (n: El): string => (n.type === "text" ? (n.value ?? "") : (n.children ?? []).map(text).join(""));

describe("markdown", () => {
  it("stops bare links before CJK text", () => {
    const [a] = find(markdownTree("见 https://x.dev/pull/1。下一句") as El, "a");
    expect(a.properties?.href).toBe("https://x.dev/pull/1");
    expect(text(a)).toBe("https://x.dev/pull/1");
  });
  it("drops unsafe URLs, shows raw HTML as text, keeps GFM tables", () => {
    const tree = markdownTree('[x](javascript:alert(1)) <b onclick="y">raw</b>\n\n| a | b |\n|---|---|\n| 1 | 2 |') as El;
    expect(find(tree, "a")[0].properties?.href).toBe("");
    expect(find(tree, "b")).toHaveLength(0);
    expect(text(tree)).toContain('<b onclick="y">raw</b>');
    expect(find(tree, "table")).toHaveLength(1);
  });
  it("url transform matches react-markdown's default", () => {
    expect(defaultUrlTransform("/a/b?c:d")).toBe("/a/b?c:d");
    expect(defaultUrlTransform("mailto:a@b.c")).toBe("mailto:a@b.c");
    expect(defaultUrlTransform("data:text/html,x")).toBe("");
    const custom = markdownTree("![i](x.png)", (u) => `/files/${u}`) as El;
    expect(find(custom, "img")[0].properties?.src).toBe("/files/x.png");
  });
});

describe("router guard", () => {
  const me = (role = "member") => ({ id: "u", email: "a@b.c", role }) as never;
  const to = (path: string, meta: Record<string, unknown> = {}, query: Record<string, string> = {}, fullPath?: string) => ({ path, meta, query, fullPath });
  it("sends signed-out users to login and back after", () => {
    expect(guard(to("/projects/p1"), null)).toBe("/login?from=%2Fprojects%2Fp1");
    expect(guard(to("/login", { public: true }), null)).toBe(true);
    expect(guard(to("/login", { public: true }, { from: "/projects/p1" }), me())).toBe("/projects/p1");
    expect(guard(to("/login", { public: true }), me())).toBe("/projects");
  });
  it("keeps the query of the page to come back to", () => {
    const target = guard(to("/admin/users", { admin: true }, { x: "1" }, "/admin/users?x=1"), null);
    expect(target).toBe("/login?from=%2Fadmin%2Fusers%3Fx%3D1");
    const from = new URLSearchParams(String(target).split("?")[1]).get("from");
    expect(guard(to("/login", { public: true }, { from: from ?? "" }), me("admin"))).toBe("/admin/users?x=1");
  });
  it("only follows `from` to paths on this site", () => {
    for (const bad of ["https://evil.example/x", "//evil.example", "/\\evil.example", "/\t/evil.example", "javascript:alert(1)", "evil", "", "/login", "/login?from=/x"]) {
      expect(guard(to("/login", { public: true }, { from: bad }), me())).toBe("/projects");
    }
    expect(guard({ path: "/login", meta: { public: true }, query: { from: ["/a", "/b"] } } as never, me())).toBe("/projects");
    expect(safeFrom("/projects/p1?tab=x#y")).toBe("/projects/p1?tab=x#y");
  });
  it("shows non-admins the 404 page at the same address", () => {
    expect(guard(to("/admin/users", { admin: true }, { x: "1" }), me())).toEqual({ name: "not-found", params: { rest: ["admin", "users"] }, query: { x: "1" }, hash: undefined, replace: true });
    expect(guard(to("/admin/users", { admin: true }), me("admin"))).toBe(true);
    expect(guard(to("/settings/team"), me("admin"))).toBe(true);
    expect(guard(to("/projects/p1"), me("admin"))).toBe(true);
  });
});

describe("shell and main helpers", () => {
  it("groups the sidebar: pinned, the rest, archived", () => {
    const p = (id: string, extra: object = {}) => ({ id, name: id, description: "", repos: [], pinned: false, archived: false, ...extra }) as never;
    const v = sidebarGroups([p("a", { pinned: true }), p("b"), p("c", { archived: true })], "");
    expect(v.groups.map((g) => [g.title, g.items.map((x: { id: string }) => x.id)])).toEqual([
      ["已置顶", ["a"]],
      ["最近", ["b"]],
    ]);
    expect(v.archived).toHaveLength(1);
    expect(sidebarGroups([p("a"), p("b")], "zz").groups).toEqual([]);
  });
  it("panel, paths, waiting", () => {
    expect(sidePanelOf("library")).toBe("library");
    expect(sidePanelOf(["x"])).toBe("threads");
    expect(threadPath("p", 3, "tasks")).toBe("/projects/p/threads/3/tasks");
    const qs = [question("a", { thread_no: 2 }), question("b", { thread_no: 2 }), question("c", { thread_no: null })];
    expect([...waitingIndex(qs).entries()].map(([no, q]) => [no, q.id])).toEqual([[2, "a"]]);
    const s = waitingSummary([thread(1), thread(2, { status: "review" }), thread(3, { counts: { ...thread(3).counts, waiting: 2 } })]);
    expect(s.count).toBe(3);
    expect(s.first?.no).toBe(2);
  });
  it("coordinator state", () => {
    const c = (node: object | null, state = "idle") => ({ conversation_id: "c", employee: { name: "lead", node, status: { state } } }) as never;
    expect(coordinatorState(undefined)).toEqual({ tone: "#B7B3AA", text: "leader 待命" });
    expect(coordinatorState(c({ online: false })).text).toBe("lead 离线");
    expect(coordinatorState(c({ online: true })).tone).toBe("#2E7D4F");
    expect(coordinatorBusy(c({ online: true }, "typing"))).toBe(true);
    expect(coordinatorBusy(c({ online: true }, "offline"))).toBe(false);
  });
  it("office styles get px like React", () => {
    expect(sx({ left: 4, opacity: 0.5, lineHeight: "13px", fontWeight: 600, padding: "1px 2px", top: undefined })).toEqual({ left: "4px", opacity: 0.5, lineHeight: "13px", fontWeight: 600, padding: "1px 2px" });
  });
});
