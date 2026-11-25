// File input for message boxes: paste, drop or pick any files (at most 200 MB each, 20 per message);
// each uploads at once straight to storage in parts (lib/upload.ts) with a progress bar, and shows
// as a thumbnail (images) or a file card with a remove button that also cancels a running upload.
// Without object storage (503 storage_unavailable) files up to 5 MB go through the old base64
// upload. Sending passes the uploaded ids; the strip clears after a successful send.
import { computed, onBeforeUnmount, ref, toValue, watch, type ComputedRef, type MaybeRefOrGetter, type Ref } from "vue";
import { projects as projectActions } from "@/api/actions";
import { errorText } from "@/api/mutations";
import { attachKind, imageFiles, LEGACY_MAX_BYTES, pickImages, type AttachKind } from "@/lib/images";
import { fileBase64 } from "@/lib/library";
import { formatSize } from "@/lib/sharedFiles";
import { toast } from "@/lib/toast";
import { projectUploads, storageUnavailable, UploadCancelled, uploadFile, type UploadApi } from "@/lib/upload";

export interface PendingImage {
  key: string;
  name: string;
  /** Object URL of an image ("" for PDFs and text files). */
  preview: string;
  kind: AttachKind;
  size?: number;
  /** 0..1 while uploading. */
  progress?: number;
  id?: string;
  error?: string;
}

export interface Attachments {
  items: Ref<PendingImage[]>;
  add: (files: File[]) => void;
  remove: (key: string) => void;
  clear: () => void;
  /** Takes every item out of the box for a send (previews stay valid until discard). */
  take: () => PendingImage[];
  /** A failed send: puts taken items back in front of whatever was attached meanwhile. */
  restore: (taken: PendingImage[]) => void;
  /** A successful send: releases the taken items' previews. */
  discard: (taken: PendingImage[]) => void;
  /** Uploaded ids in order; null while an upload is still running. */
  ids: () => string[] | null;
  uploading: ComputedRef<boolean>;
}

let counter = 0;

export interface UploadHooks {
  onProgress?: (fraction: number) => void;
  signal?: AbortSignal;
}

/** Uploads one file; injectable for tests. */
export type Uploader = (pid: string, file: File, hooks?: UploadHooks) => Promise<{ id: string }>;

export const STORAGE_FALLBACK_NOTE = "文件存储暂时不可用，已改用旧方式上传（只支持 5 MB 以内的文件）";

/**
 * The multipart direct upload through `api` (project uploads, or the project-less one of a new
 * session); when the console has no object storage, the old base64 upload for files up to 5 MB.
 */
export function directUploader(api: (pid: string) => UploadApi = projectUploads, o: { legacy?: boolean } = {}): Uploader {
  return async (pid, file, hooks = {}) => {
    try {
      const done = await uploadFile(file, api(pid), {
        kind: "attachment", ...(hooks.signal ? { signal: hooks.signal } : {}), onProgress: (sent, total) => hooks.onProgress?.(total ? sent / total : 1),
      });
      if (!done.attachment) throw new Error("服务器没有返回附件");
      return { id: done.attachment.id };
    } catch (e) {
      if (!storageUnavailable(e)) throw e;
      if (o.legacy === false) throw new Error("文件存储暂时不可用，现在不能上传附件");
      if (file.size > LEGACY_MAX_BYTES) throw new Error(`文件存储暂时不可用，现在只能上传 5 MB 以内的文件（这个文件 ${formatSize(file.size)}）`);
      toast.warning(STORAGE_FALLBACK_NOTE);
      const r = await projectActions.attach(pid, { name: file.name || "file", data: await fileBase64(file) });
      hooks.onProgress?.(1);
      return r;
    }
  };
}

const upload: Uploader = directUploader();

/** Attachments of boxes with a cache key, kept for the page so a remounted box gets them back. */
const shelf = new Map<string, Ref<PendingImage[]>>();

/**
 * State of the images attached to one message box. With cacheKey (the box's draft key) the list
 * outlives the component: uploads still finish into it and the preview URLs stay valid until the
 * item is removed or sent; without one it is the component's own and goes with it.
 */
