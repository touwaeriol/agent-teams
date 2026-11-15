import { describe, expect, it } from "vitest";
import {
  approvalCommand, checkLook, deliverableKind, groupThreads, mainProgress, markTone, memberDoing, memberTone, mergeLook, openForUser, poolColumns,
  projectTint, prLook, questionLine, taskLook, taskTitle, threadBases, threadKind, threadLine, threadPRs, visibleMainTasks, INK,
} from "@/lib/present";
import { question, task, thread } from "./fixtures";

const counts = (c: Partial<ReturnType<typeof thread>["counts"]>) => ({ ...thread(1).counts, ...c });

describe("threadKind / markTone", () => {
  it("review and finished states win", () => {
    expect(threadKind(thread(1, { status: "review", counts: counts({ questions_open: 2 }) }))).toBe("review");
    expect(threadKind(thread(1, { status: "done" }))).toBe("done");
  });
  it("an open question makes it 等你, an approval alone does not", () => {
    expect(threadKind(thread(1, { counts: counts({ questions_open: 1, waiting: 1 }) }))).toBe("wait");
    expect(threadKind(thread(1, { counts: counts({ approvals_open: 1, waiting: 1 }) }))).toBe("doing");
    expect(markTone(thread(1, { counts: counts({ approvals_open: 1, waiting: 1 }) }))).toBe(INK.wait);
  });
  it("paused and queued", () => {
    expect(threadKind(thread(1, { status: "paused" }))).toBe("paused");
    expect(threadKind(thread(1, { status: "queued" }))).toBe("queued");
    expect(markTone(thread(1, { status: "done" }))).toBe(INK.gray);
  });
});

describe("groupThreads", () => {
  it("puts a running thread with a waiting card in both waiting and active, hides archived and empty groups", () => {
    const groups = groupThreads([
      thread(12, { counts: counts({ waiting: 1 }) }),
      thread(11, { status: "review" }),
      thread(9, { status: "archived" }),
    ]);
    expect(groups.map((g) => g.id)).toEqual(["waiting", "active"]);
    expect(groups[0].items.map((t) => t.no).sort()).toEqual([11, 12]);
    expect(groups[0].title).toBe("等你处理 · 2");
    expect(groups[1].items.map((t) => t.no)).toEqual([12]);
  });
  it("sorts by most recent update", () => {
    const [g] = groupThreads([thread(1, { status: "done", updated_at: "2026-01-01T00:00:00Z" }), thread(2, { status: "done", updated_at: "2026-02-01T00:00:00Z" })]);
    expect(g.items.map((t) => t.no)).toEqual([2, 1]);
  });
});

describe("lines", () => {
  it("threadLine prefers the waiting question, then PR state, then the lead", () => {
    expect(threadLine(thread(1), question("q", { kind: "approval", detail: { input: { command: "ls" } } }))).toBe("dev-2 请求执行 ls");
    expect(threadLine(thread(1, { status: "review", pr_number: 41 }))).toBe("PR #41 等你验收");
    expect(threadLine(thread(1, { status: "done", pr_number: 39 }))).toBe("PR #39 已合并到 main");
    expect(threadLine(thread(1, { lead_last: { text: "进展", at: "" } }))).toBe("进展");
    expect(threadLine(thread(1, { status: "queued", queue_position: 2 }))).toContain("2");
  });
  it("questionLine names the lead as 组长", () => {
    expect(questionLine(question("q", { asker: "lead-13", text: "选哪个" }))).toBe("组长问：选哪个");
  });
  it("approvalCommand falls back through command, title, tool + input, text", () => {
    expect(approvalCommand({ detail: { input: { file_path: "/a" } }, text: "" })).toBe("/a");
    expect(approvalCommand({ detail: { title: "写文件" }, text: "" })).toBe("写文件");
    expect(approvalCommand({ detail: { tool_name: "Edit", input: { x: 1 } }, text: "" })).toBe('Edit {"x":1}');
    expect(approvalCommand({ detail: {}, text: "原文" })).toBe("原文");
  });
});

