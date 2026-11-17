// @vitest-environment jsdom
// The acceptance card: immediate 合并中 on click (optimistic cache update), one request per click
// burst, rollback on a failed request, locked while the server says accepting, retry after merge_error.
import { QueryClient, useQuery, VueQueryPlugin } from "@tanstack/vue-query";
import { flushPromises, mount } from "@vue/test-utils";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { defineComponent, h } from "vue";
import { keys } from "@/api/queries";
import type { ThreadView } from "@/api/views";
import ReviewCard from "@/components/app/cards/ReviewCard.vue";
import { showReviewCard, threadLine } from "@/lib/present";
import { thread } from "./fixtures";

const api = vi.hoisted(() => ({ accept: vi.fn(), reject: vi.fn() }));
const toasts = vi.hoisted(() => ({ success: vi.fn(), error: vi.fn() }));
vi.mock("@/api/actions", () => ({ threads: api }));
vi.mock("@/lib/toast", () => ({ toast: toasts }));

function deferred<T>() {
  let resolve!: (v: T) => void;
  let reject!: (e: unknown) => void;
  const promise = new Promise<T>((res, rej) => ((resolve = res), (reject = rej)));
  return { promise, resolve, reject };
}

/** Mounts the card the way the app does: the thread is read from the project's thread list cache. */
function setup(patch: Partial<ThreadView> = {}) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false, staleTime: Infinity } } });
  const t = thread(12, { status: "review", pr_number: 7, pr_url: "https://github.com/o/r/pull/7", pr_state: "open", ...patch });
  // What a refetch of the list returns (the server's view).
  const server: { thread: ThreadView; hold?: boolean } = { thread: t };
  qc.setQueryData(keys.threads("p1"), [t]);
  const Host = defineComponent(() => {
    const list = useQuery({ queryKey: keys.threads("p1"), queryFn: () => (server.hold ? new Promise<ThreadView[]>(() => {}) : Promise.resolve([server.thread])) });
    return () => (list.data.value?.[0] ? h(ReviewCard, { thread: list.data.value[0] }) : null);
  });
  const mountHost = () => mount(Host, { global: { plugins: [[VueQueryPlugin, { queryClient: qc }]] as never[] } });
  const ctx = { wrapper: mountHost() };
  const cached = () => qc.getQueryData<ThreadView[]>(keys.threads("p1"))![0];
  /** What a live thread event does to the caches. */
  const event = (p: Partial<ThreadView>) => qc.setQueryData<ThreadView[]>(keys.threads("p1"), (l) => l!.map((x) => ({ ...x, ...p })));
  const primary = () => ctx.wrapper.findAll("button")[0];
  const outline = () => ctx.wrapper.findAll("button")[1];
  const remount = () => {
    ctx.wrapper.unmount();
    ctx.wrapper = mountHost();
  };
  return {
    qc, server, cached, event, primary, outline, remount,
    get wrapper() {
      return ctx.wrapper;
    },
  };
}

beforeEach(() => {
  api.accept.mockReset();
  api.reject.mockReset();
  toasts.success.mockReset();
  toasts.error.mockReset();
});

