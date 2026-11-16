<script setup lang="ts">
/**
 * The round mark before a thread in lists. Without `mark`: a spinner while in progress, outlined
 * in the state colour otherwise, filled with ✓ when done. With `mark` (the panel list, the thread
 * header): orange dot with a soft pulsing halo waiting, blue spinner working, dashed grey ring
 * idle, grey ✓ done, red ✕ failed.
 */
import { Check, X } from "lucide-vue-next";
import { computed } from "vue";
import type { ThreadView } from "@/api/views";
import { INK, markTone, threadLook } from "@/lib/present";
import type { RowMark } from "@/lib/threadList";
import Spin from "./Spin.vue";

const props = defineProps<{ thread: ThreadView; mark?: RowMark }>();
const tone = computed(() => markTone(props.thread));
const done = computed(() => props.thread.status === "done");
const LABEL: Record<RowMark, string> = { wait: "等你处理", working: "进行中", queued: "排队中", idle: "空闲", done: "已完成", failed: "失败", archived: "已归档" };
</script>

<template>
  <template v-if="mark">
    <span class="mt-0.5 flex size-4 shrink-0 items-center justify-center" role="img" :aria-label="LABEL[mark]" :data-mark="mark">
      <Spin v-if="mark === 'working' || mark === 'queued'" :color="mark === 'working' ? INK.doing : INK.gray" :size="14" />
      <span v-else-if="mark === 'wait'" class="at-halo size-2.5 rounded-full" :style="{ background: INK.wait }" />
      <span v-else-if="mark === 'idle'" class="size-[13px] rounded-full border-[1.5px] border-dashed" :style="{ borderColor: INK.faint }" />
      <span v-else-if="mark === 'done'" class="flex size-[14px] items-center justify-center rounded-full" :style="{ background: INK.gray }"><Check class="size-2.5 text-white" :stroke-width="3.5" /></span>
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
