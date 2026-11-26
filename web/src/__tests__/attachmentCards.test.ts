// @vitest-environment jsdom
// Attachment cards (#58): any file type as a card with size and download, "已过期" for attachments
// whose retention ran out (the list marks them; a failed thumbnail asks, 410), "存到项目文件", and the composer strip's progress bar and cancel.
import { flushPromises, mount } from "@vue/test-utils";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { defineComponent, h, ref } from "vue";
import ElementPlus from "element-plus";
import type { AttachmentRef } from "@/api/views";
import AttachmentStrip from "@/components/app/AttachmentStrip.vue";
import type { Attachments, PendingImage } from "@/components/app/attachments";
import MessageImages from "@/components/app/MessageImages.vue";
import { resetAttachmentProbes } from "@/lib/attachmentActions";

const MB = 1 << 20;
const zip: AttachmentRef = { id: "img_zip", name: "数据.zip", content_type: "application/zip", size: 150 * MB };
const pdf: AttachmentRef = { id: "img_pdf", name: "需求.pdf", content_type: "application/pdf", size: 18 * MB };

/** fetch for /api/attachments/{id}: 410 for the ids in `expired`, a 302 (opaque redirect) otherwise; records save-shared posts. */
function stubFetch(expired: string[] = [], save?: (body: { path: string; overwrite: boolean }) => Response) {
  const saves: { path: string; overwrite: boolean }[] = [];
  vi.spyOn(globalThis, "fetch").mockImplementation(async (input, init) => {
    const url = String(input instanceof Request ? input.url : input);
    if (url.endsWith("/save-shared")) {
      const raw = input instanceof Request ? await input.clone().text() : String(init?.body);
      const body = JSON.parse(raw) as { path: string; overwrite: boolean };
      saves.push(body);
      return save?.(body) ?? new Response(JSON.stringify({ path: body.path, size: 1, overwritten: false }), { status: 200, headers: { "Content-Type": "application/json" } });
    }
    const id = decodeURIComponent(url.split("/api/attachments/")[1] ?? "");
    if (expired.includes(id)) return new Response("attachment expired", { status: 410 });
    return new Response(null, { status: 200 });
  });
  return saves;
}

beforeEach(() => resetAttachmentProbes());
afterEach(() => {
  vi.restoreAllMocks();
  document.body.innerHTML = "";
});

const mountCards = (items: AttachmentRef[], canSave = false) => mount(MessageImages, { props: { items, canSave }, attachTo: document.body, global: { plugins: [ElementPlus] } });

describe("attachment cards", () => {
  it("shows a PDF and a zip as cards with type, size and a download link (never inline)", async () => {
    stubFetch();
    const w = mountCards([pdf, zip]);
    await flushPromises();
    const cards = w.findAll("[data-attachment-card]");
    expect(cards).toHaveLength(2);
    expect(cards[0]!.text()).toContain("需求.pdf");
    expect(cards[0]!.text()).toContain("PDF · 18 MB");
    expect(cards[1]!.text()).toContain("ZIP · 150 MB");
    expect(cards[1]!.get("a").attributes("href")).toBe("/api/attachments/img_zip?download=1");
    expect(w.find("[data-expired]").exists()).toBe(false);
  });

  it("shows an attachment the list marks expired as 已过期 without download or save, and probes nothing", async () => {
    const fetch = vi.spyOn(globalThis, "fetch");
    const w = mountCards([pdf, { ...zip, expired: true }], true);
    await flushPromises();
    expect(fetch).not.toHaveBeenCalled();
    const gone = w.get("[data-expired]");
    expect(gone.text()).toContain("数据.zip");
    expect(gone.text()).toContain("已过期");
    expect(gone.find("a").exists()).toBe(false);
    expect(gone.find("button").exists()).toBe(false);
    expect(w.findAll("[data-attachment-card]")[0]!.find("a").exists()).toBe(true);
  });

  it("a thumbnail that fails to load asks once whether it expired (410) and then becomes a card", async () => {
    stubFetch(["img_png"]);
    const w = mountCards([{ id: "img_png", name: "截图.png", content_type: "image/png", size: 10 }]);
    await flushPromises();
    await w.get("img").trigger("error");
    await flushPromises();
    expect(w.find("img").exists()).toBe(false);
    expect(w.get("[data-expired]").text()).toContain("截图.png");
  });

  it("an attachment the message already knows is expired needs no probe; an expired image becomes a card", async () => {
    const fetch = vi.spyOn(globalThis, "fetch");
    const w = mountCards([{ ...zip, expired: true }, { id: "img_png", name: "截图.png", content_type: "image/png", size: 10, expired: true }]);
    await flushPromises();
    expect(fetch).not.toHaveBeenCalled();
    expect(w.findAll("[data-expired]")).toHaveLength(2);
    expect(w.find("img").exists()).toBe(false);
  });

  it("存到项目文件 asks for the path, and asks again to overwrite after 409 file_exists", async () => {
    const saves = stubFetch([], (b) =>
      b.overwrite
        ? new Response(JSON.stringify({ path: b.path, size: 1, overwritten: true }), { status: 200, headers: { "Content-Type": "application/json" } })
        : new Response(JSON.stringify({ detail: "exists", code: "file_exists" }), { status: 409, headers: { "Content-Type": "application/json" } }),
    );
    const w = mountCards([zip], true);
    await flushPromises();
    await w.get("[aria-label='把 数据.zip 存到项目文件']").trigger("click");
    await flushPromises();
    const input = document.body.querySelector<HTMLInputElement>("input[aria-label=项目文件路径]")!;
    expect(input.value).toBe("数据.zip");
    input.value = "inputs/数据.zip";
    input.dispatchEvent(new Event("input"));
    const form = document.body.querySelector("form")!;
    form.dispatchEvent(new Event("submit"));
    await flushPromises();
    expect(document.body.textContent).toContain("项目文件里已经有 inputs/数据.zip");
    document.body.querySelector<HTMLInputElement>(".el-checkbox input")!.click();
    await flushPromises();
    form.dispatchEvent(new Event("submit"));
    await flushPromises();
    expect(saves).toEqual([{ path: "inputs/数据.zip", overwrite: false }, { path: "inputs/数据.zip", overwrite: true }]);
  });
});

describe("composer strip while uploading", () => {
  it("shows a progress bar and percentage per file, and its button cancels the upload", async () => {
    const items = ref<PendingImage[]>([
      { key: "k1", name: "数据.zip", kind: "file", preview: "", size: 150 * MB, progress: 0.42 },
      { key: "k2", name: "done.pdf", kind: "pdf", preview: "", size: MB, id: "img_done", progress: 1 },
    ]);
    const remove = vi.fn((k: string) => (items.value = items.value.filter((x) => x.key !== k)));
    const a = { items, remove, add: vi.fn(), clear: vi.fn(), ids: () => null, uploading: ref(true) } as unknown as Attachments;
    const w = mount(defineComponent({ setup: () => () => h(AttachmentStrip, { a }) }), { attachTo: document.body });
    const bars = w.findAll("[role=progressbar]");
    expect(bars).toHaveLength(1);
    expect(bars[0]!.attributes("aria-valuenow")).toBe("42");
    expect(w.text()).toContain("ZIP · 150 MB · 42%");
    await w.get("[aria-label='取消上传 数据.zip']").trigger("click");
    expect(remove).toHaveBeenCalledWith("k1");
    expect(w.find("[aria-label='移除 done.pdf']").exists()).toBe(true);
  });
});
