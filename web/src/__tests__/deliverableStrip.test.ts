// @vitest-environment jsdom
import { beforeEach, describe, expect, it } from "vitest";
import type { DeliverableView } from "@/api/views";
import { markdownTree, stripForPreview } from "@/lib/markdown";
import { isNewDeliverable, isStripDeliverable, markSeen, parseSeen, resetSeenCache, scrollEdges, useSeenDeliverables, type SeenRecord } from "@/lib/deliverableStrip";

function item(id: string, patch: Partial<DeliverableView> = {}): DeliverableView {
  return { id, project_id: "p1", thread_no: null, kind: "doc", title: id, url: "", detail: {}, published_by: "dev-1", created_at: "2026-11-14T10:00:00Z", updated_at: "2026-11-14T10:00:00Z", ...patch };
}
const hosted = (kind: string, entry: string, render: string) => item(entry, { kind, detail: { hosted: true, entry, render: render as never } });

describe("isStripDeliverable", () => {
  it("drops PRs and plain external links", () => {
    expect(isStripDeliverable(item("a", { kind: "pr", url: "https://github.com/o/r/pull/1" }))).toBe(false);
    expect(isStripDeliverable(item("b", { kind: "doc", url: "https://docs.example.com/x" }))).toBe(false);
    expect(isStripDeliverable(item("c", { kind: "slides", url: "https://slides.example.com/x" }))).toBe(false);
    expect(isStripDeliverable(item("d", { kind: "figma", url: "https://figma.com/x" }))).toBe(false);
    // even a hosted file can't sneak a PR in
    expect(isStripDeliverable({ kind: "pr", detail: { hosted: true, entry: "a.md", render: "markdown" } })).toBe(false);
  });
  it("keeps hosted md / html / pdf / txt and designs", () => {
    expect(isStripDeliverable(hosted("doc", "架构.md", "markdown"))).toBe(true);
    expect(isStripDeliverable(hosted("design", "index.html", "html"))).toBe(true);
    expect(isStripDeliverable(hosted("doc", "report.pdf", "pdf"))).toBe(true);
    expect(isStripDeliverable(hosted("doc", "notes.txt", "text"))).toBe(true);
    expect(isStripDeliverable(item("e", { kind: "design", url: "https://figma.com/x" }))).toBe(true);
  });
  it("drops hosted images, whatever the kind", () => {
    for (const entry of ["shot.png", "a.JPG", "b.jpeg", "c.gif", "d.webp", "e.svg"]) {
      expect(isStripDeliverable(hosted("design", entry, "image"))).toBe(false);
      expect(isStripDeliverable(hosted("doc", entry, "image"))).toBe(false);
    }
    // by extension even without render, and by content type
    expect(isStripDeliverable(item("x", { kind: "design", detail: { hosted: true, entry: "flow.svg" } }))).toBe(false);
    expect(isStripDeliverable(item("y", { kind: "design", detail: { hosted: true, entry: "blob", content_type: "image/png" } }))).toBe(false);
  });
  it("leaves raw data files to the library", () => {
    expect(isStripDeliverable(hosted("data", "t.csv", "text"))).toBe(false);
    expect(isStripDeliverable(hosted("data", "t.json", "text"))).toBe(false);
  });
});

