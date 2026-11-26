// Ported from web/src/__tests__/chrome.test.ts (sidePanelOf now lives in pages/main/logic.ts).
import { describe, expect, it } from "vitest";
import type { DeliverableView } from "@/api/views";
import { fileIcon, isImageDeliverable, isLibraryDeliverable, LIBRARY_ACCEPT, formatDateTime, libraryColumns, nextSort, ownerLabel, sortLibrary, typeLabel, uploadProblem } from "@/lib/library";
import { formatBytes } from "@/lib/bytes";
import { colorIcon, iconLook } from "@/lib/projectIcon";
import { fillDays, formatTokens, formatUSD, INSTRUCTIONS_MAX, modelLabel, sectionOf, worktreeChoice } from "@/lib/projectSettings";
import { describeCron, describeSchedule, looksLikeCron, presetOf, scheduleCreator, scheduleTarget } from "@/lib/schedules";

function item(id: string, patch: Partial<DeliverableView> = {}): DeliverableView {
  return { id, project_id: "p1", thread_no: null, kind: "doc", title: id, url: "", detail: {}, published_by: "dev-1", created_at: "2026-11-14T10:00:00Z", updated_at: "2026-11-14T10:00:00Z", ...patch };
}

describe("project icon", () => {
  it("reads emoji, color and the default initial", () => {
    expect(iconLook("🚀", "agent team")).toEqual({ kind: "emoji", emoji: "🚀" });
    expect(iconLook(colorIcon("#2E7D4F"), "agent team")).toEqual({ kind: "initial", letter: "A", bg: "#2E7D4F1F", fg: "#2E7D4F" });
    const plain = iconLook("", "sup2api");
    expect(plain.kind === "initial" && plain.letter).toBe("S");
    expect(iconLook("color:#zz", "x").kind).toBe("initial");
  });
});

describe("library", () => {
  it("formats sizes in decimal units", () => {
    expect(formatBytes(undefined, "decimal")).toBe("—");
    expect(formatBytes(512, "decimal")).toBe("512 B");
    expect(formatBytes(9400, "decimal")).toBe("9.4 kB");
    expect(formatBytes(208_600, "decimal")).toBe("209 kB");
    expect(formatBytes(8_900_000, "decimal")).toBe("8.9 MB");
  });
  it("labels types and owners", () => {
    expect(typeLabel(item("a", { kind: "design", detail: { hosted: true, entry: "06-nodes.png" } }))).toBe("PNG");
    expect(typeLabel(item("b", { kind: "board" }))).toBe("白板");
    expect(typeLabel(item("c", { kind: "doc" }))).toBe("文档");
    expect(ownerLabel("user")).toBe("你");
    expect(ownerLabel("")).toBe("平台");
    expect(ownerLabel("designer-1")).toBe("designer-1");
  });
  it("formats dates like the reference", () => {
    const d = new Date(2026, 10, 14, 4, 38);
    expect(formatDateTime(d.toISOString())).toBe("2026年11月14日 4:38");
  });
  it("sorts by name, size and date", () => {
    const list = [item("b", { title: "b.png", detail: { size: 10 } }), item("a", { title: "A.md", detail: { size: 30 }, updated_at: "2026-11-15T00:00:00Z" }), item("c", { title: "c" })];
    expect(sortLibrary(list, "name", "asc").map((d) => d.id)).toEqual(["a", "b", "c"]);
    expect(sortLibrary(list, "size", "desc").map((d) => d.id)).toEqual(["a", "b", "c"]);
    expect(sortLibrary(list, "modified", "desc")[0].id).toBe("a");
    expect(nextSort({ key: "name", dir: "asc" }, "name")).toEqual({ key: "name", dir: "desc" });
    expect(nextSort({ key: "name", dir: "asc" }, "modified")).toEqual({ key: "modified", dir: "desc" });
  });
  it("leaves hosted images out of the library", () => {
    expect(isImageDeliverable(item("a", { kind: "design", detail: { hosted: true, entry: "06-nodes.png", render: "image" } }))).toBe(true);
    expect(isImageDeliverable(item("b", { kind: "doc", detail: { hosted: true, entry: "flow.SVG" } }))).toBe(true);
    expect(isImageDeliverable(item("c", { kind: "doc", detail: { hosted: true, entry: "x", content_type: "image/gif" } }))).toBe(true);
    for (const [entry, render] of [["a.html", "html"], ["b.md", "markdown"], ["c.pdf", "pdf"], ["d.txt", "text"], ["e.csv", "text"]] as const)
      expect(isLibraryDeliverable(item(entry, { detail: { hosted: true, entry, render } }))).toBe(true);
    // links are not hosted files: a design link (even one ending in .png) and a PR stay listed
    expect(isLibraryDeliverable(item("d", { kind: "design", url: "https://figma.com/x.png" }))).toBe(true);
    expect(isLibraryDeliverable(item("e", { kind: "pr", url: "https://github.com/o/r/pull/1" }))).toBe(true);
  });
  it("refuses files the server would refuse", () => {
    for (const name of ["shot.PNG", "a.jpg", "b.jpeg", "c.gif", "d.webp", "e.svg"]) expect(uploadProblem({ name, size: 10 })).toContain("图片不是交付物");
    for (const name of ["a.html", "b.md", "c.pdf", "d.txt", "e.json", "f.csv"]) expect(uploadProblem({ name, size: 10 })).toBeNull();
    expect(LIBRARY_ACCEPT).not.toMatch(/png|jpe?g|gif|webp|svg/);
    expect(uploadProblem({ name: "run.sh", size: 10 })).toContain("不支持");
    expect(uploadProblem({ name: "big.pdf", size: 6 << 20 })).toContain("5 MB");
    expect(uploadProblem({ name: "empty.md", size: 0 })).toContain("空文件");
  });
});

