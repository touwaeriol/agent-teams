<script setup lang="ts">
/**
 * The round mark before a thread in lists. Without `mark`: a spinner while in progress, outlined
 * in the state colour otherwise, filled with ✓ when done. With `mark` (the thread header) it is
 * ThreadStatusIcon.
 */
import { computed } from "vue";
import type { ThreadView } from "@/api/views";
import { markTone, threadLook } from "@/lib/present";
import type { RowMark } from "@/lib/threadList";
import Spin from "./Spin.vue";
import ThreadStatusIcon from "./ThreadStatusIcon.vue";

const props = defineProps<{ thread: ThreadView; mark?: RowMark }>();
const tone = computed(() => markTone(props.thread));
const done = computed(() => props.thread.status === "done");
</script>

<template>
  <ThreadStatusIcon v-if="mark" :mark="mark" class="mt-0.5" />
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
