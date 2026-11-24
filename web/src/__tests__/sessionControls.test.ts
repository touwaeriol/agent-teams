// @vitest-environment jsdom
// Session controls (#53): the permission menu as listed by the server (numbered, bypass last),
// digits while it is open, 切换中… until the server writes back, the model / effort chip text,
// and the new-session defaults. Fixtures: one SessionControls per CLI.
import { QueryClient, VueQueryPlugin } from "@tanstack/vue-query";
import { flushPromises, mount } from "@vue/test-utils";
import { afterEach, describe, expect, it, vi } from "vitest";
import { keys } from "@/api/queries";
import ModeMenuBody from "@/components/app/session/controls/ModeMenuBody.vue";
import ModelMenuBody from "@/components/app/session/controls/ModelMenuBody.vue";
import SessionControlsBar from "@/components/app/session/controls/SessionControlsBar.vue";
import { toast } from "@/lib/toast";
import { appliesNote, effortFor, effortLabel, modeRows, modelChipText, noAutoNote, rowForKey, type ModeOption, type SessionControls } from "@/lib/sessionControls";

const mode = (id: ModeOption["id"], label: string, applies: ModeOption["applies"] = "now"): ModeOption => ({ id, label, hint: `${label}说明`, applies });
const opus = { id: "claude-opus-5-5", name: "Opus 5.5", efforts: ["low", "medium", "high", "xhigh", "max"], supports_auto: true, default: true };
const haiku = { id: "claude-haiku-4-5", name: "Haiku 4.5", efforts: [], supports_auto: false };

export const CLAUDE: SessionControls = {
  thread_id: "th1", runtime: "claude", live: true, permission_mode: "auto", model: "claude-opus-5-5", effort: "high", pending: null as never,
  // the server's order (bypass may come anywhere; the menu puts it last)
  modes: [mode("auto", "自动"), mode("bypassPermissions", "跳过权限确认"), mode("default", "手动"), mode("acceptEdits", "接受编辑"), mode("plan", "计划")],
  models: [opus, haiku], efforts: opus.efforts, node_outdated: false,
};
export const CODEX: SessionControls = {
  thread_id: "th2", runtime: "codex", live: true, permission_mode: "bypassPermissions", model: "gpt-5.5-codex", effort: "", pending: null as never,
  modes: [mode("auto", "自动", "next_turn"), mode("default", "手动", "next_turn"), mode("bypassPermissions", "跳过权限确认", "next_turn")],
  models: [{ id: "gpt-5.5-codex", name: "GPT-5.5 Codex", efforts: ["low", "medium", "high"], default_effort: "medium", supports_auto: false, default: true }],
  efforts: ["low", "medium", "high"], node_outdated: false,
};
export const AGY: SessionControls = {
  thread_id: "th3", runtime: "antigravity", live: true, permission_mode: "default", model: "gemini-4-neon-high", effort: "", pending: null as never,
  modes: [mode("default", "手动", "next_turn")], // the role asks: no 跳过权限确认
  models: [{ id: "gemini-4-neon-high", name: "Neon (high)", efforts: [], supports_auto: false }], efforts: [], node_outdated: false,
};

