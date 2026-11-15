// Card thumbnail of a deliverable: hosted images show themselves (short-lived signed URL),
// everything else a tile in the kind's colours with its file icon and type.
import { useDeliverableFiles } from "@/api/queries";
import type { DeliverableView } from "@/api/views";
import { extensionOf, fileIcon } from "@/lib/library";
import { deliverableKind } from "@/lib/present";
import { FILE_ICONS } from "./Library";

export function DeliverableThumb({ item }: { item: DeliverableView }) {
  const image = item.detail.hosted === true && fileIcon(item) === "image";
  const files = useDeliverableFiles(item.id, 0, image);
  const look = deliverableKind(item.kind);
  const Icon = FILE_ICONS[fileIcon(item)];
  const ext = item.detail.hosted && item.detail.entry ? extensionOf(String(item.detail.entry)).toUpperCase() : "";
  if (image && files.data?.entry_url)
    return (
      <span className="flex aspect-[4/3] items-center justify-center overflow-hidden bg-[#F4F3F0]">
        <img src={files.data.entry_url} alt="" loading="lazy" className="size-full object-cover" />
      </span>
    );
  return (
    <span className="flex aspect-[4/3] flex-col items-center justify-center gap-1.5" style={{ background: look.block }}>
      <span className="flex size-10 items-center justify-center rounded-xl" style={{ background: look.bg, color: look.fg }}>
        <Icon aria-hidden className="size-5" strokeWidth={1.7} />
      </span>
      {ext && <span className="font-mono text-[10px] text-muted-foreground">{ext}</span>}
    </span>
  );
}
