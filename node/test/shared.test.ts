import { execFileSync } from "node:child_process";
import { createHash } from "node:crypto";
import { existsSync, lstatSync, mkdirSync, mkdtempSync, readdirSync, readFileSync, readlinkSync, symlinkSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { afterEach, describe, expect, it, vi } from "vitest";
import Ajv2020 from "ajv/dist/2020.js";
import addFormats from "ajv-formats";
import bundle from "../../protocol/gen/bundle.schema.json" with { type: "json" };
import { ToolBroker, UPLOAD_TOOLS } from "../src/toolhost/broker.js";
import { DownloadError, writeSharedDownload } from "../src/toolhost/download.js";
import { CHUNK_BYTES, defaultSharedPath, MAX_SHARED_BYTES, prepareSharedUpload, SHARED_DIR, UploadError, validSharedPath } from "../src/toolhost/upload.js";
import type { ToolSharedDownload } from "../src/protocol.js";
import { claudeOpen, startHarness, type Harness } from "./helpers/daemon.js";

const sha = (s: Buffer | string) => createHash("sha256").update(s).digest("hex");
const ajv = new Ajv2020({ strict: false });
addFormats(ajv);
ajv.addSchema({ ...bundle, $id: "bundle" });
const schemaOf: Record<string, string> = { "file.chunk": "FileChunk", "tool.call": "ToolCall" };
function validateBody(type: string, body: unknown): string | null {
  const v = ajv.getSchema(`bundle#/$defs/${schemaOf[type]}`)!;
  return v(body) ? null : JSON.stringify(v.errors);
}

function workdir(): string {
  return mkdtempSync(join(tmpdir(), "at-shared-"));
}

/** A shared download of one file split into chunks of `size` bytes. */
function shared(path: string, body: string | Buffer, size = CHUNK_BYTES) {
  const buf = Buffer.from(body);
  const chunks = new Map<number, string>();
  for (let off = 0, i = 0; off < buf.length; off += size, i++) chunks.set(i, buf.subarray(off, off + size).toString("base64"));
  const sd: ToolSharedDownload = { download_id: "dn_0123456789abcdef", chunks: chunks.size, files: [{ path, size: buf.length, sha256: sha(buf) }] };
  return { sd, chunks };
}

describe("shared paths", () => {
  it("follows the SharedPath contract", () => {
    for (const ok of ["a", "data/sample.csv", "报告/2026 Q4.md", "a/b/c/d/e/f/g/h", "x..y", "run.sh", "x".repeat(255), "文".repeat(85)]) expect(validSharedPath(ok), ok).toBe(true);
    for (const bad of ["", "/a", "a/", "a//b", ".env", "a/.git/x", "..", "../x", "a/../b", "a\\b", "a\u0000b", "a\nb", "a\u0085b", "a\u009fb", "a\u2028b", "a\u2029b", " a", "a ", "dir /x", "a/b/c/d/e/f/g/h/i", "x".repeat(256), "文".repeat(86)]) {
      expect(validSharedPath(bad), JSON.stringify(bad)).toBe(false);
    }
  });

  it("derives the default target, dropping a leading .shared/", () => {
    expect(defaultSharedPath("data/a.csv")).toBe("data/a.csv");
    expect(defaultSharedPath(".shared/data/a.csv")).toBe("data/a.csv");
    expect(defaultSharedPath("x/.shared/a.csv")).toBe("x/.shared/a.csv");
  });
});

describe("write_shared_file upload", () => {
  it("is an upload tool", () => expect(UPLOAD_TOOLS.has("write_shared_file")).toBe(true));

  it("reads one file of any type; the default path is the normalised relative path", async () => {
    const dir = workdir();
    mkdirSync(join(dir, "data"));
    writeFileSync(join(dir, "data", "run.sh"), "#!/bin/sh\n");
    const up = await prepareSharedUpload(dir, "./data/../data/run.sh", undefined);
    expect(up.upload.files).toEqual([{ path: "data/run.sh", size: 10, sha256: sha("#!/bin/sh\n"), content_type: "application/octet-stream" }]);
    expect(Buffer.from(up.chunks.join(""), "base64").toString()).toBe("#!/bin/sh\n");
    expect(up.upload.upload_id).toMatch(/^up_/);
    // Absolute paths inside the working directory work too.
    expect((await prepareSharedUpload(dir, join(dir, "data", "run.sh"), undefined)).upload.files[0]!.path).toBe("data/run.sh");
  });

  it("strips .shared/ from the default path and honours an explicit path", async () => {
    const dir = workdir();
    mkdirSync(join(dir, SHARED_DIR, "docs"), { recursive: true });
    writeFileSync(join(dir, SHARED_DIR, "docs", "a.md"), "# A");
    expect((await prepareSharedUpload(dir, ".shared/docs/a.md", undefined)).upload.files[0]).toMatchObject({ path: "docs/a.md", content_type: "text/markdown; charset=utf-8" });
    expect((await prepareSharedUpload(dir, ".shared/docs/a.md", "other/b.md")).upload.files[0]!.path).toBe("other/b.md");
  });

  it("refuses directories, paths outside the workdir, symlink escapes, bad targets and missing files", async () => {
    const dir = workdir();
    const outside = workdir();
    writeFileSync(join(outside, "secret.txt"), "secret");
    symlinkSync(join(outside, "secret.txt"), join(dir, "link.txt"));
    symlinkSync(outside, join(dir, "linkdir"));
    mkdirSync(join(dir, "sub"));
    writeFileSync(join(dir, "sub", "ok.txt"), "ok");
    writeFileSync(join(dir, ".env"), "x");
    for (const f of ["sub", ".", "", "../x.txt", join(outside, "secret.txt"), "link.txt", "linkdir/secret.txt", "missing.txt", 42]) {
      await expect(prepareSharedUpload(dir, f, undefined), String(f)).rejects.toBeInstanceOf(UploadError);
    }
    // A hidden file needs an explicit target: ".env" is not a valid shared path.
    await expect(prepareSharedUpload(dir, ".env", undefined)).rejects.toThrow(/default shared path ".env".*pass path/);
    // So does anything under a hidden folder or with a name that ends in a space.
    mkdirSync(join(dir, "out", ".cache"), { recursive: true });
    writeFileSync(join(dir, "out", ".cache", "x"), "x");
    writeFileSync(join(dir, "trail.txt "), "x");
    await expect(prepareSharedUpload(dir, "out/.cache/x", undefined)).rejects.toThrow(/default shared path "out\/\.cache\/x".*pass path/);
    await expect(prepareSharedUpload(dir, "trail.txt ", undefined)).rejects.toThrow(/pass path/);
    expect((await prepareSharedUpload(dir, "out/.cache/x", "cache/x")).upload.files[0]!.path).toBe("cache/x");
    expect((await prepareSharedUpload(dir, ".env", "env.txt")).upload.files[0]!.path).toBe("env.txt");
    for (const target of ["../x", "/etc/x", "a/../b", ".git/x", "a\\b", "a ", " a/b", "x\u0085y", "x\u2029y", "文".repeat(86), 7]) {
      await expect(prepareSharedUpload(dir, "sub/ok.txt", target), String(target)).rejects.toThrow(/not a valid shared path/);
    }
    // A symlink that stays inside the working directory is fine and keeps its own name.
    symlinkSync(join(dir, "sub", "ok.txt"), join(dir, "inner.txt"));
    expect((await prepareSharedUpload(dir, "inner.txt", undefined)).upload.files[0]!.path).toBe("inner.txt");
  });

  it("refuses a FIFO instead of blocking on it", async () => {
    const dir = workdir();
    execFileSync("mkfifo", [join(dir, "pipe")]);
    await expect(prepareSharedUpload(dir, "pipe", undefined)).rejects.toThrow(/regular file/);
  });

  it("enforces 20 MB per file and chunks large files", async () => {
    const dir = workdir();
    writeFileSync(join(dir, "big.bin"), Buffer.alloc(MAX_SHARED_BYTES + 1));
    await expect(prepareSharedUpload(dir, "big.bin", undefined)).rejects.toThrow(/limit is 20 MB/);
    writeFileSync(join(dir, "mid.bin"), Buffer.alloc(CHUNK_BYTES * 6 + 3));
    const up = await prepareSharedUpload(dir, "mid.bin", undefined);
    expect(up.upload.chunks).toBe(7);
    expect(Buffer.from(up.chunks[6]!, "base64").length).toBe(3);
  });

  it("broker sends file.chunk frames and a tool.call with one upload file, or fails locally", async () => {
    const dir = workdir();
    mkdirSync(join(dir, SHARED_DIR));
    writeFileSync(join(dir, SHARED_DIR, "notes.txt"), "hello");
    const sent: { type: string; body: Record<string, unknown> }[] = [];
    const broker = new ToolBroker((type, body) => sent.push({ type, body: body as Record<string, unknown> }));
    const pending = broker.call({ sessionId: "s_1", openGen: 1, tool: "write_shared_file", args: { file: ".shared/notes.txt" }, cwd: dir });
    await vi.waitFor(() => expect(sent.map((s) => s.type)).toEqual(["file.chunk", "tool.call"]));
    for (const s of sent) expect(validateBody(s.type, s.body), s.type).toBeNull();
    const call = sent[1]!.body as { call_id: string; args: unknown; upload: { upload_id: string; files: { path: string }[] } };
    expect(call.args).toEqual({ file: ".shared/notes.txt" });
    expect(call.upload.upload_id).toBe(sent[0]!.body.upload_id);
    expect(call.upload.files.map((f) => f.path)).toEqual(["notes.txt"]);
    broker.resolve({ session_id: "s_1", call_id: call.call_id, ok: true, content: [{ type: "text", text: "saved" }] });
    expect((await pending).isError).toBeUndefined();

    const bad = await broker.call({ sessionId: "s_1", tool: "write_shared_file", args: { file: SHARED_DIR }, cwd: dir });
    expect(bad.isError).toBe(true);
    expect(bad.content[0]!.text).toMatch(/^write_shared_file: .*directory/);
    expect(sent).toHaveLength(2);
  });
});

describe("read_shared_file download", () => {
  it("writes the file to .shared/<path>, git-ignored, replacing an earlier copy", async () => {
    const dir = workdir();
    execFileSync("git", ["init", "-q", dir]);
    const { sd, chunks } = shared("data/sample.csv", "a,b\n1,2\n", 3);
    const out = await writeSharedDownload(dir, sd, chunks);
    expect(out.rel).toBe(".shared/data/sample.csv");
    expect(out.abs).toBe(join(dir, ".shared", "data", "sample.csv"));
    expect(readFileSync(out.abs, "utf8")).toBe("a,b\n1,2\n");
    expect(readFileSync(join(dir, ".shared", ".gitignore"), "utf8")).toBe("*\n");
    expect(execFileSync("git", ["-C", dir, "status", "--porcelain", "--untracked-files=all"], { encoding: "utf8" })).toBe("");

    const again = shared("data/sample.csv", "new");
    await writeSharedDownload(dir, again.sd, again.chunks);
    expect(readFileSync(out.abs, "utf8")).toBe("new");
    expect(readdirSync(join(dir, ".shared", "data"))).toEqual(["sample.csv"]);
  });

  it("refuses paths that would leave .shared and malformed downloads", async () => {
    const dir = workdir();
    for (const path of ["../x", "/etc/passwd", "a/../../x", "..", ".gitignore", "a/.hidden", "a\\..\\x", "a//b", "x\u0000y", "x\u0085y", "x\u009fy", "x\u2028y", "x\u2029y", " a", "a /b", "a/b ", "文".repeat(86)]) {
      const { sd, chunks } = shared("ok.txt", "a");
      sd.files[0].path = path;
      await expect(writeSharedDownload(dir, sd, chunks), path).rejects.toBeInstanceOf(DownloadError);
    }
    const two = shared("a.txt", "a");
    (two.sd.files as unknown as unknown[]).push({ path: "b.txt", size: 0, sha256: sha("") });
    await expect(writeSharedDownload(dir, two.sd, two.chunks)).rejects.toThrow(/exactly one/);
    const bad = shared("a.txt", "hello");
    bad.sd.files[0].sha256 = sha("other");
    await expect(writeSharedDownload(dir, bad.sd, bad.chunks)).rejects.toThrow(/checksum/);
    const missing = shared("a.txt", "hello world", 4);
    missing.chunks.delete(1);
    await expect(writeSharedDownload(dir, missing.sd, missing.chunks)).rejects.toThrow(/incomplete.*read_shared_file/);
    const huge = shared("a.bin", Buffer.alloc(MAX_SHARED_BYTES + 1));
    await expect(writeSharedDownload(dir, huge.sd, huge.chunks)).rejects.toThrow(/size|exceed/);
    expect(existsSync(join(dir, "x"))).toBe(false);
    expect(readdirSync(dir).filter((n) => n !== ".shared")).toEqual([]);
  });

  it("never follows symlinks out of the working directory", async () => {
    const outside = workdir();
    const { sd, chunks } = shared("a/b.txt", "x");
    const d1 = workdir();
    symlinkSync(outside, join(d1, ".shared"));
    await expect(writeSharedDownload(d1, sd, chunks)).rejects.toThrow(/not a directory/);
    const d2 = workdir();
    mkdirSync(join(d2, ".shared"));
    symlinkSync(outside, join(d2, ".shared", "a"));
    await expect(writeSharedDownload(d2, sd, chunks)).rejects.toThrow(/not a directory/);
    expect(readdirSync(outside)).toEqual([]);
    // A symlink at the file's own name is replaced, not written through.
    const d3 = workdir();
    mkdirSync(join(d3, ".shared", "a"), { recursive: true });
    writeFileSync(join(outside, "target.txt"), "keep");
    symlinkSync(join(outside, "target.txt"), join(d3, ".shared", "a", "b.txt"));
    await writeSharedDownload(d3, sd, chunks);
    expect(readFileSync(join(outside, "target.txt"), "utf8")).toBe("keep");
    expect(lstatSync(join(d3, ".shared", "a", "b.txt")).isSymbolicLink()).toBe(false);
    expect(readFileSync(join(d3, ".shared", "a", "b.txt"), "utf8")).toBe("x");
    expect(() => readlinkSync(join(d3, ".shared", "a", "b.txt"))).toThrow();
  });

  it("broker writes the file and returns only the local path, size and sha256 — never the content", async () => {
    const dir = workdir();
    const sent: { type: string; body: Record<string, unknown> }[] = [];
    const broker = new ToolBroker((type, body) => sent.push({ type, body: body as Record<string, unknown> }));
    const pending = broker.call({ sessionId: "s_1", tool: "read_shared_file", args: { path: "notes/plan.md" }, cwd: dir });
    await vi.waitFor(() => expect(sent).toHaveLength(1));
    const callId = sent[0]!.body.call_id as string;
    const secretish = "TOP-SECRET-BODY-42";
    const { sd, chunks } = shared("notes/plan.md", secretish, 5);
    for (const [index, data] of chunks) {
      broker.chunk({ session_id: "s_1", call_id: callId, download_id: sd.download_id, index, data });
      broker.chunk({ session_id: "s_other", call_id: callId, download_id: sd.download_id, index, data: "AAAA" });
    }
    broker.resolve({
      session_id: "s_1", call_id: callId, ok: true, content: [{ type: "text", text: `content: ${secretish}` }], shared_download: sd,
      structured: { path: "notes/plan.md", size: secretish.length, sha256: sd.files[0].sha256, local_path: ".shared/notes/plan.md", content: secretish },
    });
    const res = await pending;
    const abs = join(dir, ".shared", "notes", "plan.md");
    expect(res.isError).toBeUndefined();
    expect(readFileSync(abs, "utf8")).toBe(secretish);
    expect(res.content).toHaveLength(1);
    expect(res.content[0]!.text).toContain(abs);
    expect(res.content[0]!.text).toContain(`${secretish.length} bytes`);
    expect(res.content[0]!.text).toContain(sd.files[0].sha256);
    expect(res.content[0]!.text).toMatch(/file tools/);
    expect(JSON.stringify(res)).not.toContain(secretish);
    expect(res.structuredContent).toEqual({ path: "notes/plan.md", size: secretish.length, sha256: sd.files[0].sha256, local_path: abs });

    // Missing chunks fail as incomplete; a result without a download still drops structured.content.
    const again = broker.call({ sessionId: "s_1", tool: "read_shared_file", args: { path: "notes/plan.md" }, cwd: workdir() });
    await vi.waitFor(() => expect(sent).toHaveLength(2));
    broker.resolve({ session_id: "s_1", call_id: sent[1]!.body.call_id as string, ok: true, content: [], shared_download: sd });
    expect((await again).content[0]!.text).toMatch(/incomplete/);
    const plain = broker.call({ sessionId: "s_1", tool: "read_shared_file", args: { path: "x" } });
    await vi.waitFor(() => expect(sent).toHaveLength(3));
    broker.resolve({ session_id: "s_1", call_id: sent[2]!.body.call_id as string, ok: true, content: [{ type: "text", text: "no workdir" }], structured: { path: "x", size: 1, sha256: "0", content: "y" } });
    expect((await plain).structuredContent).toEqual({ path: "x", size: 1, sha256: "0" });
  });
});

describe("daemon shared files", () => {
  let h: Harness;
  afterEach(async () => h?.close());

  it("routes file.download frames to the waiting read_shared_file call and writes into the session's cwd", async () => {
    h = await startHarness();
    const tools = [{ name: "read_shared_file", description: "Fetch", blocking: false, input_schema: { type: "object", required: ["path"], properties: { path: { type: "string" } } } }];
    h.console.command("session.open", claudeOpen("s_S", 1, { platform_tools: tools }));
    await h.console.waitType("session.opened");
    h.console.command("session.release", { session_id: "s_S", open_gen: 1 });
    h.console.command("session.send", { session_id: "s_S", msg_id: "m_s", text: 'use read_shared_file {"path":"data/a.csv"}' });
    const call = await h.console.waitType("tool.call");
    const { sd, chunks } = shared("data/a.csv", "x,y\n");
    h.console.command("file.download", { session_id: "s_S", call_id: call.body.call_id, download_id: sd.download_id, index: 0, data: chunks.get(0) });
    h.console.command("tool.result", { session_id: "s_S", call_id: call.body.call_id, ok: true, content: [{ type: "text", text: "fetched" }], shared_download: sd });
    const text = await h.console.waitType("session.event", (b) => b.kind === "text" && b.final === true);
    expect(text.body.text).toMatch(/^tool ok: Saved shared file data\/a\.csv/);
    expect(readFileSync(join(h.opts.root, "projects/p1/threads/12/s_S", ".shared", "data", "a.csv"), "utf8")).toBe("x,y\n");
  });
});
