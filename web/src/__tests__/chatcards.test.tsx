import { renderToStaticMarkup } from "react-dom/server";
import { MemoryRouter } from "react-router-dom";
import { describe, expect, it } from "vitest";
import { appendMessage } from "@/api/events";
import type { MessagePages } from "@/api/queries";
import type { MessageView, QuestionView } from "@/api/views";
import { ArtifactCard, PlanCard, ResumedNote, TodoCard } from "@/components/app/chatcards";
import { answerBody, answeredRows, askItems, chatQuestions, complete, emptyChoices, letter, oneClick, toggle, typeOther } from "@/lib/ask";
import { chatBlocks, threadTalk } from "@/lib/chat";
import { readScroll, restoredTop, scrollMarkOf, writeScroll } from "@/lib/persist";

const structured: QuestionView = {
  id: "q1", thread_no: 1, asker: "lead-1", task_id: null, audience: "user", kind: "question", text: "会话也直接切吗？", options: ["也直接切", "先保留"],
  detail: { questions: [{ question: "会话也直接切吗？", header: "会话", options: [{ label: "也直接切", description: "去掉 JWT", recommended: true }, { label: "先保留" }] }] },
  blocking: false, answer: null, status: "open", decided_by: null, created_at: "2026-11-15T10:00:00Z", answered_at: null,
};
const plain: QuestionView = { ...structured, id: "q2", text: "用哪个库？", options: ["A", "B"], detail: {} };

function msg(id: string, extra: Partial<MessageView>): MessageView {
  return { id, role: "system", author: "lead-1", kind: "chat", content: "", meta: {}, created_at: "2026-11-15T10:00:00Z", ...extra };
}

describe("question cards", () => {
  it("draws plain and structured questions the same way", () => {
    expect(askItems(plain)).toEqual([{ question: "用哪个库？", header: "", options: [{ label: "A" }, { label: "B" }] }]);
    expect(askItems(structured)[0].options[0].recommended).toBe(true);
    expect([0, 1, 2].map(letter).join("")).toBe("ABC");
    expect(oneClick(askItems(structured))).toBe(true);
    expect(oneClick([{ question: "x", header: "", multi_select: true, options: [] }])).toBe(false);
  });

  it("selects one option, toggles several, and 其他 replaces a single choice", () => {
    const c = emptyChoices(askItems(structured))[0];
    expect(toggle(c, "也直接切", false)).toEqual({ selected: ["也直接切"], other: "" });
    expect(toggle(toggle(c, "也直接切", false), "也直接切", false).selected).toEqual([]);
    const multi = toggle(toggle(c, "A", true), "B", true);
    expect(multi.selected).toEqual(["A", "B"]);
    expect(toggle(multi, "A", true).selected).toEqual(["B"]);
    expect(typeOther(toggle(c, "A", false), "自己写", false)).toEqual({ selected: [], other: "自己写" });
    expect(typeOther(multi, "再加一个", true).selected).toEqual(["A", "B"]);
    expect(complete([c])).toBe(false);
    expect(complete([{ selected: [], other: " x " }])).toBe(true);
  });

  it("sends choices for structured questions and text for plain ones", () => {
    expect(answerBody(structured, [{ selected: ["也直接切"], other: "" }])).toEqual({ answer: "", answers: [{ selected: ["也直接切"] }] });
    expect(answerBody(plain, [{ selected: ["B"], other: "" }])).toEqual({ answer: "B" });
    expect(answerBody(plain, [{ selected: [], other: "都不要" }])).toEqual({ answer: "都不要" });
  });

  it("collapses answered cards to the chosen rows", () => {
    const answered = { ...structured, status: "answered" as const, answer: "也直接切", detail: { ...structured.detail, answers: [{ question: "会话也直接切吗？", selected: ["也直接切"] }] } };
    expect(answeredRows(answered)).toEqual([{ question: "会话也直接切吗？", selected: ["也直接切"] }]);
    expect(answeredRows({ ...plain, answer: "B" })[0].selected).toEqual(["B"]);
    expect(answeredRows({ ...plain, answer: "都不要" })[0]).toMatchObject({ selected: [], other: "都不要" });
  });

  it("keeps the user's questions in the chat after answering, approvals only while open", () => {
    const list = [structured, { ...plain, status: "answered" as const }, { ...plain, id: "q3", status: "cancelled" as const }, { ...plain, id: "q4", kind: "approval" as const, status: "answered" as const }, { ...plain, id: "q5", audience: "lead" as const }];
    expect(chatQuestions(list).map((q) => q.id)).toEqual(["q1", "q2"]);
  });
});

