// Actions on a sent attachment (#58): saving it into the project's files and finding out whether it expired.
// Lists already say which attachments expired (meta.attachments / comments / dispatch carry
// expired: true); a probe is only made when an image fails to load.
import { client, unwrap } from "@/api/client";
import type { components } from "@/api/schema";
import { attachmentUrl } from "./images";

export type SavedFile = components["schemas"]["FileEntry"];

/** Copies the attachment into the project's files at path (409 file_exists unless overwrite, 409 folder_unavailable, 410 attachment_expired). */
export function saveAttachmentShared(id: string, path: string, overwrite = false): Promise<SavedFile> {
  return unwrap(client.POST("/api/attachments/{id}/save-shared", { params: { path: { id } }, body: { path, overwrite } }));
}

export type AttachmentState = "ok" | "expired" | "missing";

const probes = new Map<string, Promise<AttachmentState>>();

/**
 * Whether the attachment can still be fetched: GET without following the redirect (a presigned
 * 302 costs nothing; an old database row would send bytes, so the body is cancelled at once).
 * 410 = expired (its retention ran out), 404 = gone. One probe per id per page load; used only
 * when an image fails to load (the lists already mark expired ones).
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
