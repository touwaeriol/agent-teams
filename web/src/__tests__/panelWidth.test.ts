// @vitest-environment jsdom
// The thread panel's width: one width for the list and an open thread, clamped to the board
// (less the coordinator strip) and remembered per user.
import { mount } from "@vue/test-utils";
import { describe, expect, it } from "vitest";
import { defineComponent, h, nextTick, ref } from "vue";
import ResizeHandle from "@/components/app/ResizeHandle.vue";
import { markdownTree, wrapTables } from "@/lib/markdown";
import { usePanelWidth, type PanelWidth } from "@/components/app/usePanelWidth";
import { COORDINATOR_PEEK, PANEL_DEFAULT, PANEL_MIN, clampWidth, dragWidth, loadWidth, panelKey, panelMax, roomForChat, saveWidth } from "@/lib/panelWidth";

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

describe("panel width", () => {
  it("is at most the board less the coordinator strip", () => {
    expect(COORDINATOR_PEEK).toBe(80);
    expect(panelMax(1192)).toBe(1112); // 1440 window − 248 sidebar
    expect(panelMax(1672)).toBe(1592);
    expect(panelMax(50)).toBe(PANEL_MIN);
  });

  it("clamps to [PANEL_MIN, max]; null and junk give the default", () => {
    expect(clampWidth(null, 1192)).toBe(PANEL_DEFAULT);
    expect(clampWidth(NaN, 1192)).toBe(PANEL_DEFAULT);
    expect(clampWidth(5000, 1192)).toBe(1112);
    expect(clampWidth(100, 1192)).toBe(PANEL_MIN);
    expect(clampWidth(700.4, 1192)).toBe(700);
    // a board too small for the default
    expect(clampWidth(null, 450)).toBe(370);
  });

  it("makes room for the coordinator chat when asked", () => {
    expect(roomForChat(1112, 1192)).toBe(832); // the chat gets 360
    expect(roomForChat(500, 1192)).toBe(500); // already enough room
    expect(roomForChat(1000, 600)).toBe(PANEL_MIN); // can't: the narrowest panel
  });

  it("loads and saves per user; the default is stored as nothing; the old detail width is dropped", () => {
    const s = memory({ "at:thread-detail-width:u1": "900" });
    expect(loadWidth("u1", s)).toBe(PANEL_DEFAULT);
    saveWidth("u1", 700, s);
    expect(s.data.get(panelKey("u1"))).toBe("700");
    expect(panelKey("u1")).toBe("at:thread-panel-width:u1");
    expect(s.data.has("at:thread-detail-width:u1")).toBe(false);
    expect(loadWidth("u1", s)).toBe(700);
    expect(loadWidth("u2", s)).toBe(PANEL_DEFAULT);
    saveWidth("u1", PANEL_DEFAULT, s);
    expect(s.data.has(panelKey("u1"))).toBe(false);
  });

  it("ignores junk and blocked or missing storage", () => {
    expect(loadWidth("u1", memory({ [panelKey("u1")]: "wide" }))).toBe(PANEL_DEFAULT);
    expect(loadWidth("u1", memory({ [panelKey("u1")]: "-3" }))).toBe(PANEL_DEFAULT);
    expect(loadWidth("u1", broken)).toBe(PANEL_DEFAULT);
    expect(loadWidth("u1", undefined)).toBe(PANEL_DEFAULT);
    expect(() => saveWidth("u1", 700, broken)).not.toThrow();
    expect(() => saveWidth("u1", 700, undefined)).not.toThrow();
  });
});

describe("usePanelWidth", () => {
  function host(board: ReturnType<typeof ref<number>>, userId = ref("u1")) {
    let api!: PanelWidth;
    const C = defineComponent({
      setup() {
        api = usePanelWidth(() => userId.value, board as never);
        return () => h("div");
      },
    });
    mount(C);
    return api;
  }

  it("remembers a dragged width, re-clamps it on a smaller board, and resets on double-click", async () => {
    localStorage.clear();
    const board = ref(1672);
    const w = host(board);
    expect(w.width.value).toBe(PANEL_DEFAULT);
    w.set(900);
    expect(w.width.value).toBe(900);
    expect(localStorage.getItem(panelKey("u1"))).toBe("900");
    w.set(10);
    expect(w.width.value).toBe(PANEL_MIN);
    w.set(900);
    board.value = 800; // window shrinks: max is now 720
    await nextTick();
    expect(w.width.value).toBe(720);
    expect(w.max.value).toBe(720);
    board.value = 1672; // and grows back: the remembered width returns
    await nextTick();
    expect(w.width.value).toBe(900);
    w.reset();
    expect(w.width.value).toBe(PANEL_DEFAULT);
    expect(localStorage.getItem(panelKey("u1"))).toBeNull();
    // a new mount picks up what was stored
    w.set(1000);
    expect(host(ref(1672)).width.value).toBe(1000);
  });

  it("follows the signed-in user once known", async () => {
    localStorage.clear();
    localStorage.setItem(panelKey("u2"), "600");
    const user = ref("anon");
    const w = host(ref(1672), user);
    expect(w.width.value).toBe(PANEL_DEFAULT);
    user.value = "u2";
    await nextTick();
    expect(w.width.value).toBe(600);
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
