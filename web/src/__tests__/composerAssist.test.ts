// @vitest-environment jsdom
// Composer assist: "/" and "@" triggers, platform commands, popup rows, picks, references kept
// in the text, file kinds, /status and /context panels, and the popup driving the real Composer.
import { QueryClient, VueQueryPlugin } from "@tanstack/vue-query";
import { flushPromises, mount } from "@vue/test-utils";
import { afterEach, describe, expect, it, vi } from "vitest";
import { createMemoryHistory, createRouter } from "vue-router";
import Composer from "@/components/app/Composer.vue";
import { applyPick, commandOf, findTrigger, mentionItems, moveIndex, refsInText, slashItems } from "@/components/app/composer/assist";
import { contextRows, statusSummary } from "@/components/app/composer/panels";
import { attachKind, pickImages } from "@/lib/images";
import { thread } from "./fixtures";

describe("assist logic", () => {
  it("finds / only at the start and @ after a space", () => {
    expect(findTrigger("/des", 4)).toEqual({ kind: "slash", start: 0, end: 4, query: "des" });
    expect(findTrigger("/design x", 9)).toBeNull();
    expect(findTrigger("看 /x", 4)).toBeNull();
    expect(findTrigger("看 @src/a", 8)).toEqual({ kind: "mention", start: 2, end: 8, query: "src/a" });
    expect(findTrigger("a@b", 3)).toBeNull();
    expect(findTrigger("@", 1)).toEqual({ kind: "mention", start: 0, end: 1, query: "" });
  });

  it("lists platform commands per scope, then skills, filtered as typed", () => {
    const skills = [{ name: "design", description: "做原型", argument_hint: "", source: "skill" as const }, { name: "review:check", description: "", argument_hint: "", source: "plugin" as const }];
    const all = slashItems("", "lead", skills);
    expect(all.filter((i) => i.group === "命令").map((i) => i.label)).toEqual(["/model", "/effort", "/status", "/context", "/skills", "/usage", "/memory", "/config"]);
    expect(all.filter((i) => i.group === "技能").map((i) => i.insert)).toEqual(["/design ", "/review:check "]);
    expect(slashItems("", "coordinator", []).some((i) => i.label === "/effort")).toBe(false);
    expect(slashItems("des", "lead", skills).map((i) => i.label)).toEqual(["/design"]);
    expect(slashItems("chk", "lead", skills).map((i) => i.label)).toEqual(["/review:check"]);
    expect(commandOf(" /status ", "lead")).toBe("status");
    expect(commandOf("/effort", "coordinator")).toBeNull();
    expect(commandOf("/design 做", "lead")).toBeNull();
  });

  it("groups @ candidates and keeps only references whose token is still in the text", () => {
    const items = mentionItems([
      { kind: "memory", label: "deploy.md", detail: "部署", name: "deploy.md" },
      { kind: "thread", label: "#3 登录", detail: "进行中", no: 3 },
      { kind: "file", label: "src/app.go", detail: "o/r@main", repo: "o/r", path: "src/app.go" },
    ]);
    expect(items.map((i) => [i.group, i.insert])).toEqual([["文件", "@src/app.go "], ["讨论串", "@#3 "], ["记忆", "@deploy.md "]]);
    const t = findTrigger("看 @ap", 5)!;
    expect(applyPick("看 @ap 吧", t, "@src/app.go ")).toEqual({ text: "看 @src/app.go  吧", caret: 14 });
    const refs = items.map((i) => i.ref!);
    expect(refsInText("看 @src/app.go 和 @#3", [...refs, refs[0]!])).toEqual([
      { kind: "file", repo: "o/r", path: "src/app.go" },
      { kind: "thread", no: 3 },
    ]);
    expect(moveIndex(0, -1, 3)).toBe(2);
    expect(moveIndex(2, 1, 3)).toBe(0);
  });

  it("accepts images, PDFs and text files by kind", () => {
    expect(attachKind({ type: "image/png", name: "a.png" })).toBe("image");
    expect(attachKind({ type: "", name: "spec.PDF" })).toBe("pdf");
    expect(attachKind({ type: "", name: "main.go" })).toBe("text");
    expect(attachKind({ type: "", name: "Makefile" })).toBe("text");
    expect(attachKind({ type: "", name: "a.exe" })).toBeNull();
    const { ok, problems } = pickImages([{ type: "", name: "big.md", size: 2 << 20 }, { type: "", name: "ok.md", size: 10 }], 0);
    expect(ok.map((f) => f.name)).toEqual(["ok.md"]);
    expect(problems).toEqual(["big.md 超过 1 MB"]);
  });

  it("summarises /status and /context", () => {
    const busy = thread(2, { status: "running", members: [{ ...thread(2).members[0]!, name: "dev-1", status: { state: "typing", bubble: "写表单" } }] } as never);
    const s = statusSummary({ threads: [busy, thread(3, { status: "done" })], waiting: { questions: [{} as never], reviews: [] }, nodes: [{ online: true } as never, { online: false } as never] });
    expect(s).toEqual({ busy: [{ name: "dev-1", where: "#2", doing: "写表单" }], activeThreads: 1, cards: 1, reviews: 0, nodesOnline: 1, nodesTotal: 2 });
    const rows = contextRows({
      employee: "lead-1", runtime: "claude", instructions_chars: 12, memory_index_chars: 0, memory_files: 2, messages: 4, message_chars: 300,
      session: { state: "active", model: "claude-opus-5-5", turns: 2, cost_usd: 1, total: { input: 10, output: 200, cache_read: 3000, cache_write: 100 }, last_turn: { input: 5, output: 100, cache_read: 1500, cache_write: 50 } },
    });
    expect(Object.fromEntries(rows)).toMatchObject({ 项目指令: "12 字", "MEMORY.md": "没有", 会话: "claude-opus-5-5 · 2 轮" });
    expect(rows.find(([k]) => k === "上一轮")![1]).toContain("缓存读");
  });
});

