import { execFileSync } from "node:child_process";
import { createHash } from "node:crypto";
import { existsSync, mkdirSync, mkdtempSync, readFileSync, readdirSync, statSync, utimesSync, writeFileSync } from "node:fs";
import { createServer, type IncomingMessage, type Server, type ServerResponse } from "node:http";
import type { AddressInfo } from "node:net";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { afterEach, describe, expect, it } from "vitest";
import { attachmentOptions } from "../src/config.js";
import type { AttachmentRef } from "../src/protocol.js";
import {
  ATTACH_DIR, AttachmentExpired, base64Length, DEFAULT_ATTACHMENT_OPTIONS, DIRECT_LIMITS, downloadAttachment, planDelivery, sanitizeName, sweepAttachments,
  type AttachmentOptions, type ConsoleHttp, type SavedAttachment,
} from "../src/sessions/attachments.js";
import { claudeContent, codexImages } from "../src/sessions/images.js";
import { promptBlocks } from "../src/sessions/antigravity/driver.js";
import { ToolBroker } from "../src/toolhost/broker.js";

const sha = (b: Buffer | string) => createHash("sha256").update(b).digest("hex");
const MB = 1024 * 1024;
const TOKEN = "node-token";
const opts: AttachmentOptions = { ...DEFAULT_ATTACHMENT_OPTIONS, minFreeBytes: 0, timeoutMs: 5_000 };

function workdir(): string {
  return mkdtempSync(join(tmpdir(), "at-att-"));
}

/** A console + storage stand-in: /nodes/attachments/{id}/url, /nodes/attachments/{id}, /s3/{id} (presigned). */
class FakeStore {
  server!: Server;
  base = "";
  files = new Map<string, Buffer>();
  /** id -> what /url answers: "presigned" (default), "null", a status code, or "missing-endpoint". */
  urlMode = new Map<string, "presigned" | "null" | number>();
  /** Presigned GETs answered with this status (one time each) before serving. */
  presignedFailures = new Map<string, number[]>();
  hang = new Set<string>();
  urlCalls: string[] = [];
  legacyCalls: { id: string; auth: string | undefined }[] = [];
  presignedCalls: { id: string; auth: string | undefined }[] = [];

  async start(): Promise<ConsoleHttp> {
    this.server = createServer((req, res) => void this.handle(req, res));
    await new Promise<void>((r) => this.server.listen(0, "127.0.0.1", r));
    this.base = `http://127.0.0.1:${(this.server.address() as AddressInfo).port}`;
    return { base: this.base, token: TOKEN, fetch };
  }

  stop(): Promise<void> {
    this.server.closeAllConnections();
    return new Promise((r) => this.server.close(() => r()));
  }

  private async handle(req: IncomingMessage, res: ServerResponse): Promise<void> {
    const url = req.url ?? "";
    const auth = req.headers.authorization;
    let m: RegExpExecArray | null;
    if ((m = /^\/nodes\/attachments\/([^/]+)\/url$/.exec(url))) {
      const id = decodeURIComponent(m[1]!);
      this.urlCalls.push(id);
      if (auth !== `Bearer ${TOKEN}`) return void res.writeHead(401).end();
      const mode = this.urlMode.get(id) ?? "presigned";
      if (typeof mode === "number") return void res.writeHead(mode).end();
      res.writeHead(200, { "Content-Type": "application/json" });
      return void res.end(JSON.stringify({ url: mode === "null" ? null : `${this.base}/s3/${id}?X-Amz-Signature=sig${this.urlCalls.length}`, expires_at: "2026-11-25T13:00:00Z" }));
    }
    if ((m = /^\/nodes\/attachments\/([^/?]+)$/.exec(url))) {
      const id = decodeURIComponent(m[1]!);
      this.legacyCalls.push({ id, auth });
      if (auth !== `Bearer ${TOKEN}`) return void res.writeHead(401).end();
      const b = this.files.get(id);
      return void (b ? res.writeHead(200).end(b) : res.writeHead(404).end());
    }
    if ((m = /^\/s3\/([^/?]+)/.exec(url))) {
      const id = decodeURIComponent(m[1]!);
      this.presignedCalls.push({ id, auth });
      if (this.hang.has(id)) return void res.writeHead(200, { "Content-Length": "100" }).write("x");
      const fails = this.presignedFailures.get(id);
      if (fails?.length) return void res.writeHead(fails.shift()!).end();
      const b = this.files.get(id);
      return void (b ? res.writeHead(200).end(b) : res.writeHead(404).end());
    }
    res.writeHead(404).end();
  }
}

