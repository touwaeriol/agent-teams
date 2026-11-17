import { execFileSync } from "node:child_process";
import { createHash } from "node:crypto";
import { existsSync, mkdirSync, mkdtempSync, readdirSync, readFileSync, symlinkSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { afterEach, describe, expect, it, vi } from "vitest";
import { ToolBroker } from "../src/toolhost/broker.js";
import { DOWNLOAD_DIR, DownloadError, writeDownload } from "../src/toolhost/download.js";
import { CHUNK_BYTES, MAX_BYTES } from "../src/toolhost/upload.js";
import type { ToolDownload } from "../src/protocol.js";
import { claudeOpen, startHarness, type Harness } from "./helpers/daemon.js";

const sha = (s: Buffer | string) => createHash("sha256").update(s).digest("hex");

/** A download of the given files (path → body) split into chunks of `size` bytes. */
function download(files: Record<string, string | Buffer>, size = CHUNK_BYTES, id = "dl_abc", version = 2) {
  const bodies = Object.entries(files).map(([path, body]) => ({ path, body: Buffer.from(body) }));
  const all = Buffer.concat(bodies.map((b) => b.body));
  const chunks = new Map<number, string>();
  for (let off = 0, i = 0; off < all.length; off += size, i++) chunks.set(i, all.subarray(off, off + size).toString("base64"));
  const dl: ToolDownload = {
    download_id: "dn_0123456789abcdef", deliverable_id: id, version, chunks: chunks.size,
    files: bodies.map((b) => ({ path: b.path, size: b.body.length, sha256: sha(b.body) })) as ToolDownload["files"],
  };
  return { dl, chunks };
}

function workdir(): string {
  return mkdtempSync(join(tmpdir(), "at-dl-"));
}

describe("read_deliverable download", () => {
  it("writes a site under .agent-deliverables/<id>/v<N>/ keeping its folders, git-ignored", async () => {
    const dir = workdir();
    execFileSync("git", ["init", "-q", dir]);
    const { dl, chunks } = download({ "index.html": "<title>A</title>", "assets/css/s.css": "body{}" }, 5);
    const out = await writeDownload(dir, dl, chunks);
    expect(out.rel).toBe(`${DOWNLOAD_DIR}/dl_abc/v2`);
    expect(readFileSync(join(out.abs, "index.html"), "utf8")).toBe("<title>A</title>");
    expect(readFileSync(join(out.abs, "assets", "css", "s.css"), "utf8")).toBe("body{}");
    expect(readFileSync(join(dir, DOWNLOAD_DIR, ".gitignore"), "utf8")).toContain("*");
    expect(execFileSync("git", ["-C", dir, "status", "--porcelain", "--untracked-files=all"], { encoding: "utf8" })).toBe("");
    execFileSync("git", ["-C", dir, "add", "-A"]);
    expect(execFileSync("git", ["-C", dir, "diff", "--cached", "--name-only"], { encoding: "utf8" })).toBe("");

    // Reading the version again replaces the folder: files of the earlier copy disappear.
    writeFileSync(join(out.abs, "stale.md"), "x");
    await writeDownload(dir, download({ "index.html": "<title>B</title>" }).dl, download({ "index.html": "<title>B</title>" }).chunks);
    expect(readdirSync(out.abs)).toEqual(["index.html"]);
  });

  it("refuses stored names that would leave the folder, hidden names, odd types and bad ids", async () => {
    const dir = workdir();
    for (const path of ["../x.html", "/etc/x.html", "a/../../x.html", ".git/config.txt", "a/.hidden.md", "run.sh", "a//b.md", "x\0.md"]) {
      const { dl, chunks } = download({ "ok.md": "a" });
      dl.files[0]!.path = path;
      await expect(writeDownload(dir, dl, chunks), path).rejects.toBeInstanceOf(DownloadError);
    }
    for (const id of ["../x", "dl_a/b", "x"]) {
      const { dl, chunks } = download({ "ok.md": "a" }, CHUNK_BYTES, id);
      await expect(writeDownload(dir, dl, chunks), id).rejects.toBeInstanceOf(DownloadError);
    }
    const { dl, chunks } = download({ "a.md": "a", "b.md": "b" });
    dl.files[1]!.path = "a.md";
    await expect(writeDownload(dir, dl, chunks)).rejects.toThrow(/file name/);
    expect(existsSync(join(dir, "x.html"))).toBe(false);
  });

  it("checks checksums, sizes, missing pieces and the size limit", async () => {
    const dir = workdir();
    const bad = download({ "a.md": "hello" });
    bad.dl.files[0]!.sha256 = sha("other");
    await expect(writeDownload(dir, bad.dl, bad.chunks)).rejects.toThrow(/checksum/);
    const short = download({ "a.md": "hello" });
    short.dl.files[0]!.size = 3;
    await expect(writeDownload(dir, short.dl, short.chunks)).rejects.toThrow(/sizes/);
    const missing = download({ "a.md": "hello world" }, 4);
    missing.chunks.delete(1);
    await expect(writeDownload(dir, missing.dl, missing.chunks)).rejects.toThrow(/incomplete/);
    const huge = download({ "a.png": Buffer.alloc(MAX_BYTES + 1) });
    await expect(writeDownload(dir, huge.dl, huge.chunks)).rejects.toThrow(/exceed/);
    expect(existsSync(join(dir, DOWNLOAD_DIR, "dl_abc", "v2", "a.md"))).toBe(false);
  });

  it("never follows a symlinked download folder out of the working directory", async () => {
    const dir = workdir();
    const outside = workdir();
    symlinkSync(outside, join(dir, DOWNLOAD_DIR));
    const { dl, chunks } = download({ "a.md": "x" });
    await expect(writeDownload(dir, dl, chunks)).rejects.toThrow(/not a directory/);
    expect(readdirSync(outside)).toEqual([]);

    const dir2 = workdir();
    mkdirSync(join(dir2, DOWNLOAD_DIR));
    symlinkSync(outside, join(dir2, DOWNLOAD_DIR, "dl_abc"));
    await expect(writeDownload(dir2, dl, chunks)).rejects.toThrow(/not a directory/);
    const dir3 = workdir();
    mkdirSync(join(dir3, DOWNLOAD_DIR));
    symlinkSync(join(outside, "gi"), join(dir3, DOWNLOAD_DIR, ".gitignore"));
    await expect(writeDownload(dir3, dl, chunks)).rejects.toThrow(/cannot write/);
    expect(readdirSync(outside)).toEqual([]);
  });

  it("broker keeps chunks of the waiting call only and writes them when the result arrives", async () => {
    const dir = workdir();
    const sent: { type: string; body: Record<string, unknown> }[] = [];
    const broker = new ToolBroker((type, body) => sent.push({ type, body: body as Record<string, unknown> }));
    const pending = broker.call({ sessionId: "s_1", tool: "read_deliverable", args: { deliverable_id: "dl_abc" }, cwd: dir });
    await vi.waitFor(() => expect(sent).toHaveLength(1));
    const callId = sent[0]!.body.call_id as string;
    const { dl, chunks } = download({ "index.html": "<p>hi</p>" }, 4);
    for (const [index, data] of chunks) {
      broker.chunk({ session_id: "s_1", call_id: callId, download_id: dl.download_id, index, data });
      broker.chunk({ session_id: "s_other", call_id: callId, download_id: dl.download_id, index, data: "AAAA" });
    }
    broker.chunk({ session_id: "s_1", call_id: "tc_stale", download_id: dl.download_id, index: 0, data: "AAAA" });
    broker.resolve({ session_id: "s_1", call_id: callId, ok: true, content: [{ type: "text", text: "fetched" }], download: dl, structured: { dir: dl.deliverable_id } });
    const res = await pending;
    expect(res.isError).toBeUndefined();
    expect(res.content[0]!.text).toContain(join(dir, DOWNLOAD_DIR, "dl_abc", "v2"));
    expect(res.content[1]!.text).toBe("fetched");
    expect(res.structuredContent?.abs_dir).toBe(join(dir, DOWNLOAD_DIR, "dl_abc", "v2"));
    expect(readFileSync(join(dir, DOWNLOAD_DIR, "dl_abc", "v2", "index.html"), "utf8")).toBe("<p>hi</p>");

    // A result whose chunks never arrived fails as incomplete instead of writing partial files.
    const again = broker.call({ sessionId: "s_1", tool: "read_deliverable", args: { deliverable_id: "dl_abc" }, cwd: workdir() });
    await vi.waitFor(() => expect(sent).toHaveLength(2));
    broker.resolve({ session_id: "s_1", call_id: sent[1]!.body.call_id as string, ok: true, content: [{ type: "text", text: "x" }], download: dl });
    const failed = await again;
    expect(failed.isError).toBe(true);
    expect(failed.content[0]!.text).toMatch(/incomplete/);
  });
});

describe("daemon file.download", () => {
  let h: Harness;
  afterEach(async () => h?.close());

  it("routes file.download frames to the waiting read_deliverable call and writes into the session's cwd", async () => {
    h = await startHarness();
    const tools = [{ name: "read_deliverable", description: "Fetch", blocking: false, input_schema: { type: "object", required: ["deliverable_id"], properties: { deliverable_id: { type: "string" } } } }];
    h.console.command("session.open", claudeOpen("s_D", 1, { platform_tools: tools }));
    await h.console.waitType("session.opened");
    h.console.command("session.release", { session_id: "s_D", open_gen: 1 });
    h.console.command("session.send", { session_id: "s_D", msg_id: "m_d", text: 'use read_deliverable {"deliverable_id":"dl_abc"}' });
    const call = await h.console.waitType("tool.call");
    const { dl, chunks } = download({ "spec.md": "# 规格" });
    h.console.command("file.download", { session_id: "s_D", call_id: call.body.call_id, download_id: dl.download_id, index: 0, data: chunks.get(0) });
    h.console.command("tool.result", { session_id: "s_D", call_id: call.body.call_id, ok: true, content: [{ type: "text", text: "fetched" }], download: dl });
    const text = await h.console.waitType("session.event", (b) => b.kind === "text" && b.final === true);
    expect(text.body.text).toMatch(/^tool ok: Saved 1 file\(s\)/);
    expect(readFileSync(join(h.opts.root, "projects/p1/threads/12/s_D", DOWNLOAD_DIR, "dl_abc", "v2", "spec.md"), "utf8")).toBe("# 规格");
  });
});