describe("session control menus", () => {
  it("numbers the server's modes with bypass last, and digits pick them", () => {
    const rows = modeRows(CLAUDE.modes);
    expect(rows.map((r) => `${r.key}:${r.id}`)).toEqual(["1:auto", "2:default", "3:acceptEdits", "4:plan", "5:bypassPermissions"]);
    expect(rows.at(-1)?.danger).toBe(true);
    expect(rowForKey(rows, "4")?.id).toBe("plan");
    expect(rowForKey(rows, "6")).toBeUndefined();
    expect(rowForKey(rows, "a")).toBeUndefined();
    // a model without auto: the server leaves 自动 out, the numbers close up
    expect(modeRows(CLAUDE.modes!.filter((m) => m.id !== "auto")).map((r) => r.key + r.id)).toEqual(["1default", "2acceptEdits", "3plan", "4bypassPermissions"]);
    expect(modeRows(AGY.modes).map((r) => r.id)).toEqual(["default"]);
    expect(modeRows(null)).toEqual([]);
  });
  it("says when a switch takes effect, by CLI", () => {
    expect(appliesNote("claude", CLAUDE.modes)).toMatch(/^立即生效/);
    expect(appliesNote("codex", CODEX.modes)).toMatch(/^下一轮生效。Codex/);
    expect(appliesNote("antigravity", AGY.modes)).toMatch(/^下一轮生效。Antigravity/);
  });
  it("labels the model chip and keeps or drops the effort on a model change", () => {
    expect(modelChipText(CLAUDE.models, CLAUDE.model, "high", "claude")).toBe("Opus 5.5 · 高");
    expect(modelChipText(CODEX.models, CODEX.model, "", "codex")).toBe("GPT-5.5 Codex");
    expect(modelChipText(AGY.models, AGY.model, "", "antigravity")).toBe("Neon (high)");
    expect(modelChipText([], "", "", "codex")).toBe("Codex 默认");
    expect(effortLabel("xhigh")).toBe("超高");
    expect(effortFor(haiku, "high")).toBe("");
    expect(effortFor(opus, "max")).toBe("max");
    expect(effortFor(CODEX.models![0], "xhigh")).toBe("medium");
    expect(noAutoNote("claude", CLAUDE.models, "claude-haiku-4-5")).toMatch(/Haiku 4.5 不支持自动模式/);
    expect(noAutoNote("claude", CLAUDE.models, "claude-opus-5-5")).toBe("");
  });
});

describe("menu components", () => {
  it("shows the rows with numbers, the current mark, and disables them while switching", async () => {
    const w = mount(ModeMenuBody, { props: { rows: modeRows(CLAUDE.modes), current: "auto", runtimeLabel: "Claude Code", notes: ["立即生效"] } });
    expect(w.text()).toContain("模式 · Claude Code");
    expect(w.findAll("[data-mode]").map((b) => b.attributes("data-mode"))).toEqual(["auto", "default", "acceptEdits", "plan", "bypassPermissions"]);
    expect(w.find('[data-mode="auto"]').attributes("aria-checked")).toBe("true");
    expect(w.find('[data-mode="plan"]').text()).toContain("4");
    await w.find('[data-mode="plan"]').trigger("click");
    expect(w.emitted("pick")).toEqual([["plan"]]);
    await w.setProps({ pending: "plan" });
    expect(w.find('[data-mode="plan"]').attributes("disabled")).toBeDefined();
    expect(w.find('[data-mode="auto"]').attributes("aria-checked")).toBe("false");
    expect(w.text()).toContain("等节点确认");
    // a model or effort switch waiting: every row disabled, the current one still marked
    const off = mount(ModeMenuBody, { props: { rows: modeRows(CLAUDE.modes), current: "auto", runtimeLabel: "x", notes: [], disabled: true } });
    expect(off.findAll("[data-mode]").every((b) => b.attributes("disabled") !== undefined)).toBe(true);
    expect(off.find('[data-testid="mode-current"]').exists()).toBe(true);
    // aria-checked follows the check: still on the current row while a model/effort switch waits
    expect(off.find('[data-mode="auto"]').attributes("aria-checked")).toBe("true");
    await off.find('[data-mode="plan"]').trigger("click");
    expect(off.emitted("pick")).toBeUndefined();
    const touch = mount(ModeMenuBody, { props: { rows: modeRows(CLAUDE.modes), current: "auto", runtimeLabel: "x", notes: [], touch: true } });
    expect(touch.find('[data-mode="plan"]').text()).not.toContain("4");
  });
  it("lists the models with their efforts and the current model's effort levels", async () => {
    const w = mount(ModelMenuBody, { props: { models: CLAUDE.models!, model: CLAUDE.model, effort: "high", efforts: CLAUDE.efforts!, runtime: "claude" } });
    expect(w.find('[data-model="claude-haiku-4-5"]').text()).toContain("不支持自动模式");
    expect(w.findAll("[data-effort]").map((b) => b.text())).toEqual(["低", "中", "高", "超高", "最高"]);
    await w.find('[data-effort="low"]').trigger("click");
    expect(w.emitted("effort")).toEqual([["low"]]);
    await w.find('[data-model="claude-haiku-4-5"]').trigger("click");
    expect(w.emitted("model")).toEqual([["claude-haiku-4-5"]]);
    const agy = mount(ModelMenuBody, { props: { models: AGY.models!, model: AGY.model, effort: "", efforts: [], runtime: "antigravity" } });
    expect(agy.text()).toContain("强度是模型的一部分");
  });
});

