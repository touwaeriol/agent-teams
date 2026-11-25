import type { ContentBlock, FileDownload, ToolCancel, ToolResult, ToolUpload } from "../protocol.js";
import { deferred, type Deferred } from "../util/async.js";
import { newId } from "../util/ids.js";
import { DownloadError, checkShared, sharedTarget, writeDownload, writeSharedDownload } from "./download.js";
import { MAX_BYTES, MAX_CHUNKED_SHARED_BYTES, MAX_FILES, SHARED_DIR, UploadError, prepareSharedUpload, prepareUpload, resolveSharedUpload, type PreparedUpload } from "./upload.js";
import { AttachmentError, DEFAULT_ATTACHMENT_OPTIONS, downloadAttachment, hashFile, streamDownload, type AttachmentOptions, type ConsoleHttp } from "../sessions/attachments.js";
import { putFile, requestUpload } from "./transfer.js";

export type SendFn = (type: string, body: object) => void;

export interface ToolCallRequest {
  sessionId: string;
  openGen?: number;
  tool: string;
  args: Record<string, unknown>;
  toolUseId?: string;
  /** Absolute working directory of the session (publish_file reads files from it). */
  cwd?: string;
}

/** Tools whose files the daemon uploads (file.chunk frames) before forwarding the call. */
export const UPLOAD_TOOLS = new Set(["publish_file", "write_shared_file"]);

/** Tools whose result is preceded by file.download frames the daemon writes into the working directory. */
const DOWNLOAD_TOOLS = new Set(["read_deliverable", "read_shared_file"]);

/** What the broker needs for presigned transfers (attachments_v2); without it every transfer uses frames. */
export interface BrokerDeps {
  http?: ConsoleHttp;
  attachments?: () => AttachmentOptions;
  /** A .attachments folder was written in this working directory (swept for old copies). */
  noteAttachmentDir?: (absCwd: string) => void;
}

export interface McpToolResult {
  [k: string]: unknown;
  content: ContentBlock[];
  isError?: boolean;
  structuredContent?: Record<string, unknown>;
}

interface Pending {
  sessionId: string;
  cwd?: string;
  tool: string;
  wait: Deferred<ToolResult>;
  /** read_deliverable / read_shared_file: file.download chunks (base64 by index) that arrived for this call. */
  chunks?: Map<number, string>;
  chunkBytes?: number;
}

/** Base64 a download may take in memory before its tool.result (4/3 of the size limit plus slack). */
const MAX_DOWNLOAD_B64 = Math.ceil((MAX_BYTES * 4) / 3) + 4 * MAX_FILES;
/** Most file.download chunks kept for one call. */
const MAX_DOWNLOAD_CHUNKS = 64;
const MAX_SHARED_B64 = Math.ceil((MAX_CHUNKED_SHARED_BYTES * 4) / 3) + 4 * MAX_DOWNLOAD_CHUNKS;

/**
 * Forwards platform tool calls to the console (protocol.md §6): tool.call (reliable,
 * call_id generated here, persisted by the outbox before sending) and waits — without
 * a timeout, blocking tools can take hours — for tool.result.
 */
export class ToolBroker {
  private readonly pending = new Map<string, Pending>();

  constructor(
    private readonly send: SendFn,
    private readonly deps: BrokerDeps = {},
  ) {}

  private get attachmentOptions(): AttachmentOptions {
    return this.deps.attachments?.() ?? DEFAULT_ATTACHMENT_OPTIONS;
  }

  async call(req: ToolCallRequest): Promise<McpToolResult> {
    const callId = newId("tc_");
    let upload: PreparedUpload | undefined;
    let direct: ToolUpload | undefined;
    if (UPLOAD_TOOLS.has(req.tool)) {
      try {
        if (req.tool === "write_shared_file") {
          direct = await this.directSharedUpload(req, callId);
          if (!direct) upload = await prepareSharedUpload(req.cwd ?? "", req.args.file, req.args.path);
        } else upload = await prepareUpload(req.cwd ?? "", req.args.path);
      } catch (err) {
        return { content: [{ type: "text", text: `${req.tool}: ${(err as Error).message}` }], isError: true };
      }
    }
    const wait = deferred<ToolResult>();
    this.pending.set(callId, { sessionId: req.sessionId, tool: req.tool, wait, ...(req.cwd ? { cwd: req.cwd } : {}) });
    if (upload) this.sendChunks(req, upload);
    const up = direct ?? upload?.upload;
    this.send("tool.call", {
      call_id: callId,
      session_id: req.sessionId,
      ...(req.openGen !== undefined ? { open_gen: req.openGen } : {}),
      tool: req.tool,
      args: req.args,
      ...(req.toolUseId ? { tool_use_id: req.toolUseId } : {}),
      ...(up ? { upload: up } : {}),
    });
    return toMcp(await wait.promise);
  }

