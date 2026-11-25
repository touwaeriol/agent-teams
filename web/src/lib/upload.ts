// Direct multipart uploads (#58, api.md uploads): the console signs, the browser PUTs every part
// straight to storage. create → parts (presigned URLs, 1 hour) → PUT each slice of part_size bytes
// (3 at a time, each retried; the ETag response header is kept) → complete. Cancelling aborts the
// running PUTs and DELETEs the upload. The bytes never pass through the console.
import { client, unwrap } from "@/api/client";
import type { components } from "@/api/schema";

type S = components["schemas"];
export type UploadSession = S["UploadSession"];
export type UploadDone = S["UploadDone"];
export type UploadCreate = S["UploadCreate"];

/** The four calls of one upload flow; project uploads and the project-less one (new sessions) differ only in the path. */
export interface UploadApi {
  create: (body: UploadCreate) => Promise<UploadSession>;
  parts: (uploadId: string, numbers: number[]) => Promise<{ urls: { number: number; url: string }[] | null }>;
  complete: (uploadId: string, parts: { number: number; etag: string }[]) => Promise<UploadDone>;
  abort: (uploadId: string) => Promise<unknown>;
}

/** /api/projects/{pid}/uploads… */
export function projectUploads(pid: string): UploadApi {
  const path = { pid };
  return {
    create: (body) => unwrap(client.POST("/api/projects/{pid}/uploads", { params: { path }, body })),
    parts: (upload_id, numbers) => unwrap(client.POST("/api/projects/{pid}/uploads/{upload_id}/parts", { params: { path: { ...path, upload_id } }, body: { numbers } })),
    complete: (upload_id, parts) => unwrap(client.POST("/api/projects/{pid}/uploads/{upload_id}/complete", { params: { path: { ...path, upload_id } }, body: { parts } })),
    abort: (upload_id) => unwrap(client.DELETE("/api/projects/{pid}/uploads/{upload_id}", { params: { path: { ...path, upload_id } } })),
  };
}

/**
 * /api/uploads… (attachments for the first message of a new plain session, which has no project
 * yet; the console moves them into the session's project). Same bodies and answers as projectUploads.
 */
export function standaloneUploads(): UploadApi {
  const p = (upload_id: string) => ({ params: { path: { upload_id } } });
  return {
    create: (body) => unwrap(client.POST("/api/uploads", { body })),
    parts: (upload_id, numbers) => unwrap(client.POST("/api/uploads/{upload_id}/parts", { ...p(upload_id), body: { numbers } })),
    complete: (upload_id, parts) => unwrap(client.POST("/api/uploads/{upload_id}/complete", { ...p(upload_id), body: { parts } })),
    abort: (upload_id) => unwrap(client.DELETE("/api/uploads/{upload_id}", p(upload_id))),
  };
}

/** PUTs one slice; resolves with the ETag response header. Injectable for tests. */
export type PutPart = (url: string, body: Blob, onProgress: (loaded: number) => void, signal: AbortSignal) => Promise<string>;

export class PartError extends Error {
  constructor(
    public status: number,
    message: string,
  ) {
    super(message);
  }
}

/** XMLHttpRequest PUT (fetch reports no upload progress). The storage's CORS exposes ETag. */
export const xhrPut: PutPart = (url, body, onProgress, signal) =>
  new Promise((resolve, reject) => {
    const xhr = new XMLHttpRequest();
    xhr.open("PUT", url);
    xhr.upload.onprogress = (e) => onProgress(e.loaded);
    xhr.onload = () => {
      if (xhr.status < 200 || xhr.status >= 300) return reject(new PartError(xhr.status, `分片上传失败（HTTP ${xhr.status}）`));
      const etag = xhr.getResponseHeader("ETag");
      if (!etag) return reject(new PartError(0, "存储没有返回 ETag（检查存储的 CORS 是否暴露了 ETag 头）"));
      resolve(etag);
    };
    xhr.onerror = () => reject(new PartError(0, "网络错误，分片没有传上去"));
    xhr.onabort = () => reject(new UploadCancelled());
    signal.addEventListener("abort", () => xhr.abort(), { once: true });
    xhr.send(body);
  });

export class UploadCancelled extends Error {
  constructor() {
    super("已取消上传");
  }
}

export interface UploadOptions {
  kind: "attachment" | "shared";
  /** Shared files: the target path. */
  path?: string;
  overwrite?: boolean;
  /** Bytes sent so far (all parts), and the total. */
  onProgress?: (sent: number, total: number) => void;
  /** Called once the console accepted the upload (its id is known, so it can be cancelled). */
  onStarted?: (session: UploadSession) => void;
  signal?: AbortSignal;
  /** Parts in flight at once. */
  concurrency?: number;
  /** Attempts per part (a failed PUT is retried; 403 asks for a new URL first). */
  attempts?: number;
  put?: PutPart;
  /** Wait before a retry (tests: 0). */
  retryDelayMs?: number;
}

const PART_URL_BATCH = 100;

/**
 * Uploads one file with the multipart flow and returns the complete answer. Throws ApiError for the
 * console's refusals (503 storage_unavailable, 409 quota_exceeded …), PartError when a part keeps
 * failing, UploadCancelled when signal aborts (the upload is then deleted on the console).
 */
export async function uploadFile(file: Blob & { name?: string }, api: UploadApi, o: UploadOptions): Promise<UploadDone> {
  const signal = o.signal ?? new AbortController().signal;
  if (signal.aborted) throw new UploadCancelled();
  const release = await fileSlot(signal);
  try {
    return await uploadNow(file, api, { ...o, signal });
  } finally {
    release();
  }
}

