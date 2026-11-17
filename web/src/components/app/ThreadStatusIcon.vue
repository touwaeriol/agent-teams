<script setup lang="ts">
/**
 * A thread's round status mark (the panel list rows, the open thread's header): orange dot with
 * a soft pulsing halo when it waits on the user, blue spinner working, grey spinner queued,
 * dashed grey ring idle, grey filled ✓ done, red filled ✕ failed, grey ring with ‖ paused,
 * plain grey ring archived. Get `mark` from `threadMark(thread, waiting)` in lib/threadList.
 */
import { Check, Pause, X } from "lucide-vue-next";
import { computed } from "vue";
import { INK } from "@/lib/present";
import type { RowMark } from "@/lib/threadList";
import Spin from "./Spin.vue";

const props = withDefaults(defineProps<{ mark: RowMark; size?: number }>(), { size: 16 });
const LABEL: Record<RowMark, string> = { wait: "等你处理", working: "进行中", queued: "排队中", idle: "空闲", done: "已解决", failed: "失败", paused: "已暂停", archived: "已归档" };
const box = computed(() => ({ width: `${props.size}px`, height: `${props.size}px` }));
/** The inner mark is a little smaller than the box so rings do not touch neighbours. */
const inner = computed(() => Math.round(props.size * 0.875));
const disc = computed(() => ({ width: `${inner.value}px`, height: `${inner.value}px` }));
</script>

<template>
  <span class="flex shrink-0 items-center justify-center" :style="box" role="img" :aria-label="LABEL[mark]" :data-mark="mark">
    <Spin v-if="mark === 'working' || mark === 'queued'" :color="mark === 'working' ? INK.doing : INK.gray" :size="inner" />
    <span v-else-if="mark === 'wait'" class="at-halo size-2.5 rounded-full" :style="{ background: INK.wait }" />
    <span v-else-if="mark === 'idle'" class="rounded-full border-[1.5px] border-dashed" :style="{ ...disc, borderColor: INK.faint }" />
    <span v-else-if="mark === 'done'" class="flex items-center justify-center rounded-full" :style="{ ...disc, background: INK.gray }">
      <Check class="size-2.5 text-white" :stroke-width="3.5" />
    </span>
    <span v-else-if="mark === 'failed'" class="flex items-center justify-center rounded-full" :style="{ ...disc, background: INK.bad }">
      <X class="size-2.5 text-white" :stroke-width="3.5" />
    </span>
    <span v-else-if="mark === 'paused'" class="flex items-center justify-center rounded-full border-[1.5px]" :style="{ ...disc, borderColor: INK.gray, color: INK.gray }">
      <Pause class="size-2" :stroke-width="3" fill="currentColor" />
    </span>
    <span v-else class="rounded-full border-[1.5px]" :style="{ ...disc, borderColor: INK.idle }" />
  </span>
</template>