describe("session controls bar", () => {
  afterEach(() => {
    vi.restoreAllMocks();
    document.body.innerHTML = "";
  });
  function mountBar(data: SessionControls, put: (body: unknown) => SessionControls) {
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false, staleTime: Infinity } } });
    qc.setQueryData(keys.sessionControls("p1"), data);
    const fetch = vi.fn(async (req: Request) => {
      const body = req.method === "PUT" ? put(await req.json()) : data;
      return new Response(JSON.stringify(body), { status: 200, headers: { "Content-Type": "application/json" } });
    });
    vi.stubGlobal("fetch", fetch);
    const w = mount(SessionControlsBar, { props: { pid: "p1" }, global: { plugins: [[VueQueryPlugin, { queryClient: qc }]] as never[] }, attachTo: document.body });
    return { w, qc, fetch };
  }
  it("shows the mode and model, sends a pick and shows 切换中… until the server writes back", async () => {
    const pendingView = { ...CLAUDE, pending: { permission_mode: "plan", since: "2026-11-24T10:00:00Z" } } as SessionControls;
    const { w, qc, fetch } = mountBar(CLAUDE, () => pendingView);
    expect(w.find('[data-testid="mode-control"]').text()).toContain("自动审批");
    expect(w.find('[data-testid="model-control"]').text()).toContain("Opus 5.5 · 高");
    // the popover's content is rendered into the body
    await w.find('[data-testid="mode-control"]').trigger("click");
    await flushPromises();
    const plan = document.querySelector('[data-mode="plan"]') as HTMLButtonElement | null;
    expect(plan).not.toBeNull();
    plan!.click();
    await flushPromises();
    const put = fetch.mock.calls.find(([r]) => (r as Request).method === "PUT");
    expect(put).toBeDefined();
    expect(w.find('[data-testid="mode-control"]').text()).toContain("切换到计划…");
    qc.setQueryData(keys.sessionControls("p1"), { ...CLAUDE, permission_mode: "plan" });
    await flushPromises();
    expect(w.find('[data-testid="mode-control"]').text()).toContain("计划模式");
  });
  it("picks a mode with its digit while the menu is open", async () => {
    const { w, fetch } = mountBar(CODEX, (b) => ({ ...CODEX, permission_mode: (b as { permission_mode: string }).permission_mode }));
    expect(w.find('[data-testid="mode-control"]').text()).toContain("跳过权限确认");
    window.dispatchEvent(new KeyboardEvent("keydown", { key: "2" }));
    await flushPromises();
    expect(fetch.mock.calls.some(([r]) => (r as Request).method === "PUT")).toBe(false);
    await w.find('[data-testid="mode-control"]').trigger("click");
    await new Promise((r) => setTimeout(r, 50)); // the popover opens on a timer
    await flushPromises();
    window.dispatchEvent(new KeyboardEvent("keydown", { key: "2" }));
    await flushPromises();
    const req = fetch.mock.calls.map(([r]) => r as Request).find((r) => r.method === "PUT")!;
    expect(req).toBeDefined();
    expect(w.find('[data-testid="mode-control"]').text()).toContain("修改前询问");
  });
});

