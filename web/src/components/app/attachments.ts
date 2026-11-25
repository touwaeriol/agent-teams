// File input for message boxes: paste, drop or pick images, PDFs or text files; each uploads at
// once to /api/projects/{pid}/attachments and shows as a thumbnail (images) or a file chip with a
// remove button. Sending passes the uploaded ids; the strip clears after a successful send.
import { computed, onBeforeUnmount, ref, toValue, watch, type ComputedRef, type MaybeRefOrGetter, type Ref } from "vue";
import { projects as projectActions } from "@/api/actions";
import { errorText } from "@/api/mutations";
import { attachKind, pastedFiles, pickImages, type AttachKind } from "@/lib/images";
import { fileBase64 } from "@/lib/library";
import { toast } from "@/lib/toast";

export interface PendingImage {
  key: string;
  name: string;
  /** Object URL of an image ("" for PDFs and text files). */
  preview: string;
  kind: AttachKind;
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

/** Uploads one image; injectable for tests. */
export type Uploader = (pid: string, file: File) => Promise<{ id: string }>;

const upload: Uploader = async (pid, file) => projectActions.attach(pid, { name: file.name || "image", data: await fileBase64(file) });

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
  const send = async (key: string, file: File) => {
    try {
      patch(key, { id: (await uploader(toValue(pid), file)).id });
    } catch (e) {
      patch(key, { error: errorText(e) });
    }
  };
  const add = (files: File[]) => {
    const { ok, problems } = pickImages(files, items.value.length);
    problems.forEach((p) => toast.error(p));
    const fresh = ok.map((f) => {
      const kind = attachKind(f) ?? "text";
      return { key: `img-${++counter}`, name: f.name || "图片", kind, preview: kind === "image" ? URL.createObjectURL(f) : "", file: f };
    });
    if (!toValue(cacheKey)) urls.push(...fresh.map((f) => f.preview).filter(Boolean));
    items.value = [...items.value, ...fresh.map(({ file: _file, ...x }) => x)];
    fresh.forEach((f) => void send(f.key, f.file));
  };
  const remove = (key: string) => {
    revoke(items.value.filter((x) => x.key === key));
    items.value = items.value.filter((x) => x.key !== key);
  };
  const clear = () => {
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
      const files = pastedFiles((e as ClipboardEvent).clipboardData);
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
