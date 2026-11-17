<script setup lang="ts">
/**
 * The small status icon before the thread title in the detail header's breadcrumb. Follows the
 * list's mark (`threadMark`) so the header and the row agree, and tells 已暂停 apart from 空闲:
 * blue spinner working, grey spinner queued, orange pulsing dot waiting for the user, dashed ring
 * idle, ⏸ paused, green ✓ done, red ✕ failed, archive box archived. `data-mark` carries the state.
 */
import { Archive, Check, Pause, X } from "lucide-vue-next";
import { computed } from "vue";
import type { ThreadView } from "@/api/views";
import { INK } from "@/lib/present";
import { threadMark, type RowMark } from "@/lib/threadList";
import Spin from "../Spin.vue";

export type StatusIconState = RowMark | "paused";

const props = defineProps<{ thread: ThreadView }>();
const state = computed<StatusIconState>(() => {
  const mark = threadMark(props.thread);
  return mark === "idle" && props.thread.status === "paused" ? "paused" : mark;
});
const LABEL: Record<StatusIconState, string> = {
  wait: "等你处理",
  working: "进行中",
  queued: "排队中",
  idle: "空闲",
  paused: "已暂停",
  done: "已完成",
  failed: "失败",
  archived: "已归档",
};
</script>

<template>
  <span class="flex size-4 shrink-0 items-center justify-center" role="img" :aria-label="LABEL[state]" :title="LABEL[state]" :data-mark="state">
    <Spin v-if="state === 'working' || state === 'queued'" :color="state === 'working' ? INK.doing : INK.gray" :size="13" />
    <span v-else-if="state === 'wait'" class="at-halo size-2 rounded-full" :style="{ background: INK.wait }" />
    <span v-else-if="state === 'idle'" class="size-3 rounded-full border-[1.5px] border-dashed" :style="{ borderColor: INK.faint }" />
    <Pause v-else-if="state === 'paused'" class="size-3.5" :style="{ color: INK.gray }" :stroke-width="2.4" />
    <Check v-else-if="state === 'done'" class="size-3.5" :style="{ color: INK.done }" :stroke-width="2.8" />
    <X v-else-if="state === 'failed'" class="size-3.5" :style="{ color: INK.bad }" :stroke-width="2.8" />
    <Archive v-else class="size-3.5" :style="{ color: INK.gray }" :stroke-width="2" />
  </span>
</template>
