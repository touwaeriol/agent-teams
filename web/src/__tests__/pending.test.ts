// @vitest-environment jsdom
// Optimistic sends (api/pending.ts): the user's message shows in the chat at once, marked 发送中;
// the POST's reply, the SSE frame and a refetch each leave exactly one copy (whichever comes
// first); a failed send stays red with 重试 (same Idempotency-Key) and 删除; the box gets the text
// back on failure and loses it again when a retry from the chat succeeds.
import { QueryClient, VueQueryPlugin } from "@tanstack/vue-query";
import { flushPromises, mount, type VueWrapper } from "@vue/test-utils";
import ElementPlus from "element-plus";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { defineComponent, h, type Ref } from "vue";
import { createMemoryHistory, createRouter } from "vue-router";
import { applyProjectEvent } from "@/api/events";
import { confirms, pendingOf, prunePending, resetPending, retryMessage, sendMessage, withPending, type PendingMessage } from "@/api/pending";
import { flattenMessages, keys, type MessagePages } from "@/api/queries";
import type { CoordinatorView, MessageView } from "@/api/views";
import Chat from "@/components/app/Chat.vue";
import type { MessageRef } from "@/components/app/composer/assist";
import { dropPickedRefs, useComposerAssist } from "@/components/app/composer/useComposerAssist";
import LeadComposer from "@/components/app/thread/LeadComposer.vue";
import { resetDraftMemory } from "@/lib/persist";
import CoordinatorComposer from "@/pages/main/CoordinatorComposer.vue";
import { thread } from "./fixtures";

const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
const msg = (id: string, content: string, patch: Partial<MessageView> = {}): MessageView => ({
  id, role: "user", author: "u1", kind: "chat", content, meta: {}, created_at: "2026-11-19T00:00:00Z", ...patch,
});
const pend = (key: string, content: string, patch: Partial<PendingMessage> = {}): PendingMessage => ({
  key, cid: "c1", content, images: [], refs: [], attachments: [], status: "sending", at: "2026-11-19T00:00:01Z", seen: ["10"], seenMax: 10, ...patch,
});

interface Post {
  key: string | null;
  content: string;
  respond: (r: Response) => void;
}

/** Messages GET answers `server` (a message a POST answered with is added to it); each POST waits until answered by hand (posts[i].respond). */
function stubApi(server: MessageView[] = []) {
  const posts: Post[] = [];
  let gets = 0;
  let uploads = 0;
  vi.spyOn(globalThis, "fetch").mockImplementation(async (input) => {
    const req = input instanceof Request ? input : new Request(String(input));
    const path = new URL(req.url, "http://x").pathname;
    if (path === "/api/me") return json({ id: "u1", email: "u1@x", name: "u1" });
    // No object storage in these tests: the direct upload answers 503 and the box falls back to the base64 upload.
    if (req.method === "POST" && path.endsWith("/uploads")) return json({ detail: "no storage", code: "storage_unavailable" }, 503);
    if (req.method === "POST" && path.endsWith("/attachments")) return json({ id: `att${++uploads}` });
    if (req.method === "POST" && path.endsWith("/messages")) {
      const body = (await req.clone().json()) as { content: string };
      return new Promise<Response>((resolve) =>
        posts.push({
          key: req.headers.get("Idempotency-Key"),
          content: body.content,
          respond: (r) => {
            if (r.ok) void r.clone().json().then((m: MessageView) => server.push(m));
            resolve(r);
          },
        }),
      );
    }
    if (path.endsWith("/messages")) {
      gets++;
      return json({ items: server, next_cursor: null });
    }
    return json({});
  });
  return { posts, gets: () => gets };
}

let client: QueryClient;
const plugins = () => {
  const router = createRouter({ history: createMemoryHistory(), routes: [{ path: "/:p(.*)*", component: { render: () => null } }] });
  return [[VueQueryPlugin, { queryClient: client }], router, ElementPlus] as never[];
};
const seed = (cid: string, items: MessageView[]) => client.setQueryData<MessagePages>(keys.messages(cid), { pages: [{ items, next_cursor: null }], pageParams: [undefined] });
const cached = (cid: string) => flattenMessages(client.getQueryData<MessagePages>(keys.messages(cid)));
const shown = (cid: string) => withPending(cached(cid), pendingOf(cid));

beforeEach(() => {
  URL.createObjectURL = vi.fn((f: Blob) => `blob:${(f as File).name}`);
  URL.revokeObjectURL = vi.fn();
  window.localStorage.clear();
  resetDraftMemory();
  resetPending();
  client = new QueryClient({ defaultOptions: { queries: { retry: false, staleTime: Infinity } } });
});
afterEach(() => {
  vi.restoreAllMocks();
  document.body.innerHTML = "";
});

