// Presigned uploads for write_shared_file (attachments_v2, #58): POST /nodes/uploads (node token)
// hands out a single presigned PUT URL; the file is streamed there with its Content-Length (S3
// refuses chunked uploads). The URL is only used inside the daemon, never shown to the employee.
import { createReadStream } from "node:fs";
import { request as httpRequest } from "node:http";
import { request as httpsRequest } from "node:https";
import { pipeline } from "node:stream/promises";
import type { ConsoleHttp } from "../sessions/attachments.js";
import { UploadError } from "./upload.js";

const UPLOAD_ID = /^up_[0-9A-Za-z]{10,40}$/;

export interface UploadTicket {
  upload_id: string;
  url: string;
  headers?: Record<string, string>;
  expires_at?: string;
}

export interface UploadRequest {
  session_id: string;
  call_id: string;
  size: number;
  sha256: string;
  /** Target shared path. */
  name: string;
  content_type: string;
}

/**
 * POST /nodes/uploads. "unsupported" when the console has no such endpoint (404, 405, 501: an
 * older console, which takes file.chunk frames instead); other failures throw.
 */
export async function requestUpload(http: ConsoleHttp, body: UploadRequest, timeoutMs = 30_000): Promise<UploadTicket | "unsupported"> {
  const res = await http
    .fetch(`${http.base}/nodes/uploads`, {
      method: "POST", headers: { Authorization: `Bearer ${http.token}`, "Content-Type": "application/json" }, body: JSON.stringify(body),
      redirect: "error", signal: AbortSignal.timeout(timeoutMs),
    })
    .catch((err: Error) => {
      throw new UploadError(`could not reach the console for the upload: ${err.message}`);
    });
  if (res.status === 404 || res.status === 405 || res.status === 501) {
    await res.body?.cancel().catch(() => undefined);
    return "unsupported";
  }
  const text = await res.text().catch(() => "");
  if (!res.ok) throw new UploadError(`the console refused the upload (HTTP ${res.status})${detail(text)}`);
  let t: Partial<UploadTicket>;
  try {
    t = JSON.parse(text) as Partial<UploadTicket>;
  } catch {
    throw new UploadError("the console sent a bad upload answer");
  }
  if (typeof t.upload_id !== "string" || !UPLOAD_ID.test(t.upload_id) || typeof t.url !== "string" || !/^https?:\/\//.test(t.url)) {
    throw new UploadError("the console sent a bad upload answer");
  }
  const headers: Record<string, string> = {};
  for (const [k, v] of Object.entries(t.headers ?? {})) if (typeof v === "string") headers[k] = v;
  return { upload_id: t.upload_id, url: t.url, headers, ...(t.expires_at ? { expires_at: t.expires_at } : {}) };
}

function detail(text: string): string {
  try {
    const j = JSON.parse(text) as { detail?: unknown; message?: unknown };
    const m = typeof j.detail === "string" ? j.detail : typeof j.message === "string" ? j.message : "";
    return m ? `: ${m.slice(0, 300)}` : "";
  } catch {
    return "";
  }
}

/** Streams the file to a presigned PUT URL with Content-Length; any non-2xx answer or timeout throws. */
export async function putFile(url: string, path: string, size: number, headers: Record<string, string>, timeoutMs: number): Promise<void> {
  const u = new URL(url);
  const req = (u.protocol === "https:" ? httpsRequest : httpRequest)(u, { method: "PUT", headers: { ...headers, "Content-Length": String(size) }, timeout: timeoutMs });
  const done = new Promise<void>((resolve, reject) => {
    req.on("response", (res) => {
      res.resume();
      res.on("end", () => (res.statusCode && res.statusCode >= 200 && res.statusCode < 300 ? resolve() : reject(new UploadError(`upload failed: HTTP ${res.statusCode}`))));
      res.on("error", reject);
    });
    req.on("timeout", () => req.destroy(new UploadError(`upload timed out (${Math.round(timeoutMs / 1000)} s)`)));
    req.on("error", (err) => reject(err instanceof UploadError ? err : new UploadError(`upload failed: ${err.message}`)));
  });
  done.catch(() => undefined);
  const timedOut = () => new UploadError(`upload timed out (${Math.round(timeoutMs / 1000)} s)`);
  let total: NodeJS.Timeout | undefined;
  // Whatever the socket does (no response, no error event after destroy), the call ends by the deadline.
  const expired = new Promise<never>((_, reject) => {
    total = setTimeout(() => {
      req.destroy(timedOut());
      reject(timedOut());
    }, timeoutMs);
  });
  expired.catch(() => undefined);
  try {
    await Promise.race([
      pipeline(createReadStream(path), req).catch(async (err: Error) => {
        req.destroy();
        // The answer (403, 400 …) explains more than the broken pipe it caused.
        await Promise.race([done, expired]);
        throw err instanceof UploadError ? err : new UploadError(`upload failed: ${err.message}`);
      }),
      expired,
    ]);
    await Promise.race([done, expired]);
  } finally {
    clearTimeout(total);
  }
}
