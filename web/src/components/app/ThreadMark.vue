<script setup lang="ts">
/**
 * The round mark before a thread in lists. Without `mark`: a spinner while in progress, outlined
 * in the state colour otherwise, filled with ✓ when done. With `mark` (the panel list, the thread
 * header): orange dot with a soft pulsing halo waiting, blue spinner working, dashed grey ring
 * paused, grey ✓ done, red ✕ failed; a session's 已完成 is a green ✓ in a ring, 已解决 a grey ✓✓.
 */
import { Check, CheckCheck, X } from "lucide-vue-next";
import { computed } from "vue";
import type { ThreadView } from "@/api/views";
import { INK, markTone, threadLook } from "@/lib/present";
import { threadPhase } from "@/lib/phase";
import { MARK_LABEL, type RowMark } from "@/lib/threadList";
import Spin from "./Spin.vue";

const props = defineProps<{ thread: ThreadView; mark?: RowMark }>();
const tone = computed(() => markTone(props.thread));
const done = computed(() => props.thread.status === "done");
/** The state's name from threadPhase (e.g. 待验收), the mark's generic one when the caller's mark differs. */
const label = computed(() => {
  const p = threadPhase(props.thread);
  return props.mark && p.mark === props.mark ? p.label : MARK_LABEL[props.mark ?? "working"];
});
</script>

<template>
  <template v-if="mark">
    <span class="mt-0.5 flex size-4 shrink-0 items-center justify-center" role="img" :aria-label="label" :data-mark="mark">
      <Spin v-if="mark === 'working' || mark === 'queued'" :color="mark === 'working' ? INK.doing : INK.gray" :size="14" />
      <span v-else-if="mark === 'wait'" class="at-halo size-2.5 rounded-full" :style="{ background: INK.wait }" />
      <span v-else-if="mark === 'paused'" class="size-[13px] rounded-full border-[1.5px] border-dashed" :style="{ borderColor: INK.faint }" />
      <span v-else-if="mark === 'done'" class="flex size-[14px] items-center justify-center rounded-full" :style="{ background: INK.gray }"><Check class="size-2.5 text-white" :stroke-width="3.5" /></span>
      <span v-else-if="mark === 'finished'" class="flex size-[14px] items-center justify-center rounded-full border-[1.5px]" :style="{ borderColor: INK.done, color: INK.done }"><Check class="size-2.5" :stroke-width="3.5" /></span>
      <span v-else-if="mark === 'resolved'" class="flex size-[14px] items-center justify-center rounded-full" :style="{ background: INK.gray }"><CheckCheck class="size-2.5 text-white" :stroke-width="3" /></span>
      <span v-else-if="mark === 'failed'" class="flex size-[14px] items-center justify-center rounded-full" :style="{ background: INK.bad }"><X class="size-2.5 text-white" :stroke-width="3.5" /></span>
      <span v-else class="size-[13px] rounded-full border-[1.5px]" :style="{ borderColor: INK.idle }" />
    </span>
  </template>
  <Spin v-else-if="threadLook(thread).spin" :color="tone" :size="16" class="mt-0.5" label="进行中" />
  <span
    v-else
    aria-hidden="true"
    class="mt-0.5 flex size-4 shrink-0 items-center justify-center rounded-full border-[1.5px] text-[10px] font-bold text-white"
    :style="{ borderColor: tone, background: done ? tone : '#FFFFFF' }"
  >
    {{ done ? "✓" : "" }}
  </span>
</template>
