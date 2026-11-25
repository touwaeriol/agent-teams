// Actions on a sent attachment (#58): saving it as a shared file and finding out whether it expired.
// POST /api/attachments/{id}/save-shared is the contract agreed with the server (q10); until it is
// in the generated schema it is called with fetch.
import { csrfToken, CSRF_HEADER, toApiError } from "@/api/client";
import type { components } from "@/api/schema";
import { attachmentUrl } from "./images";

export type SavedSharedFile = components["schemas"]["UploadedSharedFile"];

/** Copies the attachment into the project's shared files at path (409 file_exists unless overwrite, 409 quota_exceeded, 410 attachment_expired). */
export async function saveAttachmentShared(id: string, path: string, overwrite = false): Promise<SavedSharedFile> {
  const token = csrfToken();
  const res = await globalThis.fetch(`${attachmentUrl(id)}/save-shared`, {
    method: "POST", credentials: "same-origin",
    headers: { "Content-Type": "application/json", ...(token ? { [CSRF_HEADER]: token } : {}) },
    body: JSON.stringify({ path, overwrite }),
  });
  const text = await res.text();
  let data: unknown;
  try {
    data = text ? JSON.parse(text) : undefined;
  } catch {
    data = text;
  }
  if (!res.ok) throw toApiError(res.status, data, res.statusText);
  return data as SavedSharedFile;
}

export type AttachmentState = "ok" | "expired" | "missing";

const probes = new Map<string, Promise<AttachmentState>>();

/**
 * Whether the attachment can still be fetched: GET without following the redirect (a presigned
 * 302 costs nothing; an old database row would send bytes, so the body is cancelled at once).
 * 410 = expired (its retention ran out), 404 = gone. One probe per id per page load.
 */
export function attachmentState(id: string, fetchFn: typeof fetch = globalThis.fetch): Promise<AttachmentState> {
  let p = probes.get(id);
  if (!p) {
    p = fetchFn(attachmentUrl(id), { credentials: "same-origin", redirect: "manual" })
      .then((res) => {
        void res.body?.cancel().catch(() => undefined);
        return res.status === 410 ? "expired" : res.status === 404 ? "missing" : "ok";
      })
      .catch(() => "ok" as const);
    probes.set(id, p);
  }
  return p;
}

/** Marks an attachment expired (a 410 seen elsewhere, e.g. a failed save). */
export function markExpired(id: string): void {
  probes.set(id, Promise.resolve("expired"));
}

/** Tests: forget probe results. */
export function resetAttachmentProbes(): void {
  probes.clear();
}
