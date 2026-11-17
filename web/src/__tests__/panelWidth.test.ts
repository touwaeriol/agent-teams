// @vitest-environment jsdom
// The thread panel's width: list mode (remembered, clamped to the window) and detail mode
// (fills the board but the coordinator strip; a dragged width is remembered separately).
import { mount } from "@vue/test-utils";
import { describe, expect, it } from "vitest";
import { defineComponent, h, nextTick, ref } from "vue";
import ResizeHandle from "@/components/app/ResizeHandle.vue";
import { markdownTree, wrapTables } from "@/lib/markdown";
import { useDetailWidth, type DetailWidth } from "@/components/app/usePanelWidth";
import {
  COORDINATOR_PEEK,
  DETAIL_MIN,
  PANEL_DEFAULT,
  clampDetail,
  clampWidth,
  detailDefault,
  detailKey,
  dragWidth,
  loadDetail,
  loadWidth,
  panelKey,
  saveDetail,
  saveWidth,
} from "@/lib/panelWidth";

function memory(init: Record<string, string> = {}) {
  const data = new Map(Object.entries(init));
  return {
    data,
    getItem: (k: string) => data.get(k) ?? null,
    setItem: (k: string, v: string) => void data.set(k, v),
    removeItem: (k: string) => void data.delete(k),
  };
}
const broken = {
  getItem: () => {
    throw new Error("blocked");
  },
  setItem: () => {
    throw new Error("blocked");
  },
  removeItem: () => {
    throw new Error("blocked");
  },
};

describe("detail width", () => {
  it("defaults to the board minus the coordinator strip", () => {
    expect(COORDINATOR_PEEK).toBe(80);
    expect(detailDefault(1192)).toBe(1112); // 1440 window − 248 sidebar
    expect(detailDefault(1672)).toBe(1592);
    expect(detailDefault(50)).toBe(0);
  });

  it("clamps to [DETAIL_MIN, default]; null and junk follow the default", () => {
    expect(clampDetail(null, 1192)).toBe(1112);
    expect(clampDetail(NaN, 1192)).toBe(1112);
    expect(clampDetail(5000, 1192)).toBe(1112);
    expect(clampDetail(100, 1192)).toBe(DETAIL_MIN);
    expect(clampDetail(700.4, 1192)).toBe(700);
    // a board too small for the minimum: the default wins, the strip stays
    expect(clampDetail(100, 400)).toBe(320);
    expect(clampDetail(null, 400)).toBe(320);
  });

  it("loads and saves per user; the default is stored as nothing", () => {
    const s = memory();
    expect(loadDetail("u1", s)).toBeNull();
    saveDetail("u1", 700, 1192, s);
    expect(s.data.get(detailKey("u1"))).toBe("700");
    expect(detailKey("u1")).toBe("at:thread-detail-width:u1");
    expect(loadDetail("u1", s)).toBe(700);
    expect(loadDetail("u2", s)).toBeNull();
    saveDetail("u1", 1112, 1192, s);
    expect(s.data.has(detailKey("u1"))).toBe(false);
    saveDetail("u1", 700, 1192, s);
    saveDetail("u1", null, 1192, s);
    expect(s.data.has(detailKey("u1"))).toBe(false);
  });

  it("ignores junk and blocked or missing storage", () => {
    expect(loadDetail("u1", memory({ [detailKey("u1")]: "wide" }))).toBeNull();
    expect(loadDetail("u1", memory({ [detailKey("u1")]: "-3" }))).toBeNull();
    expect(loadDetail("u1", broken)).toBeNull();
    expect(loadDetail("u1", undefined)).toBeNull();
    expect(() => saveDetail("u1", 700, 1192, broken)).not.toThrow();
    expect(() => saveDetail("u1", 700, 1192, undefined)).not.toThrow();
  });

  it("is kept apart from the list width", () => {
    const s = memory();
    saveDetail("u1", 700, 1192, s);
    saveWidth("u1", 500, s);
    expect(s.data.get(panelKey("u1"))).toBe("500");
    expect(loadWidth("u1", s)).toBe(500);
    expect(loadDetail("u1", s)).toBe(700);
  });
});