describe("ReviewCard accept", () => {
  it("locks at once and sends one request for a double click", async () => {
    const d = deferred<ThreadView>();
    api.accept.mockReturnValue(d.promise);
    const { server, primary, outline } = setup();
    const el = primary().element as HTMLButtonElement;
    el.click();
    el.click(); // same tick: the disabled attribute is not rendered yet
    await flushPromises();
    await primary().trigger("click");
    expect(api.accept).toHaveBeenCalledTimes(1);
    expect(primary().attributes("disabled")).toBeDefined();
    expect(primary().attributes("aria-busy")).toBe("true");
    // The spinner is decorative: no status region, no "加载中" in the accessible name.
    expect(primary().find("span").attributes("aria-hidden")).toBe("true");
    expect(primary().text()).toBe("合并中…");
    expect(outline().attributes("disabled")).toBeDefined();
    server.thread = thread(12, { status: "review", pr_number: 7, pr_state: "accepting" });
    d.resolve(server.thread);
    await flushPromises();
    // Between the response and the live event the server state keeps it locked.
    expect(primary().attributes("disabled")).toBeDefined();
    await primary().trigger("click");
    expect(api.accept).toHaveBeenCalledTimes(1);
    expect(toasts.success).toHaveBeenCalledWith(expect.stringContaining("已开始合并"));
  });

  it("shows 合并中 optimistically and clears an old merge error", async () => {
    api.accept.mockReturnValue(new Promise(() => {}));
    const { wrapper, cached, primary } = setup({ merge_error: "有冲突" });
    await primary().trigger("click");
    await flushPromises();
    expect(cached().pr_state).toBe("accepting");
    expect(cached().merge_error).toBeNull();
    expect(wrapper.find("section").attributes("aria-label")).toBe("合并中");
    expect(wrapper.text()).toContain("合并中…");
    expect(wrapper.text()).not.toContain("有冲突");
  });

  it("rolls back and re-enables the buttons when the request fails", async () => {
    api.accept.mockRejectedValue(new Error("网络错误"));
    const { wrapper, cached, primary, outline } = setup({ merge_error: "检查未通过" });
    await primary().trigger("click");
    await flushPromises();
    expect(cached().pr_state).toBe("open");
    expect(cached().merge_error).toBe("检查未通过");
    expect(toasts.error).toHaveBeenCalledWith("网络错误");
    expect(primary().attributes("disabled")).toBeUndefined();
    expect(outline().attributes("disabled")).toBeUndefined();
    expect(wrapper.find("section").attributes("aria-label")).toBe("合并失败");
  });

  it("locks the merge button while the server reports accepting, but a change request can withdraw it", async () => {
    api.reject.mockResolvedValue({});
    const s = setup({ pr_state: "accepting" });
    expect(s.primary().attributes("disabled")).toBeDefined();
    expect(s.wrapper.text()).toContain("合并中");
    expect(s.wrapper.findAll('[role="status"]')).toHaveLength(0);
    await s.primary().trigger("click");
    expect(api.accept).not.toHaveBeenCalled();
    expect(s.outline().attributes("disabled")).toBeUndefined();
    await s.outline().trigger("click");
    await s.wrapper.find("textarea").setValue("先别合，标题要改");
    await s.wrapper.find("form").trigger("submit");
    await flushPromises();
    expect(api.reject).toHaveBeenCalledWith("t12", "先别合，标题要改");
  });

  it("shows the merge error and retries", async () => {
    api.accept.mockReturnValue(new Promise(() => {}));
    const { wrapper, primary } = setup({ pr_state: "open", merge_error: "与 develop 有冲突，需要先解决" });
    expect(wrapper.find('[role="alert"]').text()).toContain("与 develop 有冲突，需要先解决");
    expect(wrapper.text()).toContain("合并失败");
    expect(primary().text()).toBe("重试合并");
    expect(primary().attributes("disabled")).toBeUndefined();
    await primary().trigger("click");
    await flushPromises();
    expect(api.accept).toHaveBeenCalledTimes(1);
  });

  it("follows a live failure event: shows the reason and offers a retry", async () => {
    const s = setup({ pr_state: "accepting" });
    s.event({ pr_state: "open", merge_error: "权限不足" });
    await flushPromises();
    expect(s.primary().text()).toBe("重试合并");
    expect(s.wrapper.find('[role="alert"]').text()).toContain("权限不足");
  });

  it("does not roll back when a live event already merged the thread", async () => {
    const d = deferred<ThreadView>();
    api.accept.mockReturnValue(d.promise);
    const s = setup();
    await s.primary().trigger("click");
    await flushPromises();
    s.server.thread = { ...s.cached(), status: "done", pr_state: "merged" };
    s.event({ status: "done", pr_state: "merged" });
    d.reject(new Error("网关超时"));
    await flushPromises();
    expect(s.cached()).toMatchObject({ status: "done", pr_state: "merged" });
  });

  it("keeps a live event that came before the response", async () => {
    const d = deferred<ThreadView>();
    api.accept.mockReturnValue(d.promise);
    const s = setup();
    await s.primary().trigger("click");
    await flushPromises();
    s.server.hold = true; // no refetch to paper over a stale write: only the onSuccess guard keeps the event
    s.event({ pr_state: "open", merge_error: "有冲突" });
    d.resolve({ ...s.cached(), pr_state: "accepting", merge_error: null });
    await flushPromises();
    expect(s.cached()).toMatchObject({ pr_state: "open", merge_error: "有冲突" });
    expect(s.primary().text()).toBe("重试合并");
  });

  it("releases the lock when live events end the merge while the request hangs", async () => {
    api.accept.mockReturnValue(new Promise(() => {}));
    const s = setup();
    await s.primary().trigger("click");
    await flushPromises();
    expect(s.outline().attributes("disabled")).toBeDefined();
    s.event({ pr_state: "open", merge_error: "检查未通过" });
    await flushPromises();
    expect(s.primary().text()).toBe("重试合并");
    expect(s.primary().attributes("disabled")).toBeUndefined();
    expect(s.outline().attributes("disabled")).toBeUndefined();
    await s.primary().trigger("click");
    await flushPromises();
    expect(api.accept).toHaveBeenCalledTimes(2);
  });

  it("stays 合并中 across an unmount and remount while the request runs", async () => {
    const d = deferred<ThreadView>();
    api.accept.mockReturnValue(d.promise);
    const s = setup();
    await s.primary().trigger("click");
    await flushPromises();
    s.remount();
    await flushPromises();
    expect(s.wrapper.find("section").attributes("aria-label")).toBe("合并中");
    expect(s.primary().attributes("disabled")).toBeDefined();
    await s.primary().trigger("click");
    d.reject(new Error("网络错误"));
    await flushPromises();
    expect(api.accept).toHaveBeenCalledTimes(1);
    expect(s.cached().pr_state).toBe("open");
    expect(s.primary().attributes("disabled")).toBeUndefined();
  });
});

