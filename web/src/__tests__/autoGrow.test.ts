// @vitest-environment jsdom
// useAutoGrow: jsdom has no layout, so scrollHeight / offsetWidth are faked from the text
// (24px lines + 14px top padding, like the composer's textarea).
import { mount } from "@vue/test-utils";
import { afterAll, afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import { defineComponent, h, nextTick, ref } from "vue";
import { defaultMaxHeight, useAutoGrow } from "@/components/app/composer/useAutoGrow";

const LINE = 24;
const PAD = 14;
const restore: (() => void)[] = [];

function fake<K extends keyof HTMLElement>(proto: object, key: K, get: (this: HTMLTextAreaElement) => number) {
  const prev = Object.getOwnPropertyDescriptor(proto, key);
  Object.defineProperty(proto, key, { configurable: true, get });
  restore.push(() => (prev ? Object.defineProperty(proto, key, prev) : delete (proto as Record<string, unknown>)[key]));
}

beforeAll(() => {
  fake(HTMLTextAreaElement.prototype, "scrollHeight", function () {
    return PAD + Math.max(1, this.value.split("\n").length) * LINE;
  });
  fake(HTMLElement.prototype, "offsetWidth", () => 600);
});
afterAll(() => restore.forEach((f) => f()));

function setup(max = 200) {
  const text = ref("");
  const resized: number[] = [];
  let grow!: ReturnType<typeof useAutoGrow>;
  const C = defineComponent({
    setup() {
      const field = ref<HTMLTextAreaElement | null>(null);
      grow = useAutoGrow(field, text, { maxHeight: () => max, onResize: (el) => resized.push(el.offsetHeight) });
      return () => h("textarea", { ref: field, value: text.value, style: `line-height:${LINE}px;padding-top:${PAD}px;padding-bottom:0;font-size:15px` });
    },
  });
  const w = mount(C, { attachTo: document.body });
  const el = w.element as HTMLTextAreaElement;
  const set = async (v: string) => {
    text.value = v;
    await nextTick();
    await nextTick();
  };
  return { w, el, set, grow: () => grow, resized };
}

// jsdom has no ResizeObserver: a fake whose callbacks the tests fire by hand.
class FakeObserver {
  static all: FakeObserver[] = [];
  targets: Element[] = [];
  constructor(private cb: () => void) {
    FakeObserver.all.push(this);
  }
  observe(el: Element) {
    this.targets.push(el);
  }
  disconnect() {
    this.targets = [];
  }
  static fire(el: Element) {
    for (const o of FakeObserver.all) if (o.targets.includes(el)) o.cb();
  }
}
afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
  FakeObserver.all = [];
});

const lines = (n: number) => Array.from({ length: n }, (_, i) => `line ${i + 1}`).join("\n");
const MIN = 2 * LINE + PAD; // 62