describe("chat cards", () => {
  it("turns card messages into card blocks that the thread chat keeps", () => {
    const todos = msg("1", { kind: "todos", meta: { todos: [{ content: "a", status: "in_progress", active_form: "Doing a" }] } });
    const note = msg("2", { kind: "note", content: "[Task t1] brief" });
    expect(chatBlocks([todos, note]).map((b) => b.kind)).toEqual(["card", "line"]);
    expect(threadTalk(todos)).toBe(true);
    expect(threadTalk(note)).toBe(false);
  });

  it("draws the checklist with a spinner on the item in progress, the plan as Markdown, the artifact and the resumed note", () => {
    const todo = renderToStaticMarkup(<TodoCard message={msg("1", { kind: "todos", meta: { employee: "lead-1", todos: [{ content: "写测试", status: "completed" }, { content: "修 bug", status: "in_progress", active_form: "正在修 bug" }, { content: "发布", status: "pending" }] } })} />);
    expect(todo).toContain("1/3");
    expect(todo).toContain("at-spin");
    expect(todo).toContain("正在修 bug");
    expect(renderToStaticMarkup(<PlanCard message={msg("2", { kind: "plan", content: "# 计划\n\n1. 先写测试" })} />)).toContain("<h1>计划</h1>");
    const art = renderToStaticMarkup(
      <MemoryRouter>
        <ArtifactCard pid="p1" message={msg("3", { kind: "artifact", meta: { deliverable: { id: "dl_1", kind: "design", title: "登录页", url: "", hosted: true, entry: "index.html", version: 2 } } })} />
      </MemoryRouter>,
    );
    expect(art).toContain('href="/projects/p1/deliverables/dl_1"');
    expect(art).toContain("第 2 版");
    expect(renderToStaticMarkup(<ResumedNote message={msg("4", { kind: "resumed", content: "会话已恢复" })} />)).toContain("会话已恢复");
  });

  it("replaces an updated card in place", () => {
    const first = msg("1", { kind: "todos", meta: { todos: [{ content: "a", status: "pending" }] } });
    const data: MessagePages = { pages: [{ items: [first, msg("2", {})], next_cursor: null }], pageParams: [0] } as unknown as MessagePages;
    const next = appendMessage(data, { ...first, meta: { todos: [{ content: "a", status: "completed" }] } })!;
    expect(next.pages[0].items.map((m) => m.id)).toEqual(["1", "2"]);
    expect(next.pages[0].items[0].meta.todos?.[0].status).toBe("completed");
  });
});

describe("scroll and drafts", () => {
  it("keeps the offset from the bottom, following when near it", () => {
    expect(scrollMarkOf({ scrollTop: 900, scrollHeight: 2000, clientHeight: 1000 })).toEqual({ fromBottom: 0 });
    expect(scrollMarkOf({ scrollTop: 200, scrollHeight: 2000, clientHeight: 1000 })).toEqual({ fromBottom: 800 });
    expect(restoredTop({ fromBottom: 800 }, { scrollHeight: 2400, clientHeight: 1000 })).toBe(600);
    expect(restoredTop(null, { scrollHeight: 2400, clientHeight: 1000 })).toBe(1400);
    expect(restoredTop({ fromBottom: 5000 }, { scrollHeight: 2400, clientHeight: 1000 })).toBe(0);
  });

  it("survives a missing or throwing localStorage", () => {
    expect(readScroll("c1")).toBeNull();
    expect(() => writeScroll("c1", { fromBottom: 300 })).not.toThrow();
  });
});