function ref(id: string, body: Buffer, extra: Partial<AttachmentRef> = {}): AttachmentRef {
  return { id, name: "报告 v2.pdf", content_type: "application/pdf", size: body.length, sha256: sha(body), ...extra };
}

let store: FakeStore;
let http: ConsoleHttp;
afterEach(async () => {
  await store?.stop();
});

async function setup(): Promise<void> {
  store = new FakeStore();
  http = await store.start();
}

describe("attachment file names", () => {
  it("keeps letters, digits, . _ - and cuts at 120 bytes keeping the extension", () => {
    expect(sanitizeName("报告 v2.pdf")).toBe("报告_v2.pdf");
    expect(sanitizeName("../../etc/passwd")).toBe("__.._etc_passwd");
    expect(sanitizeName(".env")).toBe("_env");
    expect(sanitizeName("")).toBe("file");
    const long = sanitizeName(`${"文".repeat(100)}.tar.gz`);
    expect(Buffer.byteLength(long)).toBeLessThanOrEqual(120);
    expect(long.endsWith(".gz")).toBe(true);
  });
});

describe("downloading attachments", () => {
  it("streams the presigned URL into .attachments/ with a .gitignore, without the node token", async () => {
    await setup();
    const body = Buffer.from("%PDF-1.7 hello");
    store.files.set("img_a1", body);
    const cwd = workdir();
    const saved = await downloadAttachment(cwd, ref("img_a1", body), http, opts);
    expect(saved.rel).toBe(`${ATTACH_DIR}/img_a1-报告_v2.pdf`);
    expect(readFileSync(saved.abs)).toEqual(body);
    expect(readFileSync(join(cwd, ATTACH_DIR, ".gitignore"), "utf8")).toBe("*\n");
    expect(store.presignedCalls).toEqual([{ id: "img_a1", auth: undefined }]);
    expect(store.legacyCalls).toEqual([]);
    expect(readdirSync(join(cwd, ATTACH_DIR)).sort()).toEqual([".gitignore", "img_a1-报告_v2.pdf"]);
    // A complete copy is reused (no new download) and its mtime refreshed (retention from last use).
    utimesSync(saved.abs, new Date(0), new Date(0));
    const again = await downloadAttachment(cwd, ref("img_a1", body), http, opts);
    expect(again.reused).toBe(true);
    expect(store.presignedCalls.length).toBe(1);
    expect(statSync(saved.abs).mtimeMs).toBeGreaterThan(Date.now() - 60_000);
    // retentionFrom: download keeps the old mtime.
    utimesSync(saved.abs, new Date(0), new Date(0));
    await downloadAttachment(cwd, ref("img_a1", body), http, { ...opts, retentionFrom: "download" });
    expect(statSync(saved.abs).mtimeMs).toBe(0);
  });

  it("saves into the shared folder's attachments/ when given one", async () => {
    await setup();
    const body = Buffer.from("shared bytes");
    store.files.set("img_s1", body);
    const cwd = workdir();
    const into = workdir();
    const saved = await downloadAttachment(cwd, ref("img_s1", body), http, opts, undefined, undefined, into);
    expect(saved.abs).toBe(join(into, "img_s1-报告_v2.pdf"));
    expect(saved.rel).toBe(`${ATTACH_DIR}/img_s1-报告_v2.pdf`);
    expect(readFileSync(saved.abs)).toEqual(body);
    expect(existsSync(join(cwd, ATTACH_DIR))).toBe(false);
  });

  it("falls back to GET /nodes/attachments/{id} when the URL is null or the endpoint is missing", async () => {
    await setup();
    const body = Buffer.from("legacy bytes");
    store.files.set("img_old", body);
    store.urlMode.set("img_old", "null");
    store.files.set("img_404", body);
    store.urlMode.set("img_404", 404);
    const cwd = workdir();
    await downloadAttachment(cwd, ref("img_old", body), http, opts);
    await downloadAttachment(cwd, ref("img_404", body), http, opts);
    expect(store.legacyCalls).toEqual([{ id: "img_old", auth: `Bearer ${TOKEN}` }, { id: "img_404", auth: `Bearer ${TOKEN}` }]);
    expect(store.presignedCalls).toEqual([]);
    // read_attachment passes url null: straight to the legacy endpoint, no /url call.
    store.files.set("img_pre", body);
    await downloadAttachment(cwd, ref("img_pre", body), http, opts, null);
    expect(store.urlCalls).not.toContain("img_pre");
    expect(store.legacyCalls.at(-1)).toEqual({ id: "img_pre", auth: `Bearer ${TOKEN}` });
  });

  it("asks for a new URL once when the presigned one fails (expired), then tries the legacy endpoint", async () => {
    await setup();
    const body = Buffer.from("x".repeat(1000));
    store.files.set("img_exp", body);
    store.presignedFailures.set("img_exp", [403]);
    const cwd = workdir();
    await downloadAttachment(cwd, ref("img_exp", body), http, opts);
    expect(store.urlCalls).toEqual(["img_exp", "img_exp"]);
    expect(store.legacyCalls).toEqual([]);
    store.files.set("img_bad", body);
    store.presignedFailures.set("img_bad", [403, 403]);
    await downloadAttachment(cwd, ref("img_bad", body), http, opts);
    expect(store.legacyCalls.map((c) => c.id)).toEqual(["img_bad"]);
  });

  it("an expired attachment (410 from the console) fails at once with 已过期, no legacy fallback", async () => {
    await setup();
    const body = Buffer.from("gone");
    store.urlMode.set("img_gone", 410);
    const err = await downloadAttachment(workdir(), ref("img_gone", body), http, opts).catch((e: Error) => e);
    expect(err).toBeInstanceOf(AttachmentExpired);
    expect((err as Error).message).toMatch(/附件已过期/);
    expect(store.urlCalls).toEqual(["img_gone"]);
    expect(store.legacyCalls).toEqual([]);
    const d = await planDelivery("claude", [{ ref: ref("img_gone", body), error: (err as Error).message, expired: true }]);
    expect(d.note).toMatch(/img_gone .*：附件已过期（超过保留期没有再使用，文件已删除），请让用户重新上传/);
    expect(d.note).not.toMatch(/img_gone.*重试/);
  });

  it("refuses a body with the wrong checksum or size and leaves nothing behind", async () => {
    await setup();
    store.files.set("img_sum", Buffer.from("other bytes"));
    const cwd = workdir();
    await expect(downloadAttachment(cwd, ref("img_sum", Buffer.from("other bytez")), http, opts)).rejects.toThrow(/校验和不一致/);
    await expect(downloadAttachment(cwd, ref("img_sum", Buffer.from("short")), http, opts)).rejects.toThrow(/超过声明的大小/);
    // Without sha256 only the size is checked.
    await downloadAttachment(cwd, { id: "img_sum", name: "a.bin", content_type: "application/octet-stream", size: 11 }, http, opts);
    expect(readdirSync(join(cwd, ATTACH_DIR)).sort()).toEqual([".gitignore", "img_sum-a.bin"]);
  });

  it("gives up after the per-file timeout", async () => {
    await setup();
    store.hang.add("img_slow");
    store.urlMode.set("img_slow", "presigned");
    const cwd = workdir();
    const t0 = Date.now();
    await expect(downloadAttachment(cwd, { id: "img_slow", name: "a", content_type: "x/y", size: 100 }, http, { ...opts, timeoutMs: 300 })).rejects.toThrow(/超时|HTTP 404/);
    expect(Date.now() - t0).toBeLessThan(4_000);
    expect(readdirSync(join(cwd, ATTACH_DIR))).toEqual([".gitignore"]);
  });

  it("bounds the whole download, retries and fallback included, by one timeout", async () => {
    await setup();
    store.hang.add("img_total");
    const cwd = workdir();
    const t0 = Date.now();
    await expect(downloadAttachment(cwd, { id: "img_total", name: "a", content_type: "x/y", size: 100 }, http, { ...opts, timeoutMs: 400 })).rejects.toThrow(/下载超时（共 0 秒）|下载超时/);
    expect(Date.now() - t0).toBeLessThan(2_000);
    // The presigned GET used up the budget: no second URL and no legacy attempt.
    expect(store.urlCalls).toEqual(["img_total"]);
    expect(store.legacyCalls).toEqual([]);
  });

  it("stops when the session is closed (cancel signal)", async () => {
    await setup();
    store.hang.add("img_cancel");
    const ac = new AbortController();
    const p = downloadAttachment(workdir(), { id: "img_cancel", name: "a", content_type: "x/y", size: 100 }, http, opts, undefined, ac.signal);
    setTimeout(() => ac.abort(), 100);
    await expect(p).rejects.toThrow(/下载已取消/);
    expect(store.legacyCalls).toEqual([]);
  });

  it("checks the free disk space first", async () => {
    await setup();
    store.files.set("img_big", Buffer.from("abc"));
    await expect(downloadAttachment(workdir(), ref("img_big", Buffer.from("abc")), http, { ...opts, minFreeBytes: Number.MAX_SAFE_INTEGER })).rejects.toThrow(/磁盘空间不足/);
    expect(store.presignedCalls).toEqual([]);
  });

  it("is ignored by git, in a repository and when nested below one (b2)", async () => {
    await setup();
    const body = Buffer.from("data");
    store.files.set("img_git", body);
    const repo = workdir();
    execFileSync("git", ["init", "-q", repo]);
    await downloadAttachment(repo, ref("img_git", body), http, opts);
    const sub = join(repo, "sub");
    mkdirSync(sub);
    await downloadAttachment(sub, ref("img_git", body), http, opts);
    execFileSync("git", ["-C", repo, "add", "-A"]);
    expect(execFileSync("git", ["-C", repo, "status", "--porcelain"], { encoding: "utf8" })).toBe("");
  });
});