/**
 * Files this tab uploads at once; the others wait before create. The console caps a user's uploads
 * in flight (429 too_many_uploads, core.MaxUploadsInFlight = 16), and a message takes up to 20 files.
 */
export const FILES_AT_ONCE = 4;
let filesActive = 0;
const fileQueue: (() => void)[] = [];

/** Waits for one of the FILES_AT_ONCE slots; resolves with its release (idempotent). Aborting while queued rejects with UploadCancelled. */
function fileSlot(signal: AbortSignal): Promise<() => void> {
  return new Promise((resolve, reject) => {
    const grant = () => {
      signal.removeEventListener("abort", onAbort);
      filesActive++;
      let released = false;
      resolve(() => {
        if (released) return;
        released = true;
        filesActive--;
        fileQueue.shift()?.();
      });
    };
    const onAbort = () => {
      const i = fileQueue.indexOf(grant);
      if (i >= 0) fileQueue.splice(i, 1);
      reject(new UploadCancelled());
    };
    if (filesActive < FILES_AT_ONCE) return grant();
    fileQueue.push(grant);
    signal.addEventListener("abort", onAbort, { once: true });
  });
}

async function uploadNow(file: Blob & { name?: string }, api: UploadApi, o: UploadOptions): Promise<UploadDone> {
  const put = o.put ?? xhrPut;
  const attempts = o.attempts ?? 3;
  const delay = o.retryDelayMs ?? 800;
  const signal = o.signal ?? new AbortController().signal;
  if (signal.aborted) throw new UploadCancelled();
  const session = await api.create({
    kind: o.kind, name: file.name || "file", size: file.size,
    ...(o.path !== undefined ? { path: o.path } : {}), ...(o.overwrite !== undefined ? { overwrite: o.overwrite } : {}),
  });
  o.onStarted?.(session);
  const id = session.upload_id;
  // Stops the other parts once one fails for good (or the user cancels).
  const inner = new AbortController();
  const onCancel = () => inner.abort();
  signal.addEventListener("abort", onCancel, { once: true });
  // Cancelled while create was in flight: the listener above never fires, so stop now.
  if (signal.aborted) inner.abort();
  const abortUpload = () => void api.abort(id).catch(() => undefined);
  try {
    if (inner.signal.aborted) throw new UploadCancelled();
    const partSize = session.part_size;
    const count = Math.max(1, session.parts || Math.ceil(file.size / partSize));
    const loaded = new Array<number>(count + 1).fill(0);
    const report = () => o.onProgress?.(loaded.reduce((a, b) => a + b, 0), file.size);
    report();
    const urls = new Map<number, string>();
    const fetchUrls = async (numbers: number[]) => {
      for (let i = 0; i < numbers.length; i += PART_URL_BATCH) {
        const res = await api.parts(id, numbers.slice(i, i + PART_URL_BATCH));
        for (const u of res.urls ?? []) urls.set(u.number, u.url);
      }
    };
    await fetchUrls(Array.from({ length: count }, (_, i) => i + 1));
    if (inner.signal.aborted) throw new UploadCancelled();
    const etags = new Map<number, string>();
    const sendPart = async (n: number) => {
      const start = (n - 1) * partSize;
      const end = Math.min(file.size, n * partSize);
      const slice = file.slice(start, end);
      for (let attempt = 1; ; attempt++) {
        if (inner.signal.aborted) throw new UploadCancelled();
        try {
          const url = urls.get(n);
          if (!url) throw new PartError(0, `没有拿到第 ${n} 片的上传地址`);
          etags.set(n, await put(url, slice, (l) => ((loaded[n] = Math.min(l, end - start)), report()), inner.signal));
          loaded[n] = end - start;
          report();
          return;
        } catch (e) {
          if (e instanceof UploadCancelled || inner.signal.aborted) throw new UploadCancelled();
          loaded[n] = 0;
          report();
          if (attempt >= attempts) {
            inner.abort();
            throw e;
          }
          // An expired or refused URL: ask for a fresh one before trying again.
          if (e instanceof PartError && (e.status === 403 || e.status === 400)) await fetchUrls([n]);
          if (delay) await new Promise((r) => setTimeout(r, delay * attempt));
        }
      }
    };
    let next = 1;
    let failure: unknown;
    const worker = async () => {
      while (next <= count && !inner.signal.aborted) {
        try {
          await sendPart(next++);
        } catch (e) {
          // The first real failure wins over the cancellations it causes in the other workers.
          if (failure === undefined || (failure instanceof UploadCancelled && !(e instanceof UploadCancelled))) failure = e;
          inner.abort();
        }
      }
    };
    await Promise.all(Array.from({ length: Math.min(o.concurrency ?? 3, count) }, worker));
    if (failure !== undefined) throw failure;
    if (signal.aborted) throw new UploadCancelled();
    const parts = [...etags].sort((a, b) => a[0] - b[0]).map(([number, etag]) => ({ number, etag }));
    return await api.complete(id, parts);
  } catch (e) {
    abortUpload();
    if (signal.aborted) throw new UploadCancelled();
    throw e;
  } finally {
    signal.removeEventListener("abort", onCancel);
  }
}

/** True for the console's "no object storage configured" answer (fall back to the old upload). */
export function storageUnavailable(e: unknown): boolean {
  const x = e as { status?: number; code?: string };
  return x?.status === 503 && (x.code === "storage_unavailable" || !x.code);
}
