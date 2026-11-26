import type { ContentBlock, FileDownload, ToolCancel, ToolResult } from "../protocol.js";
import { deferred, type Deferred } from "../util/async.js";
import { newId } from "../util/ids.js";
import { DownloadError, writeDownload } from "./download.js";
import { MAX_BYTES, MAX_FILES, prepareUpload, type PreparedUpload } from "./upload.js";
import { AttachmentError, DEFAULT_ATTACHMENT_OPTIONS, downloadAttachment, type AttachmentOptions, type ConsoleHttp } from "../sessions/attachments.js";

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
export const UPLOAD_TOOLS = new Set(["publish_file"]);

/** Tools whose result is preceded by file.download frames the daemon writes into the working directory. */
const DOWNLOAD_TOOLS = new Set(["read_deliverable"]);

/** What the broker needs for attachment downloads (attachments_v2). */
export interface BrokerDeps {
  http?: ConsoleHttp;
  attachments?: () => AttachmentOptions;
  /** The shared folder's attachments/ for a working directory linked to it (null: the local .attachments). */
  attachmentDir?: (absCwd: string) => string | null;
  /** The shared-folder directories of a working directory linked to it (publish_file may read there). */
  sharedDirs?: (absCwd: string) => string[];
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
  /** read_deliverable: file.download chunks (base64 by index) that arrived for this call. */
  chunks?: Map<number, string>;
  chunkBytes?: number;
}

/** Base64 a download may take in memory before its tool.result (4/3 of the size limit plus slack). */
const MAX_DOWNLOAD_B64 = Math.ceil((MAX_BYTES * 4) / 3) + 4 * MAX_FILES;
/** Most file.download chunks kept for one call. */
const MAX_DOWNLOAD_CHUNKS = 64;

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
    if (UPLOAD_TOOLS.has(req.tool)) {
      try {
        upload = await prepareUpload(req.cwd ?? "", req.args.path, req.cwd ? (this.deps.sharedDirs?.(req.cwd) ?? []) : []);
      } catch (err) {
        return { content: [{ type: "text", text: `${req.tool}: ${(err as Error).message}` }], isError: true };
      }
    }
    const wait = deferred<ToolResult>();
    this.pending.set(callId, { sessionId: req.sessionId, tool: req.tool, wait, ...(req.cwd ? { cwd: req.cwd } : {}) });
    if (upload) this.sendChunks(req, upload);
    const up = upload?.upload;
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
    if (bytes > MAX_DOWNLOAD_B64 || p.chunks.size >= MAX_DOWNLOAD_CHUNKS) return;
    p.chunkBytes = bytes;
    p.chunks.set(body.index, body.data);
  }

  /** Handles tool.result; false when no call is waiting (stale). */
  resolve(body: ToolResult): boolean {
    const p = this.pending.get(body.call_id);
    if (!p || p.sessionId !== body.session_id) return false;
    this.pending.delete(body.call_id);
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
   * read_attachment: downloads the attachment into <cwd>/.attachments/ (presigned URL, or the legacy
   * GET /nodes/attachments/{id} when url is null) and returns its absolute local path; the URL and
   * whatever the console put in content never reach the employee.
   */
  private async saveAttachment(p: Pending, body: ToolResult): Promise<ToolResult> {
    const ad = body.attachment_download!;
    const fail = (msg: string): ToolResult => ({ session_id: body.session_id, call_id: body.call_id, ok: false, content: [{ type: "text", text: `read_attachment: ${msg}` }] });
    if (!this.deps.http) return fail("this node cannot download attachments");
    try {
      const into = p.cwd ? (this.deps.attachmentDir?.(p.cwd) ?? null) : null;
      const saved = await downloadAttachment(p.cwd ?? "", ad.attachment, this.deps.http, this.attachmentOptions, ad.url ?? null, undefined, into);
      if (p.cwd && !into) this.deps.noteAttachmentDir?.(p.cwd);
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

function toMcp(r: ToolResult): McpToolResult {
  const out: McpToolResult = { content: r.content.length ? r.content : [{ type: "text", text: r.ok ? "ok" : "error" }] };
  if (!r.ok) out.isError = true;
  if (r.structured) out.structuredContent = r.structured;
  return out;
}