function savedFile(dir: string, id: string, contentType: string, size: number): SavedAttachment {
  const abs = join(dir, `${id}.bin`);
  execFileSync("truncate", ["-s", String(size), abs]);
  return { ref: { id, name: `${id}.bin`, content_type: contentType, size }, abs, rel: `${ATTACH_DIR}/${id}.bin`, reused: false };
}

describe("what the runtime gets directly", () => {
  const maxImage = (DIRECT_LIMITS.claude.imageBase64 / 4) * 3;

  it("Claude: images up to 5 MB of base64, PDFs up to 15 MB, the rest by path", async () => {
    const dir = workdir();
    expect(base64Length(maxImage)).toBe(5 * MB);
    const one = (id: string, type: string, size: number) => {
      const saved = savedFile(dir, id, type, size);
      return { ref: saved.ref, saved };
    };
    const outcomes = [
      one("img_p1", "image/png", maxImage),
      one("img_p2", "image/jpeg", maxImage + 1),
      one("img_d1", "application/pdf", 15 * MB),
      one("img_d2", "application/pdf", 15 * MB + 1),
      one("img_z", "application/zip", 150 * MB),
      one("img_t", "text/plain; charset=utf-8", 10),
      { ref: { id: "img_f", name: "lost.bin", content_type: "application/octet-stream", size: 5 }, error: "下载失败：HTTP 500" },
    ];
    const budget = { used: 0 };
    const d = await planDelivery("claude", outcomes, budget);
    expect(d.images.map((i) => [i.id, i.media_type, i.data?.length])).toEqual([["img_p1", "image/png", maxImage], ["img_d1", "application/pdf", 15 * MB]]);
    expect(budget.used).toBe(base64Length(15 * MB));
    expect(d.links).toEqual([]);
    expect(d.note).toContain("[附件]");
    expect(d.note).toMatch(/img_p1 .*→ .*img_p1\.bin（已作为图片附上）/);
    expect(d.note).toMatch(/img_p2 .*未直接附上（超出图片大小上限），请用工具读取路径/);
    expect(d.note).toMatch(/img_d1 .*已作为文档附上/);
    expect(d.note).toMatch(/img_d2 .*未直接附上（超出 Claude 文档上限），请用工具读取路径/);
    expect(d.note).toMatch(/img_z .*application\/zip，150\.0 MB.*只给路径/);
    expect(d.note).toMatch(/img_t .*只给路径/);
    expect(d.note).toMatch(/img_f lost\.bin.*下载失败（下载失败：HTTP 500）/);
    expect(d.note).toContain("read_attachment");
    const content = claudeContent("看附件", d.images) as Array<{ type: string }>;
    expect(content.map((b) => b.type)).toEqual(["image", "document", "text"]);
  });

  it("Claude: PDFs stop going in directly once the conversation holds 20 MB of PDF base64", async () => {
    const dir = workdir();
    const pdf = (id: string, size: number) => {
      const saved = savedFile(dir, id, "application/pdf", size);
      return { ref: saved.ref, saved };
    };
    const budget = { used: 0 };
    // Two 6 MB PDFs (8 MB base64 each) fit, the third does not; a small one after it still does.
    const d = await planDelivery("claude", [pdf("img_a", 6 * MB), pdf("img_b", 6 * MB), pdf("img_c", 6 * MB), pdf("img_s", MB)], budget);
    expect(d.images.map((i) => i.id)).toEqual(["img_a", "img_b", "img_s"]);
    expect(d.note).toMatch(/img_c .*超出 Claude 文档上限/);
    // The next message of the same conversation keeps counting.
    const next = await planDelivery("claude", [pdf("img_n", 3 * MB)], budget);
    expect(next.images).toEqual([]);
    expect(budget.used).toBe(base64Length(6 * MB) * 2 + base64Length(MB));
    // Images never count against it.
    expect((await planDelivery("claude", [{ ref: savedFile(dir, "img_i", "image/png", 1000).ref, saved: savedFile(dir, "img_i", "image/png", 1000) }], budget)).images.length).toBe(1);
  });

  it("Codex: every image as localImage (no size limit, not read), everything else by path", async () => {
    const dir = workdir();
    const big = savedFile(dir, "img_big", "image/png", 30 * MB);
    const pdf = savedFile(dir, "img_pdf", "application/pdf", MB);
    const d = await planDelivery("codex", [{ ref: big.ref, saved: big }, { ref: pdf.ref, saved: pdf }]);
    expect(d.images).toEqual([{ id: "img_big", media_type: "image/png", path: big.abs }]);
    expect(codexImages(d.images)).toEqual([{ type: "localImage", path: big.abs }]);
    expect(d.note).toMatch(/img_pdf .*只给路径/);
  });

  it("Antigravity: small images as image blocks, the rest as resource_link plus the path", async () => {
    const dir = workdir();
    const img = savedFile(dir, "img_i", "image/webp", 1000);
    const pdf = savedFile(dir, "img_pdf", "application/pdf", MB);
    const d = await planDelivery("antigravity", [{ ref: img.ref, saved: img }, { ref: pdf.ref, saved: pdf }]);
    expect(d.images.map((i) => i.id)).toEqual(["img_i"]);
    expect(d.links).toEqual([{ type: "resource_link", uri: `file://${pdf.abs}`, name: "img_pdf.bin", mimeType: "application/pdf", size: MB }]);
    const blocks = promptBlocks("hi" + d.note, d.images, d.links);
    expect(blocks.map((b) => b.type)).toEqual(["text", "image", "resource_link"]);
  });
});

