import { mkdirSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import type { ImageRef } from "../protocol.js";

/** What Claude accepts per image; the console enforces the same limit on upload. */
export const MAX_IMAGE_BYTES = 5 * 1024 * 1024;

const EXT: Record<ImageRef["media_type"], string> = { "image/png": ".png", "image/jpeg": ".jpg", "image/gif": ".gif", "image/webp": ".webp", "application/pdf": ".pdf" };

const PDF = "application/pdf";

/** An attached image downloaded for one message: bytes for Claude and agy, a file path for Codex (data is not read for Codex). */
export interface LoadedImage {
  id: string;
  media_type: ImageRef["media_type"];
  data?: Buffer;
  path: string;
}

/** The image's bytes (read from its file when they were not loaded). */
export function imageData(i: LoadedImage): Buffer {
  return i.data ?? readFileSync(i.path);
}

export type ImageFetcher = (id: string) => Promise<Buffer>;

/** http(s)://host from the daemon's ws(s)://host/nodes/connect URL. */
export function consoleBase(wsUrl: string): string {
  const u = new URL(wsUrl);
  u.protocol = u.protocol === "wss:" ? "https:" : "http:";
  u.pathname = u.pathname.replace(/\/nodes(\/v2)?\/connect\/?$/, "");
  u.search = "";
  return u.toString().replace(/\/$/, "");
}

/** Fetches attachments with GET /nodes/attachments/{id} and the node token. */
export function consoleImageFetcher(wsUrl: string, token: string, fetchFn: typeof fetch = fetch): ImageFetcher {
  const base = consoleBase(wsUrl);
  return async (id) => {
    const res = await fetchFn(`${base}/nodes/attachments/${encodeURIComponent(id)}`, { headers: { Authorization: `Bearer ${token}` } });
    if (!res.ok) throw new Error(`image ${id}: HTTP ${res.status}`);
    return Buffer.from(await res.arrayBuffer());
  };
}

/** Downloads the images of one message into dir (checked against the declared type and size). */
export async function loadImages(refs: ImageRef[] | undefined, fetcher: ImageFetcher, dir: string): Promise<LoadedImage[]> {
  if (!refs?.length) return [];
  mkdirSync(dir, { recursive: true, mode: 0o700 });
  const out: LoadedImage[] = [];
  for (const ref of refs) {
    const data = await fetcher(ref.id);
    if (data.length === 0 || data.length > MAX_IMAGE_BYTES) throw new Error(`attachment ${ref.id} is empty or over 5 MB`);
    const path = join(dir, `${ref.id.replace(/[^A-Za-z0-9_-]/g, "")}${EXT[ref.media_type]}`);
    writeFileSync(path, data, { mode: 0o600 });
    out.push({ id: ref.id, media_type: ref.media_type, data, path });
  }
  return out;
}

export function removeImages(dir: string): void {
  rmSync(dir, { recursive: true, force: true });
}

type ImageBlock = { type: "image"; source: { type: "base64"; media_type: Exclude<ImageRef["media_type"], typeof PDF>; data: string } };
type DocumentBlock = { type: "document"; source: { type: "base64"; media_type: typeof PDF; data: string } };
type TextBlock = { type: "text"; text: string };
type Block = ImageBlock | DocumentBlock | TextBlock;

function attachmentBlock(i: LoadedImage): ImageBlock | DocumentBlock {
  const data = imageData(i).toString("base64");
  if (i.media_type === PDF) return { type: "document", source: { type: "base64", media_type: PDF, data } };
  return { type: "image", source: { type: "base64", media_type: i.media_type, data } };
}

/**
 * The Claude user message content: the plain text, or attachment blocks (images, PDFs as
 * document blocks) followed by the text. A slash command ("/skill args") keeps its text first,
 * where the CLI looks for it.
 */
export function claudeContent(text: string, images: LoadedImage[] | undefined): string | Block[] {
  if (!images?.length) return text;
  const blocks: Block[] = images.map(attachmentBlock);
  if (!text) return blocks;
  return text.startsWith("/") ? [{ type: "text", text }, ...blocks] : [...blocks, { type: "text", text }];
}

/** Codex input items for the images (localImage reads the file itself); PDFs are left to codexFileNote. */
export function codexImages(images: LoadedImage[] | undefined): Array<{ type: "localImage"; path: string }> {
  return (images ?? []).filter((i) => i.media_type !== PDF).map((i) => ({ type: "localImage", path: i.path }));
}

/** For Codex, which has no document input: where the attached PDFs were saved ("" when none). */
export function codexFileNote(images: LoadedImage[] | undefined): string {
  const pdfs = (images ?? []).filter((i) => i.media_type === PDF);
  if (pdfs.length === 0) return "";
  return `\n\n[附件 PDF 已保存在本机，可以直接读取：${pdfs.map((p) => p.path).join("、")}]`;
}