describe("list width (unchanged)", () => {
  it("clamps to the window and forgets the default", () => {
    expect(clampWidth(NaN, 1440)).toBe(PANEL_DEFAULT);
    expect(clampWidth(100, 1440)).toBe(340);
    expect(clampWidth(2000, 1440)).toBe(760);
    const s = memory();
    saveWidth("u1", PANEL_DEFAULT, s);
    expect(s.data.size).toBe(0);
    expect(loadWidth("u1", broken)).toBe(PANEL_DEFAULT);
  });
});

describe("useDetailWidth", () => {
  function host(board: ReturnType<typeof ref<number>>, userId = ref("u1")) {
    let api!: DetailWidth;
    const C = defineComponent({
      setup() {
        api = useDetailWidth(() => userId.value, board as never);
        return () => h("div");
      },
    });
    mount(C);
    return api;
  }

  it("fills the board by default and follows it as it resizes", async () => {
    localStorage.clear();
    const board = ref(1192);
    const w = host(board);
    expect(w.width.value).toBe(1112);
    board.value = 1672;
    await nextTick();
    expect(w.width.value).toBe(1592);
  });

  it("remembers a dragged width, re-clamps it on a smaller board, and resets on double-click", async () => {
    localStorage.clear();
    const board = ref(1672);
    const w = host(board);
    w.set(900);
    expect(w.width.value).toBe(900);
    expect(localStorage.getItem(detailKey("u1"))).toBe("900");
    w.set(10);
    expect(w.width.value).toBe(DETAIL_MIN);
    w.set(900);
    board.value = 800; // window shrinks: max is now 720
    await nextTick();
    expect(w.width.value).toBe(720);
    board.value = 1672; // and grows back: the remembered width returns
    await nextTick();
    expect(w.width.value).toBe(900);
    w.reset();
    expect(w.width.value).toBe(1592);
    expect(localStorage.getItem(detailKey("u1"))).toBeNull();
    // a new mount picks up what was stored
    w.set(1000);
    expect(host(ref(1672)).width.value).toBe(1000);
  });

  it("dragging to the widest stores nothing, so a bigger screen still fills up", () => {
    localStorage.clear();
    const w = host(ref(1192));
    w.set(5000);
    expect(localStorage.getItem(detailKey("u1"))).toBeNull();
    expect(host(ref(1672)).width.value).toBe(1592);
  });
});

describe("ResizeHandle", () => {
  it("emits the dragged width (left widens), resets on double-click, nudges with keys", async () => {
    expect(dragWidth(600, 500, 450)).toBe(650);
    const w = mount(ResizeHandle, { props: { width: 600, label: "调整宽度", min: 420, max: 1112 } });
    const el = w.get("[role=separator]");
    expect(el.attributes("aria-valuemin")).toBe("420");
    expect(el.attributes("aria-valuemax")).toBe("1112");
    // jsdom has no PointerEvent: mouse events with the pointer names drive the same handlers
    const pointer = (type: string, clientX = 0) => el.element.dispatchEvent(new MouseEvent(type, { clientX, bubbles: true, cancelable: true }));
    pointer("pointerdown", 500);
    pointer("pointermove", 450);
    pointer("pointerup");
    pointer("pointermove", 300); // after release: ignored
    expect(w.emitted("change")).toEqual([[650]]);
    await el.trigger("dblclick");
    expect(w.emitted("reset")).toHaveLength(1);
    await el.trigger("keydown", { key: "ArrowLeft" });
    await el.trigger("keydown", { key: "ArrowRight", shiftKey: true });
    await el.trigger("keydown", { key: "Home" });
    expect(w.emitted("change")?.slice(1)).toEqual([[616], [536]]);
    expect(w.emitted("reset")).toHaveLength(2);
  });
});

describe("wide Markdown tables", () => {
  it("are wrapped in their own horizontal scroller", () => {
    const tree = markdownTree("| a | b |\n| - | - |\n| 1 | 2 |\n\ntext");
    wrapTables(tree);
    const wrap = tree.children.find((c) => c.type === "element" && c.tagName === "div");
    expect(wrap && wrap.type === "element" && wrap.properties.className).toEqual(["at-table-scroll"]);
    expect(wrap && wrap.type === "element" && wrap.children[0]).toMatchObject({ tagName: "table" });
  });
});