describe("cleanup of local copies", () => {
  it("removes files older than the retention, keeps .gitignore and new files, forgets missing folders", async () => {
    const cwd = workdir();
    const dir = join(cwd, ATTACH_DIR);
    mkdirSync(dir);
    writeFileSync(join(dir, ".gitignore"), "*\n");
    writeFileSync(join(dir, "img_old-a"), "a");
    writeFileSync(join(dir, "img_new-b"), "b");
    const now = Date.now();
    utimesSync(join(dir, "img_old-a"), new Date(now - 25 * 3600_000), new Date(now - 25 * 3600_000));
    utimesSync(join(dir, ".gitignore"), new Date(0), new Date(0));
    const r = await sweepAttachments([dir, join(cwd, "gone", ATTACH_DIR)], 24 * 3600_000, now);
    expect(r).toEqual({ removed: 1, gone: [join(cwd, "gone", ATTACH_DIR)] });
    expect(readdirSync(dir).sort()).toEqual([".gitignore", "img_new-b"]);
    // A shorter retention removes the newer one too; 0 keeps everything.
    expect((await sweepAttachments([dir], 0, now + 3600_000)).removed).toBe(0);
    expect((await sweepAttachments([dir], 1000, now + 3600_000)).removed).toBe(1);
    expect(existsSync(join(dir, ".gitignore"))).toBe(true);
  });

  it("reads the retention and download settings from AGENT_TEAMS_ATTACHMENT_*", () => {
    const env: Record<string, string> = {
      AGENT_TEAMS_ATTACHMENT_RETENTION_H: "48", AGENT_TEAMS_ATTACHMENT_RETENTION_FROM: "download",
      AGENT_TEAMS_ATTACHMENT_TIMEOUT_S: "120", AGENT_TEAMS_ATTACHMENT_MIN_FREE_MB: "1024",
    };
    expect(attachmentOptions((k) => env[k])).toEqual({ retentionMs: 48 * 3600_000, retentionFrom: "download", timeoutMs: 120_000, minFreeBytes: 1024 * MB });
    expect(attachmentOptions(() => undefined)).toEqual({});
    expect(attachmentOptions((k) => ({ AGENT_TEAMS_ATTACHMENT_RETENTION_H: "0", AGENT_TEAMS_ATTACHMENT_RETENTION_FROM: "never" })[k])).toEqual({ retentionMs: 0 });
    expect(DEFAULT_ATTACHMENT_OPTIONS).toEqual({ timeoutMs: 600_000, minFreeBytes: 512 * MB, retentionMs: 24 * 3600_000, retentionFrom: "last_use" });
  });
});

