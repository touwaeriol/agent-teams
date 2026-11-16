// State helpers of the 资料库 browser (web/src/components/app/Library.tsx).
import { onBeforeUnmount, onMounted, ref, type Ref } from "vue";
import { projects as projectActions } from "@/api/actions";
import { useAction } from "@/api/mutations";
import { keys } from "@/api/queries";
import { fileBase64, uploadProblem } from "@/lib/library";
import { readLocal, writeLocal } from "@/lib/persist";
import { toast } from "@/lib/toast";

export const LIBRARY_TYPES: [string, string][] = [
  ["", "全部类型"],
  ["design", "设计稿"],
  ["doc", "文档"],
  ["slides", "幻灯片"],
  ["pr", "PR"],
  ["data", "数据"],
];

export type LibraryView = "list" | "grid";

const VIEW_KEY = "library.view";

/** List or tiles, remembered per browser (list when storage is unavailable). */
export function storedView(): LibraryView {
  return readLocal(VIEW_KEY) === "grid" ? "grid" : "list";
}

export function saveView(v: LibraryView): void {
  writeLocal(VIEW_KEY, v);
}

export function typeName(value: string): string {
  return LIBRARY_TYPES.find(([id]) => id === value)?.[1] ?? "全部类型";
}

/** Uploads picked files one after another; refused ones become a toast. */
export function useUpload(pid: () => string) {
  const upload = useAction((file: File) => fileBase64(file).then((data) => projectActions.upload(pid(), { name: file.name, data })), {
    invalidate: () => [keys.allDeliverables(pid())],
    success: (d) => `已添加 ${d.title}`,
  });
  return async (files: FileList | File[] | null) => {
    for (const file of Array.from(files ?? [])) {
      const problem = uploadProblem(file);
      if (problem) toast.error(problem);
      else await upload.mutateAsync(file).catch(() => undefined);
    }
  };
}

/** Width of an element, kept up to date (0 before the first layout). */
export function useWidth(el: Ref<HTMLElement | null>): Ref<number> {
  const width = ref(0);
  let ro: ResizeObserver | null = null;
  onMounted(() => {
    if (!el.value || typeof ResizeObserver === "undefined") return;
    ro = new ResizeObserver(([e]) => (width.value = e.contentRect.width));
    ro.observe(el.value);
  });
  onBeforeUnmount(() => ro?.disconnect());
  return width;
}