describe("progress and lists", () => {
  it("mainProgress uses counts or the list", () => {
    expect(mainProgress(thread(1, { counts: counts({ main_total: 4, main_done: 2 }) })).pct).toBe("50%");
    const t = thread(1, { main_tasks: [{ id: "a", pos: 1, title: "", status: "done", auto_done: true, tasks_total: 0, tasks_done: 0 }] });
    expect(mainProgress(t).text).toBe("1 / 1");
  });
  it("visibleMainTasks collapses beyond keep", () => {
    const list = [3, 1, 2].map((pos) => ({ id: String(pos), pos, title: "", status: "todo" as const, auto_done: false, tasks_total: 0, tasks_done: 0 }));
    expect(visibleMainTasks(list, 2, false)).toEqual({ rows: [list[1], list[2]], hidden: 1 });
    expect(visibleMainTasks(list, 2, true).hidden).toBe(0);
  });
  it("openForUser keeps open user questions, oldest first", () => {
    const list = [question("b", { created_at: "2026-01-02T00:00:00Z" }), question("a", { created_at: "2026-01-01T00:00:00Z" }), question("c", { audience: "lead" }), question("d", { status: "answered" })];
    expect(openForUser(list).map((q) => q.id)).toEqual(["a", "b"]);
  });
});

describe("tasks", () => {
  it("taskLook marks pending tasks with unfinished dependencies as 等依赖", () => {
    const all = [task("t1", { status: "running" }), task("t2", { depends_on: ["t1"] })];
    expect(taskLook(all[1], all).label).toBe("等依赖");
    expect(taskLook(task("t3"), all).label).toBe("待领取");
  });
  it("poolColumns drops empty optional columns and hides cancelled", () => {
    const cols = poolColumns([task("a"), task("b", { status: "decision" }), task("c", { status: "cancelled" }), task("d", { status: "failed" })]);
    expect(cols.map((c) => c.title)).toEqual(["待领取", "进行中", "待审查", "待决策", "完成"]);
    expect(cols[1].tasks.map((t) => t.id)).toEqual(["d"]);
  });
  it("taskTitle takes the first line", () => expect(taskTitle(task("x"))).toBe("do x"));
});

describe("looks", () => {
  it("members", () => {
    expect(memberTone({ status: null, node: null })).toBe(INK.idle);
    expect(memberTone({ status: { state: "raising_hand" }, node: null })).toBe(INK.wait);
    expect(memberDoing({ status: { state: "typing" } })).toBe("写代码");
    expect(memberDoing({ status: { state: "typing", bubble: "改 A" } })).toBe("改 A");
  });
  it("merge, checks, PR, deliverable kinds, tints", () => {
    expect(mergeLook("merged").label).toBe("已合并");
    expect(mergeLook("weird").label).toBe("weird");
    expect(checkLook({ status: "completed", conclusion: "success" }).label).toBe("通过");
    expect(checkLook({ status: "completed", conclusion: "failure" }).tone).toBe(INK.bad);
    expect(checkLook({ status: "in_progress", conclusion: null }).label).toBe("运行中");
    expect(prLook({ pr_number: null, pr_state: null, status: "running" }).label).toContain("全部主要任务");
    expect(prLook({ pr_number: 3, pr_state: "merged", status: "done" }).label).toBe("PR #3 已合并");
    const repos = [
      { repo: "o/web", base_branch: "dev", branch_created: true, pr_number: 7, pr_url: "https://github.com/o/web/pull/7", pr_state: "open" },
      { repo: "o/api", base_branch: "main", branch_created: true, pr_number: 8, pr_url: "https://github.com/o/api/pull/8", pr_state: "open" },
      { repo: "o/docs", base_branch: "", branch_created: false, pr_number: null, pr_url: null, pr_state: "" },
    ];
    expect(prLook({ pr_number: 7, pr_state: "open", status: "review", repos }).label).toBe("2 个 PR 等验收");
    expect(threadPRs({ pr_number: 7, pr_url: null, pr_state: "open", repos }).map((p) => `${p.repo}#${p.number}→${p.base}`)).toEqual(["o/web#7→dev", "o/api#8→main"]);
    expect(threadPRs({ pr_number: 5, pr_url: "u", pr_state: "open", base_branch: "main" })).toEqual([{ repo: "", number: 5, url: "u", base: "main", state: "open" }]);
    expect(threadBases({ base_branch: "", repos })).toBe("o/web → dev、o/api → main");
    expect(threadBases({ base_branch: "main" })).toBe("main");
    expect(threadLine(thread(1, { status: "done", pr_number: 7, repos }))).toBe("2 个 PR 已合并");
    expect(deliverableKind("slides").label).toBe("幻灯片");
    expect(deliverableKind("figma").label).toBe("链接");
    expect(projectTint("agent team")).toEqual(projectTint("agent team"));
  });
});