describe("toolhost transfers", () => {
  function broker() {
    const sent: { type: string; body: Record<string, unknown> }[] = [];
    const dirs: string[] = [];
    const b = new ToolBroker((type, body) => sent.push({ type, body: body as Record<string, unknown> }), { http, attachments: () => opts, noteAttachmentDir: (d) => dirs.push(d) });
    return { b, sent, dirs };
  }

  async function waitCall(sent: { type: string; body: Record<string, unknown> }[]): Promise<Record<string, unknown>> {
    for (let i = 0; i < 200; i++) {
      const c = sent.find((s) => s.type === "tool.call");
      if (c) return c.body;
      await new Promise((r) => setTimeout(r, 10));
    }
    throw new Error("no tool.call");
  }

  it("read_attachment downloads into .attachments/ and returns the absolute path, never the URL", async () => {
    await setup();
    const body = Buffer.from("%PDF attachment");
    store.files.set("img_r1", body);
    const cwd = workdir();
    const { b, sent, dirs } = broker();
    const pending = b.call({ sessionId: "s_1", tool: "read_attachment", args: { id: "img_r1" }, cwd });
    const call = await waitCall(sent);
    const a = ref("img_r1", body, { name: "spec.pdf" });
    b.resolve({
      session_id: "s_1", call_id: String(call.call_id), ok: true, content: [{ type: "text", text: "server text" }],
      structured: { id: "img_r1", name: "spec.pdf", content_type: "application/pdf", size: body.length, local_path: "placeholder" },
      attachment_download: { attachment: a, url: `${store.base}/s3/img_r1?X-Amz-Signature=zzz`, expires_at: "2026-11-25T13:00:00Z" },
    });
    const r = await pending;
    const abs = join(cwd, ATTACH_DIR, "img_r1-spec.pdf");
    expect(r.isError).toBeUndefined();
    expect(r.structuredContent).toMatchObject({ id: "img_r1", local_path: abs, sha256: sha(body) });
    expect(JSON.stringify(r)).not.toContain("X-Amz-Signature");
    expect(readFileSync(abs)).toEqual(body);
    expect(dirs).toEqual([cwd]);
    expect(store.urlCalls).toEqual([]);

    // url null: the legacy endpoint; a failing download is an error result.
    store.files.set("img_r2", body);
    const p2 = b.call({ sessionId: "s_1", tool: "read_attachment", args: { id: "img_r2" }, cwd });
    const c2 = (await (async () => { for (;;) { const c = sent.filter((s) => s.type === "tool.call")[1]; if (c) return c.body; await new Promise((r) => setTimeout(r, 10)); } })());
    b.resolve({ session_id: "s_1", call_id: String(c2.call_id), ok: true, content: [], attachment_download: { attachment: ref("img_r2", body), url: null } });
    expect((await p2).isError).toBeUndefined();
    expect(store.legacyCalls.map((c) => c.id)).toEqual(["img_r2"]);
    const p3 = b.call({ sessionId: "s_1", tool: "read_attachment", args: { id: "img_r3" }, cwd });
    const c3 = (await (async () => { for (;;) { const c = sent.filter((s) => s.type === "tool.call")[2]; if (c) return c.body; await new Promise((r) => setTimeout(r, 10)); } })());
    b.resolve({ session_id: "s_1", call_id: String(c3.call_id), ok: true, content: [], attachment_download: { attachment: ref("img_r3", body), url: null } });
    const r3 = await p3;
    expect(r3.isError).toBe(true);
    expect(JSON.stringify(r3.content)).toContain("read_attachment");
  });
});
