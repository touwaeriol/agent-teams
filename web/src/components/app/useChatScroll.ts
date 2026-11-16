// Chat scrolling: reopening a conversation goes back to where it was read (kept per conversation
// in localStorage); new messages scroll into view only while the reader follows the bottom.
import { nextTick, onBeforeUnmount, watch, type Ref } from "vue";
import { readScroll, restoredTop, scrollMarkOf, scrollParent, writeScroll } from "@/lib/persist";

export function useChatScroll(cid: () => string | undefined, end: Ref<HTMLElement | null>, count: () => number, ready: () => boolean) {
  let restored: string | null = null;
  let following = true;
  let detach: (() => void) | null = null;

  const restore = () => {
    const box = scrollParent(end.value);
    const id = cid();
    if (!id || !ready() || !box || restored === id) return;
    restored = id;
    const mark = readScroll(id);
    following = !mark;
    box.scrollTop = restoredTop(mark, box);
  };

  const follow = () => {
    if (restored === cid() && following) end.value?.scrollIntoView({ block: "end" });
  };

  const listen = () => {
    detach?.();
    detach = null;
    const box = scrollParent(end.value);
    const id = cid();
    if (!id || !box) return;
    let timer: ReturnType<typeof setTimeout> | null = null;
    const onScroll = () => {
      const mark = scrollMarkOf(box);
      following = mark.fromBottom === 0;
      if (timer) clearTimeout(timer);
      timer = setTimeout(() => writeScroll(id, mark), 200);
    };
    box.addEventListener("scroll", onScroll, { passive: true });
    detach = () => {
      box.removeEventListener("scroll", onScroll);
      if (timer) clearTimeout(timer);
    };
  };

  watch(
    () => [cid(), ready(), count()] as const,
    async (_now, old) => {
      await nextTick();
      restore();
      follow();
      if (!old || old[0] !== cid() || old[1] !== ready()) listen();
    },
    { immediate: true, flush: "post" },
  );
  onBeforeUnmount(() => detach?.());
}
