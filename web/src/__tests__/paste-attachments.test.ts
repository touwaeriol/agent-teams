// @vitest-environment jsdom
// Pasting into a message box: text stays text, only real files become attachments.
import { describe, expect, it, vi } from "vitest";
import { attachmentHandlers, type Attachments } from "@/components/app/attachments";
import { pastedFiles, type ClipboardLike } from "@/lib/images";

const png = (name = "image.png") => new File([new Uint8Array([137, 80, 78, 71])], name, { type: "image/png" });

function clip(data: Record<string, string>, files: File[] = []): ClipboardLike {
  return { files, getData: (t) => data[t] ?? "" };
}

function paste(cd: ClipboardLike) {
  const add = vi.fn();
  const a = { add } as unknown as Attachments;
  const e = new Event("paste", { cancelable: true }) as Event & { clipboardData: ClipboardLike };
  Object.defineProperty(e, "clipboardData", { value: cd });
  attachmentHandlers(a, () => {}).paste!(e);
  return { add, prevented: e.defaultPrevented };
}

const ID = "dl_01m88nq9y1jrk26hq5amk1rtvs";

describe("paste into a message box", () => {
  it("plain text only: inserted as text", () => {
    const r = paste(clip({ "text/plain": ID }));
    expect(r.add).not.toHaveBeenCalled();
    expect(r.prevented).toBe(false);
  });

  it("rich selection (text/html + text/plain): inserted as text", () => {
    const r = paste(clip({ "text/plain": ID, "text/html": `<meta charset="utf-8"><code class="inline">${ID}</code>` }));
    expect(r.add).not.toHaveBeenCalled();
    expect(r.prevented).toBe(false);
  });

  it("rich selection that also carries a rendered picture of it: still text", () => {
    const r = paste(clip({ "text/plain": ID, "text/html": `<code>${ID}</code>` }, [png()]));
    expect(r.add).not.toHaveBeenCalled();
    expect(r.prevented).toBe(false);
  });

  it("a real image (screenshot): attached", () => {
    const f = png();
    const r = paste(clip({}, [f]));
    expect(r.add).toHaveBeenCalledWith([f]);
    expect(r.prevented).toBe(true);
  });

  it("a copied web image (<img> in the HTML): attached", () => {
    const f = png();
    const r = paste(clip({ "text/html": '<img src="https://x/a.png">', "text/plain": "https://x/a.png" }, [f]));
    expect(r.add).toHaveBeenCalledWith([f]);
  });

  it("a file copied in Finder/Explorer (text is its name or path): attached", () => {
    expect(pastedFiles(clip({ "text/plain": "shot.png" }, [png("shot.png")]))).toHaveLength(1);
    expect(pastedFiles(clip({ "text/plain": "C:\\Users\\me\\shot.png" }, [png("shot.png")]))).toHaveLength(1);
  });

  it("text next to a non-image file (PDF): the file is attached", () => {
    const pdf = new File(["%PDF"], "a.pdf", { type: "application/pdf" });
    expect(pastedFiles(clip({ "text/plain": "a.pdf report" }, [pdf]))).toEqual([pdf]);
  });

  it("no clipboard or unsupported files: nothing attached", () => {
    expect(pastedFiles(null)).toEqual([]);
    expect(pastedFiles(clip({}, [new File(["x"], "a.exe", { type: "application/octet-stream" })]))).toEqual([]);
  });
});
