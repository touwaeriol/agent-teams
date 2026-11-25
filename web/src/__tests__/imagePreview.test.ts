// @vitest-environment jsdom
// In-page image preview: sent-message thumbnails and composer attachments open an overlay viewer
// (ElImageViewer) instead of a new tab; Esc / mask click close it, ←/→ and the arrows switch.
import { flushPromises, mount } from "@vue/test-utils";
import { afterEach, describe, expect, it, vi } from "vitest";
import { defineComponent, h, ref } from "vue";
import type { AttachmentRef } from "@/api/views";
import AttachmentStrip from "@/components/app/AttachmentStrip.vue";
import type { Attachments, PendingImage } from "@/components/app/attachments";
import MessageImages from "@/components/app/MessageImages.vue";
import { attachmentUrl } from "@/lib/images";

const img = (id: string): AttachmentRef => ({ id, name: `${id}.png`, content_type: "image/png", size: 1 });
const viewer = () => document.body.querySelector(".el-image-viewer__wrapper");
const shown = () => document.body.querySelector<HTMLImageElement>(".el-image-viewer__img")?.getAttribute("src");
const progress = () => document.body.querySelector(".el-image-viewer__progress")?.textContent?.trim();
const key = (k: string) => document.dispatchEvent(new KeyboardEvent("keydown", { key: k, code: k, bubbles: true }));
const closed = () => vi.waitFor(() => expect(viewer()).toBeNull());

afterEach(() => {
  vi.restoreAllMocks();
  document.body.innerHTML = "";
});

describe("MessageImages", () => {
  const mountImages = (items: AttachmentRef[]) => mount(MessageImages, { props: { items }, attachTo: document.body });

  it("opens the clicked image in an in-page overlay, never a new tab", async () => {
    const open = vi.spyOn(window, "open").mockImplementation(() => null);
    const w = mountImages([img("a1"), img("a2"), img("a3")]);
    expect(w.html()).not.toContain('target="_blank"');
    expect(w.find("a").exists()).toBe(false);
    expect(viewer()).toBeNull();
    await w.findAll("button")[1]!.trigger("click");
    await flushPromises();
    expect(viewer()).not.toBeNull();
    expect(shown()).toBe(attachmentUrl("a2"));
    expect(progress()).toBe("2 / 3");
    expect(document.body.innerHTML).not.toContain('target="_blank"');
    expect(open).not.toHaveBeenCalled();
  });

  it("switches with ←/→ and the arrow buttons", async () => {
    const w = mountImages([img("a1"), img("a2"), img("a3")]);
    await w.findAll("button")[0]!.trigger("click");
    await flushPromises();
    key("ArrowRight");
    await flushPromises();
    expect(shown()).toBe(attachmentUrl("a2"));
    expect(progress()).toBe("2 / 3");
    document.body.querySelector<HTMLElement>(".el-image-viewer__next")!.click();
    await flushPromises();
    expect(shown()).toBe(attachmentUrl("a3"));
    key("ArrowLeft");
    await flushPromises();
    expect(shown()).toBe(attachmentUrl("a2"));
    document.body.querySelector<HTMLElement>(".el-image-viewer__prev")!.click();
    await flushPromises();
    expect(shown()).toBe(attachmentUrl("a1"));
  });

  it("closes on Esc and on a mask click, and opens again afterwards", async () => {
    const w = mountImages([img("a1")]);
    await w.get("button").trigger("click");
    await flushPromises();
    expect(viewer()).not.toBeNull();
    key("Escape");
    await closed();
    await w.get("button").trigger("click");
    await flushPromises();
    expect(shown()).toBe(attachmentUrl("a1"));
    document.body.querySelector<HTMLElement>(".el-image-viewer__mask")!.click();
    await closed();
  });

  it("shows PDFs and other files as cards with a download link", () => {
    const w = mountImages([img("a1"), { id: "f1", name: "r.pdf", content_type: "application/pdf", size: 1 }]);
    expect(w.findAll("button")).toHaveLength(1);
    expect(w.findAll("[data-attachment-card]")).toHaveLength(1);
    expect(w.get("a").attributes("href")).toBe(`${attachmentUrl("f1")}?download=1`);
  });
});

describe("AttachmentStrip", () => {
  const strip = (list: PendingImage[]) => {
    const items = ref(list);
    const remove = vi.fn((k: string) => (items.value = items.value.filter((x) => x.key !== k)));
    const a = { items, remove, add: vi.fn(), clear: vi.fn(), ids: () => null, uploading: ref(false) } as unknown as Attachments;
    const w = mount(defineComponent({ setup: () => () => h(AttachmentStrip, { a }) }), { attachTo: document.body });
    return { w, remove };
  };
  const list: PendingImage[] = [
    { key: "k1", name: "one.png", kind: "image", preview: "blob:one", id: "i1" },
    { key: "k2", name: "doc.pdf", kind: "pdf", preview: "", id: "i2" },
    { key: "k3", name: "two.png", kind: "image", preview: "blob:two" },
  ];

  it("previews the image attachments, uploading ones included, starting at the clicked one", async () => {
    const { w } = strip(list);
    await w.get('button[aria-label="查看 two.png"]').trigger("click");
    await flushPromises();
    expect(shown()).toBe("blob:two");
    expect(progress()).toBe("2 / 2");
    key("ArrowLeft");
    await flushPromises();
    expect(shown()).toBe("blob:one");
    key("Escape");
    await closed();
  });

  it("removes without opening the preview", async () => {
    const { w, remove } = strip(list);
    await w.get('button[aria-label="移除 one.png"]').trigger("click");
    await flushPromises();
    expect(remove).toHaveBeenCalledWith("k1");
    expect(viewer()).toBeNull();
    expect(w.find('button[aria-label="查看 one.png"]').exists()).toBe(false);
  });
});
