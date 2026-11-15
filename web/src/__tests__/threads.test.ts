import { QueryClient } from "@tanstack/react-query";
import { describe, expect, it } from "vitest";
import { applyProjectEvent } from "@/api/events";
import { keys } from "@/api/queries";
import type { EmployeeBrief, ThreadView } from "@/api/views";
import { detailTabOf } from "@/components/app/ThreadDetail";
import { menuItems } from "@/components/app/ThreadMenu";
import { IMAGE_MAX_BYTES, IMAGE_MAX_COUNT, imageProblem, pickImages } from "@/lib/images";
import { activityLine, cardsByAsker, elapsed, featured, isWorking, liveLine, stateLabel, taskProgress, waitingReason } from "@/lib/live";
import { clampWidth, dragWidth, loadWidth, PANEL_DEFAULT, PANEL_MAX, PANEL_MIN, panelKey, saveWidth } from "@/lib/panelWidth";
import { groupThreads } from "@/lib/present";
import { threadPath } from "@/pages/Main";
import { question, task, thread } from "./fixtures";

function member(name: string, state: string | null, patch: Partial<EmployeeBrief> = {}): EmployeeBrief {
  return {
    id: `e-${name}`, name, tier: name.startsWith("lead") ? "lead" : "member", role: "dev", title: "", runtime: "claude", node: null,
    look: { color: "#000", outfit: "tee", accessory: "none", hair: null }, status: state ? { state, bubble: "", since: "2026-11-14T10:00:00Z" } : null, ...patch,
  };
}

describe("live progress", () => {
  it("labels states and picks the activity line", () => {
    const running = member("dev-1", "running", { status: { state: "running", bubble: "Bash npm test" } });
    expect(isWorking(running)).toBe(true);
    expect(isWorking(member("dev-2", "idle"))).toBe(false);
    expect(stateLabel(running)).toBe("在跑");
    expect(activityLine(running)).toBe("Bash npm test");
    const asking = member("dev-3", "raising_hand");
    const card = question("q1", { asker: "dev-3", kind: "approval", detail: { input: { command: "rm -rf build" } } });
    expect(activityLine(asking, card)).toBe("请求执行 rm -rf build");
  });

  it("explains why a thread waits", () => {
    const t = thread(1);
    const approval = question("q1", { kind: "approval" });
    expect(waitingReason(t, [approval, question("q2", { kind: "approval" })])).toBe("等你审批 2 条命令，员工停在这一步");
    expect(waitingReason(t, [approval, question("q3")])).toBe("等你处理 1 个审批和 1 个问题");
    expect(waitingReason(thread(2, { status: "review", pr_number: 7 }), [])).toBe("PR #7 等你验收");
    expect(waitingReason(t, [])).toBe("");
    expect(cardsByAsker([question("a", { asker: "x" }), question("b", { asker: "x" })]).get("x")?.id).toBe("a");
  });

  it("counts task progress without cancelled tasks", () => {
    const p = taskProgress([task("t1", { status: "done" }), task("t2", { status: "running" }), task("t3", { status: "cancelled" }), task("t4", { status: "failed" })]);
    expect(p).toEqual({ done: 1, total: 3, running: 1, failed: 1, pct: 33 });
    expect(taskProgress([]).pct).toBe(0);
  });

  it("formats elapsed time", () => {
    const from = "2026-11-14T10:00:00Z";
    const at = (s: number) => Date.parse(from) + s * 1000;
    expect(elapsed(from, at(42))).toBe("42 秒");
    expect(elapsed(from, at(12 * 60 + 5))).toBe("12 分");
    expect(elapsed(from, at(2 * 3600 + 5 * 60))).toBe("2 小时 5 分");
    expect(elapsed(from, at(3600))).toBe("1 小时");
    expect(elapsed(from, at(3 * 86400 + 2 * 3600))).toBe("3 天 2 小时");
    expect(elapsed(null, at(1))).toBe("");
  });

  it("builds the compact chat line", () => {
    const working = thread(3, { members: [member("lead-1", "thinking"), member("dev-1", "running", { status: { state: "running", bubble: "Bash ls" } })] });
    expect(featured(working)?.name).toBe("dev-1");
    expect(liveLine(working)).toBe("dev-1 在跑 · Bash ls");
    const asks = thread(4, { counts: { ...thread(4).counts, approvals_open: 2, waiting: 2 } });
    expect(liveLine(asks)).toBe("等你审批 2 条命令");
    expect(liveLine(thread(5, { status: "done", members: [member("dev-1", "running")] }))).toBe("");
    expect(liveLine(thread(6, { status: "paused" }))).toBe("已暂停");
    expect(liveLine(thread(7, { members: [member("lead-1", "idle")] }))).toBe("");
  });
});