const json = (v: unknown) => new Response(JSON.stringify(v), { status: 200, headers: { "Content-Type": "application/json" } });

function stubApi() {
  return vi.spyOn(globalThis, "fetch").mockImplementation(async (input) => {
    const url = (input as Request).url;
    if (url.includes("/commands")) return json({ employee: "lead-1", tier: "lead", runtime: "claude", from: "session", commands: [{ name: "design", description: "做原型", argument_hint: "", source: "skill" }] });
    if (url.includes("/references")) return json({ items: [{ kind: "file", label: "src/app.go", detail: "o/r@main", repo: "o/r", path: "src/app.go" }], files_note: "" });
    if (url.includes("/context")) return json({ employee: "lead-1", runtime: "claude", instructions_chars: 3, memory_index_chars: 10, memory_files: 0, messages: 1, message_chars: 5, session: null });
    return json({});
  });
}

function mountComposer(onSend: ReturnType<typeof vi.fn>) {
  const router = createRouter({ history: createMemoryHistory(), routes: [{ path: "/:p(.*)*", component: { render: () => null } }] });
  const w = mount(Composer, {
    props: { label: "发给组长", placeholder: "x", onSend: onSend as never, variant: "inline", assist: { pid: "p1", cid: "c1", scope: "lead" } },
    global: { plugins: [[VueQueryPlugin, { queryClient: new QueryClient() }], router] as never[] },
    attachTo: document.body,
  });
  return { w, router };
}

const settle = async (ms = 0) => {
  await flushPromises();
  if (ms) await new Promise((r) => setTimeout(r, ms));
  await flushPromises();
};

describe("Composer with assist", () => {
  afterEach(() => {
    vi.restoreAllMocks();
    document.body.innerHTML = "";
  });

  it("picks a skill from the / popup with the keyboard and sends it as typed", async () => {
    stubApi();
    const onSend = vi.fn().mockResolvedValue(undefined);
    const { w } = mountComposer(onSend);
    const input = w.get("input");
    await input.setValue("/des");
    await settle();
    expect(document.body.textContent).toContain("技能");
    expect(w.findAll("[role=option]").map((o) => o.text())).toEqual([expect.stringContaining("/design")]);
    await input.trigger("keydown", { key: "Enter" });
    await settle();
    expect((input.element as HTMLInputElement).value).toBe("/design ");
    expect(w.find("[data-testid=assist-popup]").exists()).toBe(false);
    await input.setValue("/design 登录页");
    await input.trigger("keydown", { key: "Enter" });
    await settle();
    expect(onSend).toHaveBeenCalledWith("/design 登录页", [], []);
  });

  it("runs a platform command instead of sending it, and opens settings ones", async () => {
    stubApi();
    const onSend = vi.fn();
    const { w, router } = mountComposer(onSend);
    const input = w.get("input");
    await input.setValue("/context");
    await input.trigger("keydown", { key: "Escape" });
    await input.trigger("keydown", { key: "Enter" });
    await settle();
    expect(onSend).not.toHaveBeenCalled();
    expect(w.get("[data-testid=command-panel]").text()).toContain("MEMORY.md");
    await input.setValue("/usa");
    await settle();
    await input.trigger("keydown", { key: "Enter" });
    await settle();
    expect(router.currentRoute.value.path).toBe("/projects/p1/settings/usage");
    expect((input.element as HTMLInputElement).value).toBe("");
  });

  it("adds an @ file reference that goes with the message", async () => {
    stubApi();
    const onSend = vi.fn().mockResolvedValue(undefined);
    const { w } = mountComposer(onSend);
    const input = w.get("input");
    await input.setValue("看 @app");
    await settle(200);
    expect(document.body.textContent).toContain("文件");
    await input.trigger("keydown", { key: "Tab" });
    await settle();
    expect((input.element as HTMLInputElement).value).toBe("看 @src/app.go ");
    expect(w.text()).toContain("src/app.go");
    await input.trigger("keydown", { key: "Enter" });
    await settle();
    expect(onSend).toHaveBeenCalledWith("看 @src/app.go", [], [{ kind: "file", repo: "o/r", path: "src/app.go" }]);
  });
});