describe("useAutoGrow", () => {
  it("starts at two lines and grows with the content", async () => {
    const { el, set, w } = setup();
    await nextTick();
    expect(el.style.height).toBe(`${MIN}px`);
    expect(el.style.overflowY).toBe("hidden");
    await set(lines(4));
    expect(el.style.height).toBe(`${PAD + 4 * LINE}px`);
    await set(lines(6));
    expect(el.style.height).toBe(`${PAD + 6 * LINE}px`);
    expect(el.style.overflowY).toBe("hidden");
    w.unmount();
  });

  it("stops at the cap and scrolls inside", async () => {
    const { el, set, grow, w } = setup(200);
    await set(lines(7)); // 182
    expect(el.style.height).toBe("182px");
    expect(grow().scrolling.value).toBe(false);
    await set(lines(8)); // 206 > 200
    expect(el.style.height).toBe("200px");
    expect(el.style.overflowY).toBe("auto");
    expect(grow().scrolling.value).toBe(true);
    await set(lines(40));
    expect(el.style.height).toBe("200px");
    expect(el.style.overflowY).toBe("auto");
    w.unmount();
  });

  it("shrinks back when text is deleted and returns to two lines when cleared", async () => {
    const { el, set, grow, w } = setup(200);
    await set(lines(40));
    expect(el.style.height).toBe("200px");
    await set(lines(5));
    expect(el.style.height).toBe(`${PAD + 5 * LINE}px`);
    expect(el.style.overflowY).toBe("hidden");
    expect(grow().scrolling.value).toBe(false);
    await set("");
    expect(el.style.height).toBe(`${MIN}px`);
    w.unmount();
  });

  it("measures on a hidden copy, never collapsing the real box", async () => {
    const { el, set, w } = setup(200);
    const seen: string[] = [];
    const style = el.style;
    const desc = Object.getOwnPropertyDescriptor(CSSStyleDeclaration.prototype, "height");
    Object.defineProperty(style, "height", {
      configurable: true,
      get: () => desc?.get?.call(style) as string,
      set: (v: string) => (seen.push(v), desc?.set?.call(style, v)),
    });
    await set(lines(5));
    await set(lines(3));
    expect(seen).not.toContain("auto");
    expect(seen.every((v) => v.endsWith("px"))).toBe(true);
    w.unmount();
  });

  it("re-measures on window resize (the cap follows the viewport)", async () => {
    let max = 400;
    const text = ref(lines(12)); // 302
    const C = defineComponent({
      setup() {
        const field = ref<HTMLTextAreaElement | null>(null);
        useAutoGrow(field, text, { maxHeight: () => max });
        return () => h("textarea", { ref: field, value: text.value, style: `line-height:${LINE}px;padding-top:${PAD}px` });
      },
    });
    const w = mount(C, { attachTo: document.body });
    await nextTick();
    const el = w.element as HTMLTextAreaElement;
    expect(el.style.height).toBe("302px");
    max = 240;
    window.dispatchEvent(new Event("resize"));
    expect(el.style.height).toBe("240px");
    expect(el.style.overflowY).toBe("auto");
    w.unmount();
  });

  it("lowers the cap to keep the neighbouring chat visible", async () => {
    const text = ref(lines(30));
    // The chat and the box share a column: the chat gets what the box leaves.
    let column = 362;
    const C = defineComponent({
      setup() {
        const field = ref<HTMLTextAreaElement | null>(null);
        useAutoGrow(field, text, { maxHeight: () => 400, keepVisible: 120 });
        return () =>
          h("div", { style: "display:flex;flex-direction:column" }, [
            h("div", { "data-chat": "", style: "overflow-y:auto;flex-grow:1" }),
            h("div", [h("textarea", { ref: field, value: text.value, style: `line-height:${LINE}px;padding-top:${PAD}px` })]),
          ]);
      },
    });
    const w = mount(C, { attachTo: document.body });
    const chat = w.find("[data-chat]").element as HTMLElement;
    const el = w.find("textarea").element as HTMLTextAreaElement;
    const box = () => parseFloat(el.style.height) || 62;
    Object.defineProperty(el, "offsetHeight", { configurable: true, get: box });
    Object.defineProperty(chat, "clientHeight", { configurable: true, get: () => Math.max(0, column - box()) });
    await nextTick();
    expect(el.style.height).toBe("242px"); // 362 - 120 kept for the chat
    expect(el.style.overflowY).toBe("auto");
    window.dispatchEvent(new Event("resize"));
    expect(el.style.height).toBe("242px"); // stable: its own height does not feed back
    // less room (attachments, a plan card, a shorter window): the box gives space back, never below two lines
    column = 300;
    window.dispatchEvent(new Event("resize"));
    expect(el.style.height).toBe("180px");
    column = 100;
    window.dispatchEvent(new Event("resize"));
    expect(el.style.height).toBe("62px");
    w.unmount();
  });

  it("re-caps when the chat changes height on its own, without feeding back on itself", async () => {
    vi.stubGlobal("ResizeObserver", FakeObserver);
    const text = ref(lines(30));
    let column = 362; // box + chat
    let updates = 0;
    const C = defineComponent({
      setup() {
        const field = ref<HTMLTextAreaElement | null>(null);
        useAutoGrow(field, text, { maxHeight: () => 400, keepVisible: 120, onResize: () => updates++ });
        return () =>
          h("div", { style: "display:flex;flex-direction:column" }, [
            h("div", { "data-chat": "", style: "overflow-y:auto;flex-grow:1" }),
            h("div", [h("textarea", { ref: field, value: text.value, style: `line-height:${LINE}px;padding-top:${PAD}px` })]),
          ]);
      },
    });
    const w = mount(C, { attachTo: document.body });
    const chat = w.find("[data-chat]").element as HTMLElement;
    const el = w.find("textarea").element as HTMLTextAreaElement;
    const box = () => parseFloat(el.style.height) || 62;
    Object.defineProperty(el, "offsetHeight", { configurable: true, get: box });
    Object.defineProperty(chat, "clientHeight", { configurable: true, get: () => Math.max(0, column - box()) });
    await nextTick();
    expect(el.style.height).toBe("242px");
    // The box growing shrank the chat by as much: box + chat unchanged, nothing is re-measured.
    const measures = vi.spyOn(window, "getComputedStyle");
    FakeObserver.fire(chat);
    expect(measures).not.toHaveBeenCalled();
    expect(el.style.height).toBe("242px");
    // A plan card unfolds above: the chat loses 62px, the box gives it back (text unchanged).
    column = 300;
    FakeObserver.fire(chat);
    expect(el.style.height).toBe("180px");
    // ...which grows the chat again by as much: the follow-up observation is a no-op.
    const settled = updates;
    measures.mockClear();
    FakeObserver.fire(chat);
    expect(measures).not.toHaveBeenCalled();
    expect(updates).toBe(settled);
    expect(el.style.height).toBe("180px");
    // The card folds away: room comes back, the box takes it up to the cap again.
    column = 362;
    FakeObserver.fire(chat);
    expect(el.style.height).toBe("242px");
    w.unmount();
  });

  it("keeps the caret line in view at the cap after text changes, but not when merely re-measured", async () => {
    const { el, set, grow, w } = setup(200);
    let top = 0;
    Object.defineProperty(el, "scrollTop", { configurable: true, get: () => top, set: (v: number) => (top = v) });
    Object.defineProperty(el, "clientHeight", { configurable: true, get: () => parseFloat(el.style.height) || 0 });
    // Long text with the caret at its end: the last line sits at the bottom of the box.
    await set(lines(40));
    expect(el.style.height).toBe("200px");
    expect(top).toBe(PAD + 40 * LINE - 200); // 774
    // The user scrolls up to read; a re-measure without a text change leaves that alone.
    top = 0;
    grow().update();
    await nextTick();
    expect(top).toBe(0);
    // Dictation appends and places the caret in its own nextTick (setSelectionRange does not scroll).
    const more = `${lines(40)}\nline 41`;
    const done = set(more);
    void nextTick(() => el.setSelectionRange(more.length, more.length));
    await done;
    await nextTick();
    expect(top).toBe(PAD + 41 * LINE - 200); // 798: the new line is visible
    // Text inserted near the top with the caret there: scrolled up to that line.
    const edited = more.replace("line 5", "line 5!");
    const caret = edited.indexOf("line 5!") + "line 5!".length;
    const p = set(edited);
    void nextTick(() => el.setSelectionRange(caret, caret));
    await p;
    await nextTick();
    expect(top).toBe(4 * LINE); // caret line top (14 + 4*24) minus the top padding
    // Caret already visible: no scrolling at all.
    top = 50;
    const again = edited.replace("line 5!", "line 5!!");
    const c2 = again.indexOf("line 5!!") + "line 5!!".length;
    const q = set(again);
    void nextTick(() => el.setSelectionRange(c2, c2));
    await q;
    await nextTick();
    expect(top).toBe(50);
    w.unmount();
  });

  it("ignores a one-line input (the inline composer)", async () => {
    const text = ref(lines(5));
    const C = defineComponent({
      setup() {
        const field = ref<HTMLInputElement | null>(null);
        useAutoGrow(field, text);
        return () => h("input", { ref: field, value: text.value });
      },
    });
    const w = mount(C, { attachTo: document.body });
    await nextTick();
    expect((w.element as HTMLInputElement).style.height).toBe("");
    w.unmount();
  });

  it("caps at 40% of the window with a 160px floor", () => {
    const prev = window.innerHeight;
    Object.defineProperty(window, "innerHeight", { configurable: true, value: 1000 });
    expect(defaultMaxHeight()).toBe(400);
    Object.defineProperty(window, "innerHeight", { configurable: true, value: 300 });
    expect(defaultMaxHeight()).toBe(160);
    Object.defineProperty(window, "innerHeight", { configurable: true, value: prev });
  });
});
