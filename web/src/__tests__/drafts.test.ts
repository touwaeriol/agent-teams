// @vitest-environment jsdom
// Unsent drafts of the message boxes: kept per viewer, project and conversation across remounts
// (switching threads remounts the board), reloads and key changes; cleared after a send; expired
// and old-format entries swept. Attachments and @ references survive remounts in memory.
import { QueryClient, VueQueryPlugin } from "@tanstack/vue-query";
import { flushPromises, mount, type VueWrapper } from "@vue/test-utils";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { defineComponent, h, nextTick, ref, type Component } from "vue";
import { createMemoryHistory, createRouter } from "vue-router";
import type { CoordinatorView } from "@/api/views";
import { useAttachments } from "@/components/app/attachments";
import Composer from "@/components/app/Composer.vue";
import { useComposerAssist } from "@/components/app/composer/useComposerAssist";
import LeadComposer from "@/components/app/thread/LeadComposer.vue";
import { DRAFT_TTL_MS, draftKey, flushDrafts, resetDraftMemory, sweepDrafts, useDraft } from "@/lib/persist";
import CoordinatorComposer from "@/pages/main/CoordinatorComposer.vue";
import { thread } from "./fixtures";

const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });

/** /api/me answers as `user`; sending a message answers `send`; the rest {}. */
function stubApi(user = "u1", send: () => Response = () => json({ id: "m1" })) {
  return vi.spyOn(globalThis, "fetch").mockImplementation(async (input) => {
    const req = input instanceof Request ? input : new Request(String(input));
    const path = new URL(req.url, "http://x").pathname;
    if (path === "/api/me") return json({ id: user, email: `${user}@x`, name: user });
    if (req.method === "POST" && path.endsWith("/messages")) return send();
    if (path.endsWith("/messages")) return json({ items: [], next_cursor: null });
    return json({});
  });
}

let client: QueryClient;
function plugins() {
  const router = createRouter({ history: createMemoryHistory(), routes: [{ path: "/:p(.*)*", component: { render: () => null } }] });
  return [[VueQueryPlugin, { queryClient: client }], router] as never[];
}
const mountWith = (c: Component, props: Record<string, unknown> = {}) => mount(c, { props, global: { plugins: plugins() }, attachTo: document.body });

const coordinator = (cid: string) => ({ conversation_id: cid, employee: { name: "leader" }, session_state: "active" }) as unknown as CoordinatorView;
const stored = (k: string) => {
  const raw = window.localStorage.getItem(k);
  return raw === null ? null : (JSON.parse(raw) as { t: string; at: number }).t;
};
const boxOf = (w: VueWrapper) => w.get<HTMLTextAreaElement | HTMLInputElement>("textarea, input:not([type=file])");
/** A fresh page: memory gone, only localStorage left. */
const reload = () => {
  flushDrafts();
  resetDraftMemory();
};

beforeEach(() => {
  window.localStorage.clear();
  resetDraftMemory();
  client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
});
afterEach(() => {
  vi.useRealTimers();
  vi.restoreAllMocks();
  document.body.innerHTML = "";
});

describe("draftKey", () => {
  it("names viewer, project and conversation, and waits for all three", () => {
    expect(draftKey("u1", "p1", "c0")).toBe("draft:v2:u1:p1:c0");
    expect(draftKey(undefined, "p1", "c0")).toBeUndefined();
    expect(draftKey("u1", "p1", "")).toBeUndefined();
  });
});

