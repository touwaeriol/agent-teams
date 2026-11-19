// @vitest-environment jsdom
// useAutoGrow: jsdom has no layout, so scrollHeight / offsetWidth are faked from the text
// (24px lines + 14px top padding, like the composer's textarea).
import { mount } from "@vue/test-utils";
import { afterAll, beforeAll, describe, expect, it } from "vitest";
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
