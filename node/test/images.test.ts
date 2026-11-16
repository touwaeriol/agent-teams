import { mkdtempSync, readFileSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { describe, expect, it } from "vitest";
import { claudeContent, codexFileNote, codexImages, consoleBase, consoleImageFetcher, loadImages, MAX_IMAGE_BYTES } from "../src/sessions/images.js";

describe("image attachments", () => {
  it("derives the console base URL from the link URL", () => {
    expect(consoleBase("wss://agent.example.com/nodes/connect")).toBe("https://agent.example.com");
    expect(consoleBase("ws://127.0.0.1:8080/nodes/v2/connect")).toBe("http://127.0.0.1:8080");
    expect(consoleBase("wss://example.com/sub/nodes/connect?x=1")).toBe("https://example.com/sub");
  });

  it("fetches with the node token", async () => {
    const calls: Array<[string, RequestInit | undefined]> = [];
    const fake = (async (url: string, init?: RequestInit) => {
      calls.push([url, init]);
      return new Response(new Uint8Array([1, 2, 3]));
    }) as unknown as typeof fetch;
    const data = await consoleImageFetcher("wss://h/nodes/connect", "tok", fake)("img_a");
    expect([...data]).toEqual([1, 2, 3]);
    expect(calls[0]![0]).toBe("https://h/nodes/attachments/img_a");
    expect((calls[0]![1]!.headers as Record<string, string>).Authorization).toBe("Bearer tok");
  });

  it("writes images to files and builds runtime input", async () => {
    const dir = mkdtempSync(join(tmpdir(), "at-img-"));
    try {
      const imgs = await loadImages([{ id: "img_x", media_type: "image/jpeg" }], async () => Buffer.from("jpegdata"), dir);
      expect(readFileSync(imgs[0]!.path, "utf8")).toBe("jpegdata");
      expect(imgs[0]!.path.endsWith("img_x.jpg")).toBe(true);
      expect(claudeContent("看", imgs)).toEqual([
        { type: "image", source: { type: "base64", media_type: "image/jpeg", data: Buffer.from("jpegdata").toString("base64") } },
        { type: "text", text: "看" },
      ]);
      expect(claudeContent("", imgs)).toHaveLength(1);
      expect(claudeContent("plain", [])).toBe("plain");
      expect(codexImages(imgs)).toEqual([{ type: "localImage", path: imgs[0]!.path }]);
      await expect(loadImages([{ id: "img_big", media_type: "image/png" }], async () => Buffer.alloc(MAX_IMAGE_BYTES + 1), dir)).rejects.toThrow(/5 MB/);
    } finally {
      rmSync(dir, { recursive: true, force: true });
    }
  });

  it("sends a PDF as a document block, keeps a slash command's text first, and names PDFs for Codex", async () => {
    const dir = mkdtempSync(join(tmpdir(), "at-pdf-"));
    try {
      const files = await loadImages([{ id: "img_p", media_type: "application/pdf" }, { id: "img_q", media_type: "image/png" }], async () => Buffer.from("%PDF-1.4"), dir);
      expect(files[0]!.path.endsWith("img_p.pdf")).toBe(true);
      const content = claudeContent("/agent-teams-skills:design 按附件做", files) as Array<{ type: string }>;
      expect(content.map((b) => b.type)).toEqual(["text", "document", "image"]);
      expect((claudeContent("看附件", files) as Array<{ type: string }>).map((b) => b.type)).toEqual(["document", "image", "text"]);
      expect(codexImages(files)).toEqual([{ type: "localImage", path: files[1]!.path }]);
      expect(codexFileNote(files)).toContain(files[0]!.path);
      expect(codexFileNote([files[1]!])).toBe("");
    } finally {
      rmSync(dir, { recursive: true, force: true });
    }
  });
});