describe("coordinator draft across thread switches", () => {
  it("is back after a remount right after typing (no debounce wait), and in storage", async () => {
    stubApi();
    const w = mountWith(CoordinatorComposer, { pid: "p1", coordinator: coordinator("c0") });
    await flushPromises();
    await boxOf(w).setValue("还没发出去的话");
    w.unmount(); // opening a thread remounts the board at once
    expect(stored("draft:v2:u1:p1:c0")).toBe("还没发出去的话");
    const again = mountWith(CoordinatorComposer, { pid: "p1", coordinator: coordinator("c0") });
    await flushPromises();
    expect(boxOf(again).element.value).toBe("还没发出去的话");
  });

  it("comes back after a reload (only localStorage left)", async () => {
    stubApi();
    const w = mountWith(CoordinatorComposer, { pid: "p1", coordinator: coordinator("c0") });
    await flushPromises();
    await boxOf(w).setValue("刷新也在");
    window.dispatchEvent(new Event("pagehide")); // the page closes before the debounce
    expect(stored("draft:v2:u1:p1:c0")).toBe("刷新也在");
    w.unmount();
    resetDraftMemory();
    client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const again = mountWith(CoordinatorComposer, { pid: "p1", coordinator: coordinator("c0") });
    await flushPromises();
    expect(boxOf(again).element.value).toBe("刷新也在");
  });

  it("keeps what was typed before the conversation id arrived", async () => {
    stubApi();
    const w = mountWith(CoordinatorComposer, { pid: "p1", coordinator: undefined });
    await flushPromises();
    await boxOf(w).setValue("先打的字");
    await w.setProps({ coordinator: coordinator("c0") });
    await flushPromises();
    expect(boxOf(w).element.value).toBe("先打的字");
    w.unmount();
    expect(stored("draft:v2:u1:p1:c0")).toBe("先打的字");
  });

  it("clears the draft and its storage after a successful send", async () => {
    stubApi();
    const w = mountWith(CoordinatorComposer, { pid: "p1", coordinator: coordinator("c0") });
    await flushPromises();
    await boxOf(w).setValue("发出去");
    flushDrafts();
    expect(stored("draft:v2:u1:p1:c0")).toBe("发出去");
    await boxOf(w).trigger("keydown", { key: "Enter" });
    await flushPromises();
    expect(boxOf(w).element.value).toBe("");
    expect(window.localStorage.getItem("draft:v2:u1:p1:c0")).toBeNull();
    w.unmount();
    const again = mountWith(CoordinatorComposer, { pid: "p1", coordinator: coordinator("c0") });
    await flushPromises();
    expect(boxOf(again).element.value).toBe("");
  });

  it("keeps the text (box and storage) when the send fails", async () => {
    stubApi("u1", () => json({ error: "boom" }, 500));
    const w = mountWith(CoordinatorComposer, { pid: "p1", coordinator: coordinator("c0") });
    await flushPromises();
    await boxOf(w).setValue("没发出去");
    await boxOf(w).trigger("keydown", { key: "Enter" });
    await flushPromises();
    expect(boxOf(w).element.value).toBe("没发出去");
    w.unmount();
    expect(stored("draft:v2:u1:p1:c0")).toBe("没发出去");
  });
});

describe("drafts stay apart", () => {
  it("coordinator and two threads each keep their own", async () => {
    stubApi();
    const boss = mountWith(CoordinatorComposer, { pid: "p1", coordinator: coordinator("c0") });
    const a = mountWith(LeadComposer, { pid: "p1", thread: thread(1, { id: "tA" }) });
    await flushPromises();
    await boxOf(boss).setValue("统筹");
    await boxOf(a).setValue("讨论串 A");
    a.unmount();
    const b = mountWith(LeadComposer, { pid: "p1", thread: thread(2, { id: "tB" }) });
    await flushPromises();
    expect(boxOf(b).element.value).toBe("");
    await boxOf(b).setValue("讨论串 B");
    b.unmount();
    boss.unmount();
    reload();
    expect(stored("draft:v2:u1:p1:c0")).toBe("统筹");
    expect(stored("draft:v2:u1:p1:tA")).toBe("讨论串 A");
    expect(stored("draft:v2:u1:p1:tB")).toBe("讨论串 B");
    const a2 = mountWith(LeadComposer, { pid: "p1", thread: thread(1, { id: "tA" }) });
    await flushPromises();
    expect(boxOf(a2).element.value).toBe("讨论串 A");
  });

  it("a box reused for another thread saves the first before showing the second", async () => {
    stubApi();
    const w = mountWith(LeadComposer, { pid: "p1", thread: thread(1, { id: "tA" }) });
    await flushPromises();
    await boxOf(w).setValue("A 的");
    await w.setProps({ thread: thread(2, { id: "tB" }) });
    expect(stored("draft:v2:u1:p1:tA")).toBe("A 的");
    expect(boxOf(w).element.value).toBe("");
    await w.setProps({ thread: thread(1, { id: "tA" }) });
    expect(boxOf(w).element.value).toBe("A 的");
  });

  it("other projects and other viewers do not see each other's drafts", async () => {
    vi.useFakeTimers();
    const key = ref<string | undefined>(draftKey("u1", "p1", "c0"));
    let draft!: ReturnType<typeof useDraft>;
    const Host = defineComponent({ setup: () => ((draft = useDraft(key)), () => h("div")) });
    mount(Host);
    draft.value = "u1 p1";
    key.value = draftKey("u1", "p2", "c0");
    await nextTick();
    expect(draft.value).toBe("");
    draft.value = "u1 p2";
    key.value = draftKey("u2", "p1", "c0");
    await nextTick();
    expect(draft.value).toBe("");
    vi.advanceTimersByTime(300);
    expect(stored("draft:v2:u1:p1:c0")).toBe("u1 p1");
    expect(stored("draft:v2:u1:p2:c0")).toBe("u1 p2");
    expect(window.localStorage.getItem("draft:v2:u2:p1:c0")).toBeNull();
  });
});