describe("thread lifecycle in the UI", () => {
  it("offers the right menu items", () => {
    expect(menuItems({ status: "running" })).toEqual({ pause: true, resume: false, archive: true, unarchive: false });
    expect(menuItems({ status: "paused" })).toMatchObject({ pause: false, resume: true });
    expect(menuItems({ status: "archived" })).toEqual({ pause: false, resume: false, archive: false, unarchive: true });
  });

  it("keeps archived threads out of the groups", () => {
    const groups = groupThreads([thread(1), thread(2, { status: "archived" }), thread(3, { status: "done" })]);
    expect(groups.flatMap((g) => g.items.map((t) => t.no)).sort()).toEqual([1, 3]);
  });

  it("drops a deleted thread from the cache", () => {
    const qc = new QueryClient();
    qc.setQueryData<ThreadView[]>(keys.threads("p1"), [thread(1), thread(2)]);
    qc.setQueryData(keys.thread("t2"), thread(2));
    applyProjectEvent(qc, "p1", "thread", { deleted: { id: "t2", no: 2 } });
    expect(qc.getQueryData<ThreadView[]>(keys.threads("p1"))?.map((t) => t.no)).toEqual([1]);
    expect(qc.getQueryData(keys.thread("t2"))).toBeUndefined();
    applyProjectEvent(qc, "p1", "thread", { thread: thread(3) });
    expect(qc.getQueryData<ThreadView[]>(keys.threads("p1"))?.map((t) => t.no)).toEqual([3, 1]);
  });

  it("deep links threads and tabs", () => {
    expect(threadPath("p1", 12)).toBe("/projects/p1/threads/12");
    expect(threadPath("p1", 12, "progress")).toBe("/projects/p1/threads/12");
    expect(threadPath("p1", 12, "deliverables")).toBe("/projects/p1/threads/12/deliverables");
    expect(detailTabOf("members")).toBe("members");
    expect(detailTabOf("full")).toBe("progress");
    expect(detailTabOf(undefined)).toBe("progress");
  });
});

describe("panel width", () => {
  it("clamps to the limits and the window", () => {
    expect(clampWidth(100, 1600)).toBe(PANEL_MIN);
    expect(clampWidth(5000, 1600)).toBe(PANEL_MAX);
    expect(clampWidth(700, 1000)).toBe(600);
    expect(clampWidth(Number.NaN, 1600)).toBe(PANEL_DEFAULT);
    expect(dragWidth(440, 900, 800, 1600)).toBe(540);
  });

  it("remembers the width per user and survives broken storage", () => {
    const store = new Map<string, string>();
    const storage = { getItem: (k: string) => store.get(k) ?? null, setItem: (k: string, v: string) => void store.set(k, v), removeItem: (k: string) => void store.delete(k) };
    saveWidth("u1", 520, storage);
    expect(store.get(panelKey("u1"))).toBe("520");
    expect(loadWidth("u1", storage)).toBe(520);
    expect(loadWidth("u2", storage)).toBe(PANEL_DEFAULT);
    saveWidth("u1", PANEL_DEFAULT, storage);
    expect(store.has(panelKey("u1"))).toBe(false);
    const broken = {
      getItem: () => {
        throw new Error("blocked");
      },
      setItem: () => {
        throw new Error("blocked");
      },
      removeItem: () => {
        throw new Error("blocked");
      },
    };
    expect(loadWidth("u1", broken)).toBe(PANEL_DEFAULT);
    expect(() => saveWidth("u1", 500, broken)).not.toThrow();
  });
});

describe("image input", () => {
  it("accepts PNG, JPEG, GIF and WebP up to 5 MB", () => {
    expect(imageProblem({ name: "a.png", type: "image/png", size: 10 })).toBeNull();
    expect(imageProblem({ name: "a.svg", type: "image/svg+xml", size: 10 })).toContain("不是");
    expect(imageProblem({ name: "a.pdf", type: "application/pdf", size: 10 })).toContain("不是");
    expect(imageProblem({ name: "big.jpg", type: "image/jpeg", size: IMAGE_MAX_BYTES + 1 })).toContain("5 MB");
  });

  it("caps the images per message", () => {
    const img = (i: number) => ({ name: `${i}.png`, type: "image/png", size: 1 });
    const { ok, problems } = pickImages([img(1), img(2), img(3), { name: "x.txt", type: "text/plain", size: 1 }], IMAGE_MAX_COUNT - 2);
    expect(ok.map((f) => f.name)).toEqual(["1.png", "2.png"]);
    expect(problems).toHaveLength(2);
  });
});