  /**
   * write_shared_file through storage (attachments_v2): POST /nodes/uploads for a presigned PUT, then
   * the file is streamed there. Undefined when the console has no such endpoint (it then takes
   * file.chunk frames, at most 20 MB); throws when the file is refused or the upload fails.
   */
  private async directSharedUpload(req: ToolCallRequest, callId: string): Promise<ToolUpload | undefined> {
    const http = this.deps.http;
    if (!http) return undefined;
    const src = await resolveSharedUpload(req.cwd ?? "", req.args.file, req.args.path);
    const sha256 = await hashFile(src.real);
    const ticket = await requestUpload(http, { session_id: req.sessionId, call_id: callId, size: src.size, sha256, name: src.target, content_type: src.contentType });
    if (ticket === "unsupported") {
      if (src.size > MAX_CHUNKED_SHARED_BYTES) throw new UploadError(`the file is ${(src.size / 1048576).toFixed(1)} MB; this console takes at most ${MAX_CHUNKED_SHARED_BYTES / 1048576} MB per shared file until it is upgraded`);
      return undefined;
    }
    await putFile(ticket.url, src.real, src.size, ticket.headers ?? {}, this.attachmentOptions.timeoutMs);
    return { upload_id: ticket.upload_id, storage: "s3", chunks: 0, files: [{ path: src.target, size: src.size, sha256, content_type: src.contentType }] } as ToolUpload;
  }

  /** file.chunk frames go out (reliable, in order) before the tool.call that claims them. */
  private sendChunks(req: ToolCallRequest, up: PreparedUpload): void {
    up.chunks.forEach((data, index) =>
      this.send("file.chunk", {
        session_id: req.sessionId,
        ...(req.openGen !== undefined ? { open_gen: req.openGen } : {}),
        upload_id: up.upload.upload_id,
        index,
        data,
      }),
    );
  }

  /**
   * Keeps one file.download chunk until its tool.result arrives. Chunks of calls that are not
   * waiting (stale, another session) or past the size budget are dropped; the result then
   * reports the download as incomplete.
   */
  chunk(body: FileDownload): void {
    const p = this.pending.get(body.call_id);
    if (!p || p.sessionId !== body.session_id || !DOWNLOAD_TOOLS.has(p.tool)) return;
    p.chunks ??= new Map();
    if (p.chunks.has(body.index)) return;
    const bytes = (p.chunkBytes ?? 0) + body.data.length;
    if (bytes > (p.tool === "read_shared_file" ? MAX_SHARED_B64 : MAX_DOWNLOAD_B64) || p.chunks.size >= MAX_DOWNLOAD_CHUNKS) return;
    p.chunkBytes = bytes;
    p.chunks.set(body.index, body.data);
  }

  /** Handles tool.result; false when no call is waiting (stale). */
  resolve(body: ToolResult): boolean {
    const p = this.pending.get(body.call_id);
    if (!p || p.sessionId !== body.session_id) return false;
    this.pending.delete(body.call_id);
    if (p.tool === "read_shared_file") {
      if (body.ok && body.shared_download) void this.saveShared(p, body).then((r) => p.wait.resolve(r));
      else p.wait.resolve(withoutContent(body));
      return true;
    }
    if (p.tool === "read_attachment" && body.ok && body.attachment_download) {
      void this.saveAttachment(p, body).then((r) => p.wait.resolve(r));
      return true;
    }
    if (body.ok && body.download) {
      void this.saveDownload(p, body).then((r) => p.wait.resolve(r));
      return true;
    }
    p.wait.resolve(body);
    return true;
  }

  /** Writes read_deliverable's files into the working directory and adds where they are to the result. */
  private async saveDownload(p: Pending, body: ToolResult): Promise<ToolResult> {
    const dl = body.download!;
    try {
      const out = await writeDownload(p.cwd ?? "", dl, p.chunks ?? new Map());
      const note = `Saved ${out.files.length} file(s) to ${out.abs} (relative: ${out.rel}; git-ignored).`;
      const structured = body.structured ? { ...body.structured, abs_dir: out.abs } : body.structured;
      return { ...body, content: [{ type: "text", text: note }, ...body.content], ...(structured ? { structured } : {}) };
    } catch (err) {
      const msg = err instanceof DownloadError ? err.message : `could not write the files: ${(err as Error).message}`;
      return { session_id: body.session_id, call_id: body.call_id, ok: false, content: [{ type: "text", text: `read_deliverable: ${msg}` }] };
    }
  }