export function useAttachments(pid: MaybeRefOrGetter<string>, uploader: Uploader = upload, cacheKey?: MaybeRefOrGetter<string | undefined>): Attachments {
  const own = ref<PendingImage[]>([]);
  // Preview URLs this component owns (unkeyed items): revoked with it.
  const urls: string[] = [];
  onBeforeUnmount(() => urls.forEach((u) => URL.revokeObjectURL(u)));
  const box = (): Ref<PendingImage[]> => {
    const k = toValue(cacheKey);
    if (!k) return own;
    let r = shelf.get(k);
    if (!r) shelf.set(k, (r = ref<PendingImage[]>([])));
    return r;
  };
  const items = computed<PendingImage[]>({ get: () => box().value, set: (v) => (box().value = v) });
  // Files attached before the key was known move into its list when it arrives.
  watch(
    () => toValue(cacheKey),
    (k) => {
      if (!k || own.value.length === 0) return;
      const moved = own.value;
      own.value = [];
      for (const x of moved) if (urls.includes(x.preview)) urls.splice(urls.indexOf(x.preview), 1);
      box().value = [...box().value, ...moved];
    },
  );
  const revoke = (gone: PendingImage[]) =>
    gone.forEach((x) => {
      if (!x.preview) return;
      URL.revokeObjectURL(x.preview);
      if (urls.includes(x.preview)) urls.splice(urls.indexOf(x.preview), 1);
    });
  // An upload lands in whichever list holds its item now (the key may have changed meanwhile).
  const patch = (key: string, p: Partial<PendingImage>) => {
    for (const r of [own, ...shelf.values()]) if (r.value.some((x) => x.key === key)) r.value = r.value.map((x) => (x.key === key ? { ...x, ...p } : x));
  };
  // Running uploads by item key: removing an item cancels its upload.
  const running = new Map<string, AbortController>();
  const send = async (key: string, file: File) => {
    const ctl = new AbortController();
    running.set(key, ctl);
    try {
      const { id } = await uploader(toValue(pid), file, { signal: ctl.signal, onProgress: (f) => patch(key, { progress: f }) });
      patch(key, { id, progress: 1 });
    } catch (e) {
      if (!(e instanceof UploadCancelled)) patch(key, { error: errorText(e) });
    } finally {
      running.delete(key);
    }
  };
  const add = (files: File[]) => {
    const { ok, problems } = pickImages(files, items.value.length);
    problems.forEach((p) => toast.error(p));
    const fresh = ok.map((f) => {
      const kind = attachKind(f);
      return { key: `img-${++counter}`, name: f.name || "文件", kind, size: f.size, progress: 0, preview: kind === "image" ? URL.createObjectURL(f) : "", file: f };
    });
    if (!toValue(cacheKey)) urls.push(...fresh.map((f) => f.preview).filter(Boolean));
    items.value = [...items.value, ...fresh.map(({ file: _file, ...x }) => x)];
    fresh.forEach((f) => void send(f.key, f.file));
  };
  const remove = (key: string) => {
    running.get(key)?.abort();
    revoke(items.value.filter((x) => x.key === key));
    items.value = items.value.filter((x) => x.key !== key);
  };
  const clear = () => {
    for (const x of items.value) running.get(x.key)?.abort();
    revoke(items.value);
    items.value = [];
  };
  const take = () => {
    const taken = items.value;
    items.value = [];
    return taken;
  };
  const restore = (taken: PendingImage[]) => {
    if (taken.length) items.value = [...taken, ...items.value.filter((x) => !taken.some((t) => t.key === x.key))];
  };
  const pending = () => items.value.some((x) => !x.id && !x.error);
  const ids = () => (pending() ? null : items.value.filter((x) => x.id).map((x) => x.id!));
  return { items, add, remove, clear, take, restore, discard: revoke, ids, uploading: computed(pending) };
}

/** Paste and drop handlers for the element that wraps a message box (bind with v-on="..."). */
export function attachmentHandlers(a: Attachments | undefined, onDragState: (on: boolean) => void): Record<string, (e: Event) => void> {
  if (!a) return {};
  return {
    paste: (e) => {
      const files = imageFiles((e as ClipboardEvent).clipboardData?.files);
      if (files.length === 0) return;
      e.preventDefault();
      a.add(files);
    },
    dragover: (e) => {
      const d = (e as DragEvent).dataTransfer;
      if (!d || !Array.from(d.types).includes("Files")) return;
      e.preventDefault();
      onDragState(true);
    },
    dragleave: () => onDragState(false),
    drop: (e) => {
      onDragState(false);
      const files = (e as DragEvent).dataTransfer?.files;
      if (!files || files.length === 0) return;
      e.preventDefault();
      a.add(Array.from(files));
    },
  };
}