describe("withPending", () => {
  it("shows unconfirmed sends after the server's messages, marked as local", () => {
    const out = withPending([msg("10", "旧的")], [pend("k1", "新的")]);
    expect(out.map((m) => m.content)).toEqual(["旧的", "新的"]);
    expect(out[1]).toMatchObject({ id: "tmp-k1", role: "user", local: { key: "k1", status: "sending" } });
  });

  it("a newer user message with the same text confirms it; one message confirms one send", () => {
    const sends = [pend("k1", "好"), pend("k2", "好")];
    expect(withPending([msg("10", "好"), msg("11", "好")], sends).map((m) => m.id)).toEqual(["10", "11", "tmp-k2"]);
    expect(withPending([msg("10", "好"), msg("11", "好"), msg("12", "好")], sends).map((m) => m.id)).toEqual(["10", "11", "12"]);
  });

  it("is not confirmed by a message the chat already had, an older one, another text or the leader", () => {
    const p = pend("k1", "好");
    expect(confirms(msg("10", "好"), p)).toBe(false); // seen when it was sent
    expect(confirms(msg("9", "好"), p)).toBe(false); // an older page loaded later
    expect(confirms(msg("11", "好的"), p)).toBe(false);
    expect(confirms(msg("11", "好", { role: "leader" }), p)).toBe(false);
    expect(confirms(msg("11", "好", { meta: { attachments: [{ id: "a", name: "a", content_type: "image/png", size: 1 }] } }), p)).toBe(false);
    expect(confirms(msg("11", "好"), p)).toBe(true);
  });
});

describe("sendMessage", () => {
  it("shows the message at once; the POST's reply replaces it and the SSE frame after it adds nothing", async () => {
    const api = stubApi();
    seed("c1", [msg("10", "旧的")]);
    const done = sendMessage(client, { cid: "c1", content: "新的" });
    expect(shown("c1").map((m) => [m.content, m.local?.status])).toEqual([["旧的", undefined], ["新的", "sending"]]);
    await flushPromises();
    api.posts[0].respond(json(msg("11", "新的"), 202));
    await done;
    expect(pendingOf("c1")).toEqual([]);
    expect(shown("c1").map((m) => m.id)).toEqual(["10", "11"]);
    applyProjectEvent(client, "p1", "message", { conversation_id: "c1", message: msg("11", "新的") });
    expect(shown("c1").map((m) => m.id)).toEqual(["10", "11"]);
  });

  it("the SSE frame before the POST's reply: still one message", async () => {
    const api = stubApi();
    seed("c1", [msg("10", "旧的")]);
    const done = sendMessage(client, { cid: "c1", content: "新的" });
    await flushPromises();
    applyProjectEvent(client, "p1", "message", { conversation_id: "c1", message: msg("11", "新的") });
    expect(shown("c1").map((m) => m.id)).toEqual(["10", "11"]);
    api.posts[0].respond(json(msg("11", "新的"), 202));
    await done;
    expect(shown("c1").map((m) => m.id)).toEqual(["10", "11"]);
  });

  it("sends to one conversation go out in order, each with its own key", async () => {
    const api = stubApi();
    seed("c1", []);
    const a = sendMessage(client, { cid: "c1", content: "一" });
    const b = sendMessage(client, { cid: "c1", content: "二" });
    await flushPromises();
    expect(api.posts.map((p) => p.content)).toEqual(["一"]);
    expect(shown("c1").map((m) => m.content)).toEqual(["一", "二"]);
    api.posts[0].respond(json(msg("1", "一"), 202));
    await a;
    await flushPromises();
    expect(api.posts.map((p) => p.content)).toEqual(["一", "二"]);
    expect(api.posts[0].key).not.toBe(api.posts[1].key);
    api.posts[1].respond(json(msg("2", "二"), 202));
    await b;
    expect(shown("c1").map((m) => m.id)).toEqual(["1", "2"]);
  });

  it("a failure stays, marked failed; a retry reuses the Idempotency-Key and replaces it", async () => {
    const api = stubApi();
    seed("c1", []);
    const done = sendMessage(client, { cid: "c1", content: "再来" });
    await flushPromises();
    api.posts[0].respond(json({ detail: "down", code: "unavailable" }, 503));
    await expect(done).rejects.toThrow();
    expect(shown("c1")).toMatchObject([{ content: "再来", local: { status: "failed" } }]);
    const key = pendingOf("c1")[0].key;
    const again = retryMessage(client, key);
    expect(shown("c1")).toMatchObject([{ local: { status: "sending" } }]);
    await flushPromises();
    expect(api.posts[1].key).toBe(api.posts[0].key);
    api.posts[1].respond(json(msg("5", "再来"), 202));
    await again;
    expect(shown("c1").map((m) => [m.id, m.local])).toEqual([["5", undefined]]);
  });

  it("a refetch that does not have the message yet keeps it shown", async () => {
    const api = stubApi([msg("10", "旧的")]);
    seed("c1", [msg("10", "旧的")]);
    void sendMessage(client, { cid: "c1", content: "新的" }).catch(() => undefined);
    await client.refetchQueries({ queryKey: keys.messages("c1") }).catch(() => undefined);
    expect(shown("c1").map((m) => m.content)).toEqual(["旧的", "新的"]);
    expect(api.posts).toHaveLength(1);
  });
});