describe("project settings", () => {
  it("picks sections and limits", () => {
    expect(sectionOf("usage")).toBe("usage");
    expect(sectionOf("nope")).toBe("general");
    expect(INSTRUCTIONS_MAX).toBe(16000);
    expect(worktreeChoice("clone").label).toBe("关闭，直接在检出目录工作");
    expect(worktreeChoice("").label).toBe("开启，使用工作树");
  });
  it("labels a project model override: follow the roles, a common name or the typed id", () => {
    expect(modelLabel("")).toBe("跟随岗位设置");
    expect(modelLabel("claude-opus-5-5")).toBe("Opus 5.5");
    expect(modelLabel("claude-y")).toBe("claude-y");
  });
  it("formats usage", () => {
    expect(formatTokens(950)).toBe("950");
    expect(formatTokens(12_340)).toBe("12.3K");
    expect(formatTokens(4_500_000)).toBe("4.5M");
    expect(formatUSD(0.5)).toBe("$0.50");
    expect(formatUSD(null)).toBe("—");
    const days = fillDays([{ day: "2026-11-14", cost_usd: 2 }], 3, new Date("2026-11-15T12:00:00Z"));
    expect(days).toEqual([{ day: "2026-11-13", cost: 0 }, { day: "2026-11-14", cost: 2 }, { day: "2026-11-15", cost: 0 }]);
  });
});

describe("schedules", () => {
  it("describes cron expressions", () => {
    expect(describeCron("0 9 * * *")).toBe("每天 9:00");
    expect(describeCron("0  *  * * *")).toBe("每小时");
    expect(describeCron("30 18 * * *")).toBe("每天 18:30");
    expect(describeCron("5 * * * *")).toBe("每小时第 5 分");
    expect(describeCron("0 10 * * 5")).toBe("每周五 10:00");
    expect(describeCron("0 9 1 * *")).toBe("0 9 1 * *");
    expect(presetOf("0 9 * * 1")).toBe("monday9");
    expect(presetOf("1 2 3 4 5")).toBe("custom");
    expect(looksLikeCron("0 9 * * 1-5")).toBe(true);
    expect(looksLikeCron("0 9 * *")).toBe(false);
    expect(looksLikeCron("0 9 * * mon")).toBe(false);
    expect(looksLikeCron("@daily")).toBe(true);
    expect(looksLikeCron("@every 1h30m")).toBe(true);
    expect(looksLikeCron("@sometimes")).toBe(false);
    expect(describeCron("@every 2h")).toBe("每 2 小时");
    expect(describeCron("@hourly")).toBe("每小时");
  });

  it("describes targets, creators and one-off times", () => {
    const at = new Date(2026, 10, 23, 15, 5);
    expect(describeSchedule({ kind: "once", cron: "", run_at: at.toISOString() })).toBe("一次 · 11-23 15:05");
    expect(describeSchedule({ kind: "cron", cron: "0 9 * * *" })).toBe("每天 9:00");
    expect(scheduleTarget({ target: "coordinator" })).toBe("统筹");
    expect(scheduleTarget({ target: "lead", thread_no: 12, thread_title: "发版" })).toBe("#12 发版 的组长");
    expect(scheduleCreator({ created_by: "lead" })).toBe("组长");
    expect(scheduleTarget({ target: "member", thread_no: 3, employee: "dev-1", task_id: "t2" })).toBe("dev-1（#3 任务 t2）");
    expect(scheduleCreator({ created_by: "member" })).toBe("员工");
  });
});

describe("pause lines", () => {
  it("names a usage-limit pause", async () => {
    const { pauseLine } = await import("@/lib/present");
    expect(pauseLine("rate_limit")).toBe("Claude 使用限额用完，已暂停");
    expect(pauseLine("")).toBe("已暂停");
    expect(pauseLine("rolled_back_to_v1")).toBe("已暂停：rolled_back_to_v1");
  });
});

describe("side panel", () => {
  it("reads the panel parameter", async () => {
    const { sidePanelOf } = await import("@/pages/main/logic");
    expect(sidePanelOf(null)).toBe("threads");
    expect(sidePanelOf("library")).toBe("library");
    expect(sidePanelOf("none")).toBe("none");
    expect(sidePanelOf("bogus")).toBe("threads");
  });
});

describe("library rows", () => {
  it("picks an outline icon by file extension, else by kind", () => {
    expect(fileIcon(item("a", { kind: "design", detail: { hosted: true, entry: "06-nodes.png" } }))).toBe("image");
    expect(fileIcon(item("b", { kind: "design", detail: { hosted: true, entry: "index.html" } }))).toBe("code");
    expect(fileIcon(item("c", { kind: "data", detail: { hosted: true, entry: "t.csv" } }))).toBe("table");
    expect(fileIcon(item("d", { kind: "pr" }))).toBe("link"); // PRs are no longer deliverables
    expect(fileIcon(item("e", { kind: "doc" }))).toBe("text");
    expect(fileIcon(item("f", { kind: "other" }))).toBe("link");
  });
  it("drops columns as the library narrows", () => {
    expect([...libraryColumns(900)].sort()).toEqual(["modified", "name", "owner", "size", "type"]);
    expect(libraryColumns(600).has("modified")).toBe(false);
    expect(libraryColumns(500).has("owner")).toBe(false);
    expect([...libraryColumns(300)].sort()).toEqual(["name", "type"]);
  });
});
