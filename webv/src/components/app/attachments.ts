// Image input for message boxes: paste, drop or pick images; each uploads at once to
// /api/projects/{pid}/attachments and shows as a thumbnail with a remove button. Sending
// passes the uploaded ids; the strip clears after a successful send.
import { computed, onBeforeUnmount, ref, toValue, type ComputedRef, type MaybeRefOrGetter, type Ref } from "vue";
import { projects as projectActions } from "@/api/actions";
import { errorText } from "@/api/mutations";
import { imageFiles, pickImages } from "@/lib/images";
import { fileBase64 } from "@/lib/library";
import { toast } from "@/lib/toast";

export interface PendingImage {
  key: string;
  name: string;
  preview: string;
  id?: string;
  error?: string;
}

export interface Attachments {
  items: Ref<PendingImage[]>;
  add: (files: File[]) => void;
  remove: (key: string) => void;
  clear: () => void;
  /** Uploaded ids in order; null while an upload is still running. */
  ids: () => string[] | null;
  uploading: ComputedRef<boolean>;
}

let counter = 0;

/** Uploads one image; injectable for tests. */
export type Uploader = (pid: string, file: File) => Promise<{ id: string }>;

const upload: Uploader = async (pid, file) => projectActions.attach(pid, { name: file.name || "image", data: await fileBase64(file) });

/** State of the images attached to one message box. */
export function useAttachments(pid: MaybeRefOrGetter<string>, uploader: Uploader = upload): Attachments {
  const items = ref<PendingImage[]>([]);
  const urls: string[] = [];
  onBeforeUnmount(() => urls.forEach((u) => URL.revokeObjectURL(u)));
  const patch = (key: string, p: Partial<PendingImage>) => (items.value = items.value.map((x) => (x.key === key ? { ...x, ...p } : x)));
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
    const fresh = ok.map((f) => ({ key: `img-${++counter}`, name: f.name || "图片", preview: URL.createObjectURL(f), file: f }));
    urls.push(...fresh.map((f) => f.preview));
    items.value = [...items.value, ...fresh.map(({ file: _file, ...x }) => x)];
    fresh.forEach((f) => void send(f.key, f.file));
  };
  const remove = (key: string) => (items.value = items.value.filter((x) => x.key !== key));
  const pending = () => items.value.some((x) => !x.id && !x.error);
  const ids = () => (pending() ? null : items.value.filter((x) => x.id).map((x) => x.id!));
  return { items, add, remove, clear: () => (items.value = []), ids, uploading: computed(pending) };
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