describe("confirming", () => {
  it("the same text twice: the first one's reply does not confirm the second", async () => {
    const api = stubApi();
    seed("c1", []);
    const a = sendMessage(client, { cid: "c1", content: "好" });
    void sendMessage(client, { cid: "c1", content: "好" }).catch(() => undefined);
    await flushPromises();
    api.posts[0].respond(json(msg("11", "好"), 202));
    await a;
    expect(shown("c1").map((m) => [m.id, m.local?.status])).toEqual([["11", undefined], [expect.stringMatching(/^tmp-/), "sending"]]);
  });

  it("before the chat has loaded, an old message with the same text does not confirm a send", async () => {
    const api = stubApi();
    const done = sendMessage(client, { cid: "c1", content: "好" });
    expect(withPending([msg("5", "好")], pendingOf("c1")).map((m) => m.id)).toEqual(["5", expect.stringMatching(/^tmp-/)]);
    await flushPromises();
    api.posts[0].respond(json(msg("11", "好"), 202));
    await done;
    // The cache still has nothing: the stored message is drawn from the pending store meanwhile.
    expect(cached("c1")).toEqual([]);
    expect(shown("c1").map((m) => [m.id, m.local])).toEqual([["11", undefined]]);
    seed("c1", [msg("5", "好"), msg("11", "好")]);
    expect(shown("c1").map((m) => m.id)).toEqual(["5", "11"]);
    prunePending("c1", cached("c1"));
    expect(pendingOf("c1")).toEqual([]);
  });

  it("a failed send goes once the same text sent again from the box is delivered", async () => {
    const api = stubApi();
    seed("c1", []);
    await expect(
      (async () => {
        const p = sendMessage(client, { cid: "c1", content: "再说一遍" });
        await flushPromises();
        api.posts[0].respond(json({ detail: "down" }, 503));
        return p;
      })(),
    ).rejects.toThrow();
    const again = sendMessage(client, { cid: "c1", content: "再说一遍" });
    await flushPromises();
    api.posts[1].respond(json(msg("12", "再说一遍"), 202));
    await again;
    expect(pendingOf("c1")).toEqual([]);
    expect(shown("c1").map((m) => m.id)).toEqual(["12"]);
  });

  it("a failed send the server turns out to have (its frame came) goes after the next successful send", async () => {
    const api = stubApi();
    seed("c1", []);
    const lost = sendMessage(client, { cid: "c1", content: "其实到了" });
    await flushPromises();
    api.posts[0].respond(json({ detail: "timeout" }, 504));
    await expect(lost).rejects.toThrow();
    applyProjectEvent(client, "p1", "message", { conversation_id: "c1", message: msg("20", "其实到了") });
    expect(shown("c1").map((m) => m.id)).toEqual(["20"]);
    const next = sendMessage(client, { cid: "c1", content: "下一条" });
    await flushPromises();
    api.posts[1].respond(json(msg("21", "下一条"), 202));
    await next;
    expect(pendingOf("c1")).toEqual([]);
  });
});

describe("dropPickedRefs", () => {
  it("takes the message's refs out of the box unless the text left still names them", () => {
    let picked!: Ref<Array<MessageRef & { label: string }>>;
    const Host = defineComponent({
      setup() {
        picked = useComposerAssist({ pid: "p1", cid: "c1", scope: "lead" }, { value: "" } as Ref<string>, () => null).picked;
        return () => null;
      },
    });
    mount(Host, { global: { plugins: plugins() } });
    const a = { kind: "file" as const, repo: "o/r", path: "src/a.go", label: "src/a.go" };
    const b = { kind: "file" as const, repo: "o/r", path: "src/b.go", label: "src/b.go" };
    picked.value = [a, b];
    dropPickedRefs({ scope: "lead", pid: "p1", cid: "c1" }, [{ kind: "file", repo: "o/r", path: "src/a.go" }, { kind: "file", repo: "o/r", path: "src/b.go" }], "还在 @src/b.go");
    expect(picked.value.map((r) => r.path)).toEqual(["src/b.go"]);
  });
});

