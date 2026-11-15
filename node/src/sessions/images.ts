import { mkdirSync, rmSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import type { ImageRef } from "../protocol.js";

/** What Claude accepts per image; the console enforces the same limit on upload. */
export const MAX_IMAGE_BYTES = 5 * 1024 * 1024;

const EXT: Record<ImageRef["media_type"], string> = { "image/png": ".png", "image/jpeg": ".jpg", "image/gif": ".gif", "image/webp": ".webp" };

/** An attached image downloaded for one message: bytes for Claude, a file path for Codex. */
export interface LoadedImage {
  id: string;
  media_type: ImageRef["media_type"];
  data: Buffer;
  path: string;
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
    if (data.length === 0 || data.length > MAX_IMAGE_BYTES) throw new Error(`image ${ref.id} is empty or over 5 MB`);
    const path = join(dir, `${ref.id.replace(/[^A-Za-z0-9_-]/g, "")}${EXT[ref.media_type]}`);
    writeFileSync(path, data, { mode: 0o600 });
    out.push({ id: ref.id, media_type: ref.media_type, data, path });
  }
  return out;
}

export function removeImages(dir: string): void {
  rmSync(dir, { recursive: true, force: true });
}

type ImageBlock = { type: "image"; source: { type: "base64"; media_type: ImageRef["media_type"]; data: string } };
type TextBlock = { type: "text"; text: string };

/** The Claude user message content: the plain text, or image blocks followed by the text. */
export function claudeContent(text: string, images: LoadedImage[] | undefined): string | Array<ImageBlock | TextBlock> {
  if (!images?.length) return text;
  const blocks: Array<ImageBlock | TextBlock> = images.map((i) => ({ type: "image", source: { type: "base64", media_type: i.media_type, data: i.data.toString("base64") } }));
  if (text) blocks.push({ type: "text", text });
  return blocks;
}

/** Codex input items for the images (localImage reads the file itself). */
export function codexImages(images: LoadedImage[] | undefined): Array<{ type: "localImage"; path: string }> {
  return (images ?? []).map((i) => ({ type: "localImage", path: i.path }));
}