describe("useDraft storage", () => {
  const host = (key: () => string | undefined) => {
    let draft!: ReturnType<typeof useDraft>;
    const w = mount(defineComponent({ setup: () => ((draft = useDraft(key)), () => h("div")) }));
    return { w, draft: () => draft };
  };

  it("debounces writes, and two boxes on one key show the same text", () => {
    vi.useFakeTimers();
    const one = host(() => "draft:v2:u1:p1:c1");
    const two = host(() => "draft:v2:u1:p1:c1");
    one.draft().value = "typed";
    expect(window.localStorage.getItem("draft:v2:u1:p1:c1")).toBeNull();
    expect(two.draft().value).toBe("typed");
    vi.advanceTimersByTime(300);
    expect(stored("draft:v2:u1:p1:c1")).toBe("typed");
    one.draft().value = "";
    expect(window.localStorage.getItem("draft:v2:u1:p1:c1")).toBeNull();
  });

  it("does not restore an expired draft and removes it", () => {
    const old = Date.now() - DRAFT_TTL_MS - 1000;
    window.localStorage.setItem("draft:v2:u1:p1:c1", JSON.stringify({ t: "很久以前", at: old }));
    window.localStorage.setItem("draft:v2:u1:p1:c2", JSON.stringify({ t: "过期的另一条", at: old }));
    window.localStorage.setItem("draft:v2:u1:p1:c3", JSON.stringify({ t: "新的", at: Date.now() }));
    window.localStorage.setItem("draft:v2:u1:p1:c4", "not json");
    expect(host(() => "draft:v2:u1:p1:c1").draft().value).toBe("");
    expect(window.localStorage.getItem("draft:v2:u1:p1:c1")).toBeNull();
    // the first read swept the rest too
    expect(window.localStorage.getItem("draft:v2:u1:p1:c2")).toBeNull();
    expect(window.localStorage.getItem("draft:v2:u1:p1:c4")).toBeNull();
    expect(host(() => "draft:v2:u1:p1:c3").draft().value).toBe("新的");
  });

  it("adopts an old-format draft once, and sweeps the unclaimed ones after the TTL", () => {
    window.localStorage.setItem("draft:c1", "旧格式");
    window.localStorage.setItem("draft:t9", "没人认领");
    expect(host(() => draftKey("u1", "p1", "c1")).draft().value).toBe("旧格式");
    expect(window.localStorage.getItem("draft:c1")).toBeNull();
    expect(stored("draft:v2:u1:p1:c1")).toBe("旧格式");
    const seen = Number(window.localStorage.getItem("draft-legacy-seen"));
    expect(seen).toBeGreaterThan(0);
    sweepDrafts(seen + 1000);
    expect(window.localStorage.getItem("draft:t9")).toBe("没人认领");
    sweepDrafts(seen + DRAFT_TTL_MS + 1000);
    expect(window.localStorage.getItem("draft:t9")).toBeNull();
    expect(window.localStorage.getItem("draft-legacy-seen")).toBeNull();
  });

  it("merges text typed before the key with a saved draft instead of dropping either", async () => {
    window.localStorage.setItem("draft:v2:u1:p1:c1", JSON.stringify({ t: "存着的", at: Date.now() }));
    const key = ref<string | undefined>();
    const h1 = host(() => key.value);
    h1.draft().value = "新打的";
    key.value = "draft:v2:u1:p1:c1";
    await nextTick();
    expect(h1.draft().value).toBe("存着的\n新打的");
  });

  it("works without localStorage (reads and writes throw)", () => {
    vi.useFakeTimers();
    vi.spyOn(Storage.prototype, "getItem").mockImplementation(() => {
      throw new Error("blocked");
    });
    vi.spyOn(Storage.prototype, "setItem").mockImplementation(() => {
      throw new Error("blocked");
    });
    vi.spyOn(Storage.prototype, "key").mockImplementation(() => {
      throw new Error("blocked");
    });
    const one = host(() => "draft:v2:u1:p1:c1");
    one.draft().value = "still typing";
    vi.advanceTimersByTime(300);
    expect(one.draft().value).toBe("still typing");
    one.w.unmount();
    expect(host(() => "draft:v2:u1:p1:c1").draft().value).toBe("still typing");
  });
});

