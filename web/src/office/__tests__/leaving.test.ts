import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { effectScope, nextTick, shallowRef } from "vue";
import { fadeMsOf } from "../compile";
import { LEAVE_POLL_MS, useLeaving } from "../leaving";
import type { OfficeEmployee, OfficeThread } from "../types";
import { defaultLayout } from "./fixtures";

const twelve: OfficeThread = { no: 12, title: "员工通信与审批", status: "running" };
const lead: OfficeEmployee = { id: "lead-12", name: "lead-12", tier: "lead", role: "lead", thread_no: 12 };

function setup() {
  const layout = defaultLayout();
  const threads = shallowRef<OfficeThread[]>([twelve]);
  const employees = shallowRef<OfficeEmployee[]>([lead]);
  const out = new Set<string>();
  const scope = effectScope();
  const leaving = scope.run(() => useLeaving(() => layout, () => threads.value, () => employees.value, (id) => out.has(id)))!;
  const set = async (list: OfficeThread[]) => {
    threads.value = list;
    await nextTick();
  };
  return { fade: fadeMsOf(layout), leaving, out, set, scope };
}

describe("useLeaving", () => {
  beforeEach(() => vi.useFakeTimers());
  afterEach(() => vi.useRealTimers());

  it("keeps an ended thread past the fade until its lead is out", async () => {
    const { fade, leaving, out, set } = setup();
    await set([]);
    expect(leaving.value.get(12)?.leads.map((e) => e.id)).toEqual(["lead-12"]);
    vi.advanceTimersByTime(fade + LEAVE_POLL_MS * 4);
    expect(leaving.value.has(12)).toBe(true);
    out.add("lead-12");
    vi.advanceTimersByTime(LEAVE_POLL_MS);
    expect(leaving.value.has(12)).toBe(false);
  });

  it("a resumed thread leaves the set; ending again starts a new fade", async () => {
    const { fade, leaving, out, set } = setup();
    out.add("lead-12");
    await set([]);
    vi.advanceTimersByTime(fade / 2);
    await set([twelve]);
    expect(leaving.value.has(12)).toBe(false);
    vi.advanceTimersByTime(fade / 4);
    await set([]);
    const since = leaving.value.get(12)!.since;
    vi.advanceTimersByTime(fade / 2);
    expect(leaving.value.get(12)?.since).toBe(since);
    vi.advanceTimersByTime(fade / 2);
    expect(leaving.value.has(12)).toBe(false);
  });

  it("clears its timers with the scope", async () => {
    const { leaving, set, scope } = setup();
    await set([]);
    scope.stop();
    vi.runAllTimers();
    expect(leaving.value.has(12)).toBe(true);
  });
});
