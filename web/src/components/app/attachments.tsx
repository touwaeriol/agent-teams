// Image input for message boxes: paste, drop or pick images; each uploads at once to
// /api/projects/{pid}/attachments and shows as a thumbnail with a remove button. Sending
// passes the uploaded ids; the strip clears after a successful send.
import { ImagePlus, X } from "lucide-react";
import { useCallback, useEffect, useRef, useState, type ClipboardEvent, type DragEvent } from "react";
import { toast } from "sonner";
import { projects as projectActions } from "@/api/actions";
import { errorText } from "@/api/mutations";
import type { AttachmentRef } from "@/api/views";
import { Spinner } from "@/components/common";
import { attachmentUrl, IMAGE_ACCEPT, imageFiles, pickImages } from "@/lib/images";
import { fileBase64 } from "@/lib/library";
import { cn } from "@/lib/utils";

export interface PendingImage {
  key: string;
  name: string;
  preview: string;
  id?: string;
  error?: string;
}

export interface Attachments {
  items: PendingImage[];
  add: (files: File[]) => void;
  remove: (key: string) => void;
  clear: () => void;
  /** Uploaded ids in order; null while an upload is still running. */
  ids: () => string[] | null;
  uploading: boolean;
}

let counter = 0;

/** State of the images attached to one message box. */
export function useAttachments(pid: string): Attachments {
  const [items, setItems] = useState<PendingImage[]>([]);
  const urls = useRef<string[]>([]);
  useEffect(() => () => urls.current.forEach((u) => URL.revokeObjectURL(u)), []);
  const patch = (key: string, p: Partial<PendingImage>) => setItems((list) => list.map((x) => (x.key === key ? { ...x, ...p } : x)));
  const upload = useCallback(
    async (key: string, file: File) => {
      try {
        const res = await projectActions.attach(pid, { name: file.name || "image", data: await fileBase64(file) });
        patch(key, { id: res.id });
      } catch (e) {
        patch(key, { error: errorText(e) });
      }
    },
    [pid],
  );
  const add = (files: File[]) => {
    const { ok, problems } = pickImages(files, items.length);
    problems.forEach((p) => toast.error(p));
    const fresh = ok.map((f) => ({ key: `img-${++counter}`, name: f.name || "图片", preview: URL.createObjectURL(f), file: f }));
    urls.current.push(...fresh.map((f) => f.preview));
    setItems((list) => [...list, ...fresh.map(({ file: _file, ...x }) => x)]);
    fresh.forEach((f) => void upload(f.key, f.file));
  };
  const remove = (key: string) => setItems((list) => list.filter((x) => x.key !== key));
  const ids = () => (items.some((x) => !x.id && !x.error) ? null : items.filter((x) => x.id).map((x) => x.id!));
  return { items, add, remove, clear: () => setItems([]), ids, uploading: items.some((x) => !x.id && !x.error) };
}

/** Paste and drop handlers for the element that wraps a message box. */
export function attachmentHandlers(a: Attachments | undefined, onDragState: (on: boolean) => void) {
  if (!a) return {};
  return {
    onPaste: (e: ClipboardEvent) => {
      const files = imageFiles(e.clipboardData?.files);
      if (files.length === 0) return;
      e.preventDefault();
      a.add(files);
    },
    onDragOver: (e: DragEvent) => {
      if (!Array.from(e.dataTransfer.types).includes("Files")) return;
      e.preventDefault();
      onDragState(true);
    },
    onDragLeave: () => onDragState(false),
    onDrop: (e: DragEvent) => {
      onDragState(false);
      if (e.dataTransfer.files.length === 0) return;
      e.preventDefault();
      a.add(Array.from(e.dataTransfer.files));
    },
  };
}

/** Thumbnails of the attached images, each with a remove button. */
export function AttachmentStrip({ a, className }: { a: Attachments; className?: string }) {
  if (a.items.length === 0) return null;
  return (
    <div className={cn("flex flex-wrap gap-2", className)} aria-label="附带的图片">
      {a.items.map((x) => (
        <span key={x.key} className={cn("group relative size-14 shrink-0 overflow-hidden rounded-lg border bg-raised", x.error && "border-destructive")} title={x.error ?? x.name}>
          <img src={x.preview} alt={x.name} className={cn("size-full object-cover", !x.id && "opacity-60")} />
          {!x.id && !x.error && (
            <span className="absolute inset-0 flex items-center justify-center">
              <Spinner className="size-4" />
            </span>
          )}
          {x.error && <span className="absolute inset-x-0 bottom-0 bg-destructive/90 px-1 text-center text-[10px] text-white">失败</span>}
          <button
            type="button"
            aria-label={`移除 ${x.name}`}
            onClick={() => a.remove(x.key)}
            className="absolute right-0.5 top-0.5 flex size-5 items-center justify-center rounded-full bg-black/60 text-white opacity-90 hover:opacity-100"
          >
            <X className="size-3" strokeWidth={2.5} />
          </button>
        </span>
      ))}
    </div>
  );
}

/** The image picker button. */
export function AttachButton({ a, size = 32 }: { a: Attachments; size?: number }) {
  const input = useRef<HTMLInputElement>(null);
  return (
    <>
      <button
        type="button"
        aria-label="添加图片"
        title="添加图片（也可以粘贴或拖进来）"
        onClick={() => input.current?.click()}
        className="flex shrink-0 items-center justify-center rounded-lg text-muted-foreground hover:bg-raised hover:text-foreground"
        style={{ width: size, height: size }}
      >
        <ImagePlus className="size-[18px]" strokeWidth={1.8} />
      </button>
      <input
        ref={input}
        type="file"
        accept={IMAGE_ACCEPT}
        multiple
        hidden
        onChange={(e) => {
          a.add(Array.from(e.target.files ?? []));
          e.target.value = "";
        }}
      />
    </>
  );
}

/** Images of a sent message, as thumbnails that open the full image. */
export function MessageImages({ items, align = "end" }: { items: AttachmentRef[] | undefined; align?: "start" | "end" }) {
  if (!items?.length) return null;
  return (
    <div className={cn("flex flex-wrap gap-2", align === "end" ? "justify-end self-end" : "self-start")}>
      {items.map((x) => (
        <a key={x.id} href={attachmentUrl(x.id)} target="_blank" rel="noreferrer" className="block size-24 overflow-hidden rounded-xl border bg-raised" title={x.name}>
          <img src={attachmentUrl(x.id)} alt={x.name} loading="lazy" className="size-full object-cover" />
        </a>
      ))}
    </div>
  );
}