describe("新内容", () => {
  const d = item("d1", { updated_at: "2026-11-14T10:00:00Z" });
  it("nothing is new without a record", () => {
    expect(isNewDeliverable(d, null)).toBe(false);
  });
  it("compares updated_at with since and the per-item seen time", () => {
    expect(isNewDeliverable(d, { since: "2026-11-14T09:00:00Z", seen: {} })).toBe(true);
    expect(isNewDeliverable(d, { since: "2026-11-14T11:00:00Z", seen: {} })).toBe(false);
    expect(isNewDeliverable(d, { since: "2026-11-14T09:00:00Z", seen: { d1: "2026-11-14T09:30:00Z" } })).toBe(true);
    expect(isNewDeliverable(d, { since: "2026-11-14T09:00:00Z", seen: { d1: "2026-11-14T10:30:00Z" } })).toBe(false);
  });
  it("is no longer new once seen, until it updates again", () => {
    const rec: SeenRecord = { since: "2026-11-14T09:00:00Z", seen: {} };
    const after = markSeen(rec, d, Date.parse("2026-11-14T10:05:00Z"));
    expect(isNewDeliverable(d, after)).toBe(false);
    expect(isNewDeliverable({ ...d, updated_at: "2026-11-14T10:10:00Z" }, after)).toBe(true);
    // server clock ahead of the browser: still counts as seen
    const ahead = markSeen(rec, { id: "d2", updated_at: "2026-11-14T12:00:00Z" }, Date.parse("2026-11-14T10:05:00Z"));
    expect(isNewDeliverable({ id: "d2", updated_at: "2026-11-14T12:00:00Z" }, ahead)).toBe(false);
  });
  it("keeps at most 300 entries and moves since up to the latest forgotten one", () => {
    const base = Date.parse("2026-11-14T00:00:00Z");
    let rec: SeenRecord = { since: "2026-11-13T00:00:00Z", seen: {} };
    for (let i = 0; i < 302; i++) rec = markSeen(rec, { id: `x${i}`, updated_at: "2026-11-13T00:00:00Z" }, base + i * 1000);
    expect(Object.keys(rec.seen)).toHaveLength(300);
    expect(rec.seen.x0).toBeUndefined();
    expect(rec.seen.x1).toBeUndefined();
    expect(rec.since).toBe(new Date(base + 1000).toISOString());
    // the forgotten ones stay seen, until they update again
    expect(isNewDeliverable({ id: "x0", updated_at: new Date(base).toISOString() }, rec)).toBe(false);
    expect(isNewDeliverable({ id: "x1", updated_at: new Date(base + 1000).toISOString() }, rec)).toBe(false);
    expect(isNewDeliverable({ id: "x1", updated_at: new Date(base + 5000).toISOString() }, rec)).toBe(true);
  });
  it("parses stored records defensively", () => {
    expect(parseSeen(null)).toBeNull();
    expect(parseSeen("{oops")).toBeNull();
    expect(parseSeen('{"seen":{}}')).toBeNull();
    expect(parseSeen('{"since":"x","seen":{"a":"t","b":3}}')).toEqual({ since: "x", seen: { a: "t" } });
  });

  beforeEach(() => {
    localStorage.clear();
    resetSeenCache();
  });
  it("first use starts at now, per project, and marks persist", () => {
    const old = item("o", { updated_at: new Date(Date.now() - 60_000).toISOString() });
    const s = useSeenDeliverables(() => "p1");
    expect(s.isNew(old)).toBe(false);
    expect(parseSeen(localStorage.getItem("deliverables.seen.p1"))?.since).toBeTruthy();
    const fresh = item("n", { updated_at: new Date(Date.now() + 60_000).toISOString() });
    expect(s.isNew(fresh)).toBe(true);
    s.markSeen(fresh);
    expect(s.isNew(fresh)).toBe(false);
    resetSeenCache();
    expect(useSeenDeliverables(() => "p1").isNew(fresh)).toBe(false);
    expect(localStorage.getItem("deliverables.seen.p2")).toBeNull();
  });
  it("writes nothing without a project", () => {
    const s = useSeenDeliverables(() => "");
    expect(s.isNew(item("n", { updated_at: new Date(Date.now() + 60_000).toISOString() }))).toBe(false);
    s.markSeen(item("n"));
    expect(localStorage.length).toBe(0);
  });
});

describe("scrollEdges", () => {
  it("shows arrows only where there is more to see", () => {
    expect(scrollEdges(0, 300, 300)).toEqual({ left: false, right: false });
    expect(scrollEdges(0, 300, 900)).toEqual({ left: false, right: true });
    expect(scrollEdges(300, 300, 900)).toEqual({ left: true, right: true });
    expect(scrollEdges(600, 300, 900)).toEqual({ left: true, right: false });
    expect(scrollEdges(599.5, 300, 900)).toEqual({ left: true, right: false });
  });
});

describe("markdown preview", () => {
  it("turns links into text and drops images", () => {
    const tree = markdownTree("# 标题\n\n看 [文档](https://x.dev/a) 和 ![图](a.png)\n\n- [x] 完成");
    stripForPreview(tree);
    const tags: string[] = [];
    const walk = (n: { type: string; tagName?: string; children?: unknown[] }) => {
      if (n.type === "element") tags.push(n.tagName!);
      for (const c of n.children ?? []) walk(c as never);
    };
    walk(tree as never);
    expect(tags).not.toContain("a");
    expect(tags).not.toContain("img");
    expect(tags).toEqual(expect.arrayContaining(["h1", "span", "input"]));
    expect(JSON.stringify(tree)).toContain("文档");
  });
});