describe("attachments and @ references across remounts", () => {
  it("keyed attachments come back, finish uploading while unmounted, and keep previews until sent", async () => {
    URL.createObjectURL = vi.fn(() => "blob:keep");
    URL.revokeObjectURL = vi.fn();
    let resolve!: (v: { id: string }) => void;
    const uploader = vi.fn(() => new Promise<{ id: string }>((r) => (resolve = r)));
    let a!: ReturnType<typeof useAttachments>;
    const Host = defineComponent({ setup: () => ((a = useAttachments("p1", uploader, "draft:v2:u1:p1:c0")), () => h("div")) });
    const w = mountWith(Host);
    a.add([new File(["x"], "a.png", { type: "image/png" })]);
    expect(a.ids()).toBeNull();
    w.unmount();
    expect(URL.revokeObjectURL).not.toHaveBeenCalled();
    resolve({ id: "img-1" });
    await flushPromises();
    mountWith(Host);
    expect(a.items.value.map((x) => [x.preview, x.id])).toEqual([["blob:keep", "img-1"]]);
    expect(a.ids()).toEqual(["img-1"]);
    a.clear();
    expect(URL.revokeObjectURL).toHaveBeenCalledWith("blob:keep");
    expect(a.items.value).toEqual([]);
  });

  it("attachments added before the key arrive move into its list", async () => {
    URL.createObjectURL = vi.fn(() => "blob:early");
    URL.revokeObjectURL = vi.fn();
    const key = ref<string | undefined>();
    let a!: ReturnType<typeof useAttachments>;
    const Host = defineComponent({ setup: () => ((a = useAttachments("p1", async () => ({ id: "i1" }), key)), () => h("div")) });
    const w = mountWith(Host);
    a.add([new File(["x"], "a.png", { type: "image/png" })]);
    await flushPromises();
    key.value = "draft:v2:u1:p1:early";
    await nextTick();
    w.unmount();
    expect(URL.revokeObjectURL).not.toHaveBeenCalled();
    mountWith(Host);
    expect(a.ids()).toEqual(["i1"]);
  });

  it("unkeyed attachments still go with their box", () => {
    URL.createObjectURL = vi.fn(() => "blob:own");
    URL.revokeObjectURL = vi.fn();
    let a!: ReturnType<typeof useAttachments>;
    const w = mountWith(defineComponent({ setup: () => ((a = useAttachments("p1", async () => ({ id: "i" }))), () => h("div")) }));
    a.add([new File(["x"], "a.png", { type: "image/png" })]);
    w.unmount();
    expect(URL.revokeObjectURL).toHaveBeenCalledWith("blob:own");
  });

  it("@ references of a conversation come back after a remount", () => {
    let assist!: ReturnType<typeof useComposerAssist>;
    const text = ref("看 @docs/a.md");
    const opts = { pid: "p1", cid: "c-ref", scope: "coordinator" as const };
    const Host = defineComponent({ setup: () => ((assist = useComposerAssist(opts, text, () => null)), () => h("div")) });
    const w = mountWith(Host);
    assist.picked.value = [{ kind: "file", path: "docs/a.md", label: "a.md" } as never];
    w.unmount();
    mountWith(Host);
    expect(assist.picked.value).toHaveLength(1);
    const other = { ...opts, cid: "c-other" };
    mountWith(defineComponent({ setup: () => ((assist = useComposerAssist(other, text, () => null)), () => h("div")) }));
    expect(assist.picked.value).toHaveLength(0);
  });
});

describe("Composer with a draft key", () => {
  it("restores on remount without waiting for the debounce", async () => {
    const props = { label: "x", placeholder: "x", onSend: vi.fn(), draftKey: "draft:v2:u1:p1:cx" };
    const w = mountWith(Composer, props);
    await boxOf(w).setValue("立刻切走");
    w.unmount();
    const again = mountWith(Composer, props);
    expect(boxOf(again).element.value).toBe("立刻切走");
  });
});
