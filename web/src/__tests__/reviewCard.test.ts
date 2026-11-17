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
import { threadLine } from "@/lib/present";
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
  const server = { thread: t };
  qc.setQueryData(keys.threads("p1"), [t]);
  const Host = defineComponent(() => {
    const list = useQuery({ queryKey: keys.threads("p1"), queryFn: async () => [server.thread] });
    return () => (list.data.value?.[0] ? h(ReviewCard, { thread: list.data.value[0] }) : null);
  });
  const wrapper = mount(Host, { global: { plugins: [[VueQueryPlugin, { queryClient: qc }]] as never[] } });
  const cached = () => qc.getQueryData<ThreadView[]>(keys.threads("p1"))![0];
  const primary = () => wrapper.findAll("button")[0];
  const outline = () => wrapper.findAll("button")[1];
  return { qc, server, wrapper, cached, primary, outline };
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
    expect(primary().find('[role="status"]').exists()).toBe(true);
    expect(primary().text()).toContain("合并中");
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

  it("is locked while the server reports accepting", async () => {
    const { wrapper, primary, outline } = setup({ pr_state: "accepting" });
    expect(primary().attributes("disabled")).toBeDefined();
    expect(outline().attributes("disabled")).toBeDefined();
    expect(wrapper.text()).toContain("合并中");
    await primary().trigger("click");
    await outline().trigger("click");
    expect(api.accept).not.toHaveBeenCalled();
    expect(wrapper.find("textarea").exists()).toBe(false);
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

  it("follows live events: merged elsewhere, then failed", async () => {
    const { qc, wrapper, primary } = setup({ pr_state: "accepting" });
    qc.setQueryData<ThreadView[]>(keys.threads("p1"), (l) => l!.map((t) => ({ ...t, pr_state: "open", merge_error: "权限不足" })));
    await flushPromises();
    expect(primary().text()).toBe("重试合并");
    expect(wrapper.find('[role="alert"]').text()).toContain("权限不足");
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

describe("thread line", () => {
  it("says 合并中 / 合并失败 for an accepting or failed acceptance", () => {
    expect(threadLine(thread(1, { status: "review", pr_number: 3, pr_state: "accepting" }))).toBe("PR #3 合并中");
    expect(threadLine(thread(1, { status: "review", pr_number: 3, pr_state: "open", merge_error: "x" }))).toBe("PR #3 合并失败，等你处理");
    expect(threadLine(thread(1, { status: "review", pr_number: 3, pr_state: "open" }))).toBe("PR #3 等你验收");
  });
});