describe("ReviewCard change request", () => {
  it("sends once, keeps the text on failure and closes on success", async () => {
    const d = deferred<unknown>();
    api.reject.mockReturnValueOnce(Promise.reject(new Error("发送失败"))).mockReturnValueOnce(d.promise);
    const { wrapper, outline } = setup();
    await outline().trigger("click");
    const submit = () => wrapper.find('button[type="submit"]');
    expect(submit().attributes("disabled")).toBeDefined(); // empty reason
    await wrapper.find("textarea").setValue("标题改一下");
    await wrapper.find("form").trigger("submit");
    await flushPromises();
    expect(toasts.error).toHaveBeenCalledWith("发送失败");
    expect((wrapper.find("textarea").element as HTMLTextAreaElement).value).toBe("标题改一下");
    const form = wrapper.find("form").element as HTMLFormElement;
    form.requestSubmit();
    form.requestSubmit();
    await flushPromises();
    expect(api.reject).toHaveBeenCalledTimes(2);
    expect(submit().attributes("disabled")).toBeDefined();
    d.resolve({});
    await flushPromises();
    expect(wrapper.find("textarea").exists()).toBe(false);
  });
});

describe("where the card shows", () => {
  it("only in review: a cancelled or failed thread stuck on accepting gets no locked card", () => {
    expect(showReviewCard(thread(1, { status: "review", pr_state: "accepting" }))).toBe(true);
    expect(showReviewCard(thread(1, { status: "review", pr_state: "open" }))).toBe(true);
    expect(showReviewCard(thread(1, { status: "failed", pr_state: "accepting" }))).toBe(false);
    expect(showReviewCard(thread(1, { status: "done", pr_state: "merged" }))).toBe(false);
  });
});

describe("thread line", () => {
  it("says 合并中 / 合并失败 for an accepting or failed acceptance", () => {
    expect(threadLine(thread(1, { status: "review", pr_number: 3, pr_state: "accepting" }))).toBe("PR #3 合并中");
    expect(threadLine(thread(1, { status: "review", pr_number: 3, pr_state: "open", merge_error: "x" }))).toBe("PR #3 合并失败，等你处理");
    expect(threadLine(thread(1, { status: "review", pr_number: 3, pr_state: "open" }))).toBe("PR #3 等你验收");
  });
});