describe("session controls bar: how a switch ends", () => {
  afterEach(() => {
    vi.restoreAllMocks();
    document.body.innerHTML = "";
  });
  const since = "2026-11-24T10:00:00Z";
  function mountBar(data: SessionControls, put: (body: unknown) => SessionControls = () => data) {
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false, staleTime: Infinity } } });
    qc.setQueryData(keys.sessionControls("p1"), data);
    const fetch = vi.fn(async (req: Request) => {
      const body = req.method === "PUT" ? put(await req.json()) : qc.getQueryData(keys.sessionControls("p1"));
      return new Response(JSON.stringify(body), { status: 200, headers: { "Content-Type": "application/json" } });
    });
    vi.stubGlobal("fetch", fetch);
    const w = mount(SessionControlsBar, { props: { pid: "p1" }, global: { plugins: [[VueQueryPlugin, { queryClient: qc }]] as never[] }, attachTo: document.body });
    return { w, qc, fetch };
  }
  const open = async (w: ReturnType<typeof mount>, id: string) => {
    await w.find(`[data-testid="${id}"]`).trigger("click");
    await new Promise((r) => setTimeout(r, 50)); // the popover opens on a timer
    await flushPromises();
  };

  it("keeps an old failure out of a toast on load and shows it in the menu", async () => {
    const err = vi.spyOn(toast, "error").mockImplementation(() => undefined as never);
    const { w } = mountBar({ ...CLAUDE, error: "节点拒绝了这次切换" });
    await flushPromises();
    expect(err).not.toHaveBeenCalled();
    await open(w, "mode-control");
    expect(document.body.textContent).toContain("上次切换没有成功：节点拒绝了这次切换");
  });

  it("says when a switch failed, and when it was dropped without an answer", async () => {
    const err = vi.spyOn(toast, "error").mockImplementation(() => undefined as never);
    // loaded while a switch waits; the node answers with an error
    const { qc } = mountBar({ ...CLAUDE, pending: { permission_mode: "plan", since } } as SessionControls);
    await flushPromises();
    qc.setQueryData(keys.sessionControls("p1"), { ...CLAUDE, error: "节点拒绝了这次切换" });
    await flushPromises();
    expect(err).toHaveBeenCalledWith("切换没有成功：节点拒绝了这次切换");
    err.mockClear();
    // the next one times out: pending is dropped and the old values stay
    qc.setQueryData(keys.sessionControls("p1"), { ...CLAUDE, pending: { effort: "max", since } });
    await flushPromises();
    qc.setQueryData(keys.sessionControls("p1"), { ...CLAUDE });
    await flushPromises();
    expect(err).toHaveBeenCalledWith("节点没有回应，切换未生效");
    err.mockClear();
    // one that went through says nothing
    qc.setQueryData(keys.sessionControls("p1"), { ...CLAUDE, pending: { permission_mode: "plan", since } });
    await flushPromises();
    qc.setQueryData(keys.sessionControls("p1"), { ...CLAUDE, permission_mode: "plan" });
    await flushPromises();
    expect(err).not.toHaveBeenCalled();
  });

  it("disables the mode rows while a model or effort switch waits", async () => {
    const { w } = mountBar({ ...CLAUDE, pending: { model: "claude-haiku-4-5", since } } as SessionControls);
    await open(w, "mode-control");
    const rows = [...document.querySelectorAll<HTMLButtonElement>("[data-testid=mode-menu] [data-mode], [data-mode]")].filter((b) => b.tagName === "BUTTON" && b.closest("[data-testid=session-controls]") === null);
    expect(rows.length).toBeGreaterThan(0);
    expect(rows.every((b) => b.disabled)).toBe(true);
  });

  it("sends only the model when no effort is picked, and keeps a picked effort the new model has", async () => {
    const two = { ...CODEX, effort: "", models: [...CODEX.models!, { id: "gpt-5.5-mini", name: "GPT-5.5 mini", efforts: ["low", "medium"], default_effort: "low", supports_auto: false }] } as SessionControls;
    const bodies: unknown[] = [];
    const { w, qc } = mountBar(two, (b) => (bodies.push(b), { ...two, model: (b as { model: string }).model }));
    await open(w, "model-control");
    (document.querySelector('[data-model="gpt-5.5-mini"]') as HTMLButtonElement).click();
    await flushPromises();
    expect(bodies).toEqual([{ model: "gpt-5.5-mini" }]);
    // with an effort picked that the new model lacks: its default goes with it
    qc.setQueryData(keys.sessionControls("p1"), { ...two, effort: "high" });
    await flushPromises();
    await open(w, "model-control");
    (document.querySelector('[data-model="gpt-5.5-mini"]') as HTMLButtonElement).click();
    await flushPromises();
    expect(bodies[1]).toEqual({ model: "gpt-5.5-mini", effort: "low" });
  });
});