  /**
   * Writes read_shared_file's file to <cwd>/.shared/<path>. The result names the local copy (absolute
   * path), its size and sha256 and tells the employee to read it with the file tools; whatever the
   * console sent as text or content is dropped, so file contents never enter the tool result.
   */
  private async saveShared(p: Pending, body: ToolResult): Promise<ToolResult> {
    try {
      const out = body.shared_download!.files?.[0]?.url ? await this.fetchShared(p.cwd ?? "", body.shared_download!) : await writeSharedDownload(p.cwd ?? "", body.shared_download!, p.chunks ?? new Map());
      const path = body.shared_download!.files[0].path;
      const note =
        `Saved shared file ${path} to ${out.abs} (relative: ${out.rel}; git-ignored), ${out.size} bytes, sha256 ${out.sha256}. ` +
        `Read it with your file tools; to change it, edit that copy and call write_shared_file(file="${out.rel}").`;
      const { content: _drop, ...rest } = body.structured ?? {};
      const structured = { ...rest, path, size: out.size, sha256: out.sha256, local_path: out.abs };
      return { session_id: body.session_id, call_id: body.call_id, ok: true, content: [{ type: "text", text: note }], structured };
    } catch (err) {
      const msg = err instanceof DownloadError ? err.message : `could not write the file: ${(err as Error).message}`;
      return { session_id: body.session_id, call_id: body.call_id, ok: false, content: [{ type: "text", text: `read_shared_file: ${msg}` }] };
    }
  }

  /** read_shared_file with a presigned URL (chunks 0): streamed straight into .shared/<path>. */
  private async fetchShared(cwd: string, sd: NonNullable<ToolResult["shared_download"]>): Promise<{ abs: string; rel: string; size: number; sha256: string }> {
    const f = checkShared(sd);
    const t = await sharedTarget(cwd, f.path);
    try {
      await streamDownload({ url: f.url!, fetchFn: this.deps.http?.fetch ?? fetch, timeoutMs: this.attachmentOptions.timeoutMs, size: f.size, ...(f.sha256 ? { sha256: f.sha256 } : {}), dir: t.dir, tmpName: t.name, target: t.target });
    } catch (err) {
      throw new DownloadError((err as Error).message);
    }
    return { abs: t.target, rel: `${SHARED_DIR}/${f.path}`, size: f.size, sha256: f.sha256 || (await hashFile(t.target)) };
  }

  /**
   * read_attachment: downloads the attachment into <cwd>/.attachments/ (presigned URL, or the legacy
   * GET /nodes/attachments/{id} when url is null) and returns its absolute local path; the URL and
   * whatever the console put in content never reach the employee.
   */
  private async saveAttachment(p: Pending, body: ToolResult): Promise<ToolResult> {
    const ad = body.attachment_download!;
    const fail = (msg: string): ToolResult => ({ session_id: body.session_id, call_id: body.call_id, ok: false, content: [{ type: "text", text: `read_attachment: ${msg}` }] });
    if (!this.deps.http) return fail("this node cannot download attachments");
    try {
      const saved = await downloadAttachment(p.cwd ?? "", ad.attachment, this.deps.http, this.attachmentOptions, ad.url ?? null);
      if (p.cwd) this.deps.noteAttachmentDir?.(p.cwd);
      const a = ad.attachment;
      const note =
        `Saved attachment ${a.id} (${a.name}, ${a.content_type}, ${a.size} bytes) to ${saved.abs} (relative: ${saved.rel}; git-ignored)` +
        `${saved.reused ? ", reusing the existing copy" : ""}. Read it with your file or command-line tools.`;
      const structured = { ...(body.structured ?? {}), id: a.id, name: a.name, content_type: a.content_type, size: a.size, ...(a.sha256 ? { sha256: a.sha256 } : {}), local_path: saved.abs };
      return { session_id: body.session_id, call_id: body.call_id, ok: true, content: [{ type: "text", text: note }], structured };
    } catch (err) {
      return fail(err instanceof AttachmentError ? err.message : `could not write the file: ${(err as Error).message}`);
    }
  }

  cancelSession(sessionId: string, reason: ToolCancel["reason"]): void {
    for (const id of this.pendingFor(sessionId)) {
      const p = this.pending.get(id)!;
      this.pending.delete(id);
      this.send("tool.cancel", { call_id: id, session_id: sessionId, reason });
      p.wait.resolve({ session_id: sessionId, call_id: id, ok: false, content: [{ type: "text", text: `Tool call cancelled (${reason}).` }] });
    }
  }

  pendingFor(sessionId: string): string[] {
    return [...this.pending].filter(([, p]) => p.sessionId === sessionId).map(([id]) => id);
  }
}

/** read_shared_file results without a download never carry the file's content either. */
function withoutContent(body: ToolResult): ToolResult {
  if (!body.structured || !("content" in body.structured)) return body;
  const { content: _drop, ...structured } = body.structured;
  return { ...body, structured };
}

function toMcp(r: ToolResult): McpToolResult {
  const out: McpToolResult = { content: r.content.length ? r.content : [{ type: "text", text: r.ok ? "ok" : "error" }] };
  if (!r.ok) out.isError = true;
  if (r.structured) out.structuredContent = r.structured;
  return out;
}
