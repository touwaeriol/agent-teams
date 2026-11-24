// @vitest-environment jsdom
// 节点页的调度阈值：读 GET /api/me/scheduling，改了才能保存，PUT 带两个百分比，越界不让保存。
import { QueryClient, VueQueryPlugin } from "@tanstack/vue-query";
import { enableAutoUnmount, flushPromises, mount } from "@vue/test-utils";
import ElementPlus from "element-plus";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import Scheduling from "@/pages/settings/nodes/Scheduling.vue";

const settings = { five_hour_pct: 85, weekly_pct: 95, defaults: { five_hour_pct: 85, weekly_pct: 95 } };

function api() {
  return vi.spyOn(globalThis, "fetch").mockImplementation(async (input) => {
    const req = input as Request;
    const body = req.method === "PUT" ? { ...(await req.clone().json()), defaults: settings.defaults } : settings;
    return new Response(JSON.stringify(body), { status: 200, headers: { "Content-Type": "application/json" } });
  });
}

function mountIt() {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return mount(Scheduling, { global: { plugins: [[VueQueryPlugin, { queryClient }], ElementPlus] as never[] }, attachTo: document.body });
}

beforeEach(() => {
  document.cookie = "agent_teams_csrf=tok";
});
afterEach(() => {
  vi.restoreAllMocks();
  document.body.innerHTML = "";
});
enableAutoUnmount(afterEach);

const input = (w: ReturnType<typeof mountIt>, id: string) => w.get(`[data-testid="${id}"] input`);
const saveButton = (w: ReturnType<typeof mountIt>) => w.get('[data-testid="save-scheduling"]');

it("shows the thresholds and saves both after a change", async () => {
  const spy = api();
  const w = mountIt();
  await flushPromises();
  expect((input(w, "five-hour-pct").element as HTMLInputElement).value).toBe("85");
  expect((input(w, "weekly-pct").element as HTMLInputElement).value).toBe("95");
  expect(w.text()).toContain("5 小时窗口超过多少后不再主动派会话");
  expect(w.text()).toContain("默认 85%");
  expect(saveButton(w).attributes("disabled")).toBeDefined();
  await input(w, "five-hour-pct").setValue("70");
  await input(w, "five-hour-pct").trigger("change");
  await flushPromises();
  expect(saveButton(w).attributes("disabled")).toBeUndefined();
  await saveButton(w).trigger("click");
  await flushPromises();
  const puts = spy.mock.calls.map((c) => c[0] as Request).filter((r) => r.method === "PUT");
  expect(puts).toHaveLength(1);
  expect(new URL(puts[0].url).pathname).toBe("/api/me/scheduling");
  expect(await puts[0].clone().json()).toEqual({ five_hour_pct: 70, weekly_pct: 95 });
});

it("keeps the value within 1–100", async () => {
  api();
  const w = mountIt();
  await flushPromises();
  await input(w, "weekly-pct").setValue("150");
  await input(w, "weekly-pct").trigger("change");
  await flushPromises();
  expect((input(w, "weekly-pct").element as HTMLInputElement).value).toBe("100");
  await input(w, "five-hour-pct").setValue("0");
  await input(w, "five-hour-pct").trigger("change");
  await flushPromises();
  expect((input(w, "five-hour-pct").element as HTMLInputElement).value).toBe("1");
});