/** The chat and its box side by side, as the coordinator page and the thread panel show them. */
function mountChat(box: "lead" | "coordinator") {
  const Host = defineComponent({
    setup() {
      const cid = box === "lead" ? "t1" : "c0";
      const composer =
        box === "lead"
          ? () => h(LeadComposer, { pid: "p1", thread: thread(1) })
          : () => h(CoordinatorComposer, { pid: "p1", coordinator: { conversation_id: "c0", employee: { name: "leader" }, session_state: "active" } as unknown as CoordinatorView });
      return () => h("div", [h(Chat as never, { cid, pid: "p1" }), composer()]);
    },
  });
  return mount(Host, { global: { plugins: plugins() }, attachTo: document.body });
}
const bubbles = (w: VueWrapper) => w.findAll("div.whitespace-pre-wrap").map((n) => n.text());
const box = (w: VueWrapper) => w.get<HTMLTextAreaElement>("textarea");
const thumbs = (w: VueWrapper) => w.findAll("[aria-label=附件] img").map((i) => i.attributes("alt"));
const paste = (w: VueWrapper, name: string) =>
  w.get("form").element.dispatchEvent(Object.assign(new Event("paste", { bubbles: true }), { clipboardData: { files: [Object.assign(new File(["x"], name, { type: "image/png" }), { arrayBuffer: async () => new ArrayBuffer(1) })] } }));

describe.each(["coordinator", "lead"] as const)("the %s chat", (which) => {
  it("shows the message before the server answers, then once", async () => {
    const api = stubApi([msg("10", "旧的")]);
    const w = mountChat(which);
    await flushPromises();
    await box(w).setValue("马上看到");
    await box(w).trigger("keydown", { key: "Enter" });
    await flushPromises();
    expect(api.posts).toHaveLength(1);
    expect(bubbles(w)).toEqual(["旧的", "马上看到"]);
    expect(w.find("[data-pending=sending]").exists()).toBe(true);
    expect(w.text()).toContain("发送中");
    // The box is free for the next message while this one is on its way.
    expect(box(w).element.value).toBe("");
    api.posts[0].respond(json(msg("11", "马上看到"), 202));
    await flushPromises();
    expect(bubbles(w)).toEqual(["旧的", "马上看到"]);
    expect(w.find("[data-pending]").exists()).toBe(false);
  });

  it("a failed send is red with 重试 and the text back in the box; a retry delivers it and clears the box", async () => {
    const api = stubApi([]);
    const w = mountChat(which);
    await flushPromises();
    await box(w).setValue("会失败");
    await box(w).trigger("keydown", { key: "Enter" });
    await flushPromises();
    api.posts[0].respond(json({ detail: "down", code: "unavailable" }, 503));
    await flushPromises();
    expect(w.find("[data-pending=failed]").exists()).toBe(true);
    expect(w.get("[role=alert]").text()).toContain("发送失败");
    expect(box(w).element.value).toBe("会失败");
    await w.findAll("[role=alert] button").find((b) => b.text() === "重试")!.trigger("click");
    await flushPromises();
    expect(api.posts[1].key).toBe(api.posts[0].key);
    api.posts[1].respond(json(msg("7", "会失败"), 202));
    await flushPromises();
    expect(bubbles(w)).toEqual(["会失败"]);
    expect(w.find("[data-pending]").exists()).toBe(false);
    expect(box(w).element.value).toBe("");
  });

  it("删除 takes a failed message out of the chat", async () => {
    const api = stubApi([]);
    const w = mountChat(which);
    await flushPromises();
    await box(w).setValue("不要了");
    await box(w).trigger("keydown", { key: "Enter" });
    await flushPromises();
    api.posts[0].respond(json({ detail: "down" }, 503));
    await flushPromises();
    await w.findAll("[role=alert] button").find((b) => b.text() === "删除")!.trigger("click");
    await flushPromises();
    expect(bubbles(w)).toEqual([]);
    expect(api.posts).toHaveLength(1);
  });

  it("a retry from the chat that delivers an image-only message takes the image out of the box", async () => {
    const api = stubApi([]);
    const w = mountChat(which);
    await flushPromises();
    paste(w, "shot.png");
    await flushPromises();
    expect(thumbs(w)).toEqual(["shot.png"]);
    await box(w).trigger("keydown", { key: "Enter" });
    await flushPromises();
    expect(thumbs(w)).toEqual([]);
    api.posts[0].respond(json({ detail: "down" }, 503));
    await flushPromises();
    expect(thumbs(w)).toEqual(["shot.png"]);
    expect(w.find("[data-pending=failed]").exists()).toBe(true);
    await w.findAll("[role=alert] button").find((b) => b.text() === "重试")!.trigger("click");
    await flushPromises();
    api.posts[1].respond(json(msg("8", "", { meta: { attachments: [{ id: "att1", name: "shot.png", content_type: "image/png", size: 1 }] } }), 202));
    await flushPromises();
    expect(w.find("[data-pending]").exists()).toBe(false);
    expect(thumbs(w)).toEqual([]);
    expect(URL.revokeObjectURL).toHaveBeenCalledWith("blob:shot.png");
  });
});
