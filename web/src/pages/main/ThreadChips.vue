<script setup lang="ts">
/** Phones and narrow screens: active threads as chips above the chat. */
import { computed } from "vue";
import { RouterLink } from "vue-router";
import type { ThreadView } from "@/api/views";
import StateMark from "@/components/app/StateMark.vue";
import { markTone, threadLook } from "@/lib/present";

const props = defineProps<{ pid: string; threads: ThreadView[] }>();
const live = computed(() => props.threads.filter((t) => !["done", "failed", "archived"].includes(t.status)));
</script>

<template>
  <div v-if="live.length > 0" class="scroll-thin flex gap-2 overflow-x-auto border-b border-[#EFEDE8] px-3 py-2.5 lg:hidden">
    <RouterLink v-for="t in live" :key="t.id" :to="`/projects/${pid}/threads/${t.no}`" class="flex h-8 shrink-0 items-center gap-1.5 rounded-full border bg-panel px-3 text-xs">
      <StateMark :look="{ tone: markTone(t), spin: threadLook(t).spin }" />#{{ t.no }} {{ t.title }}
    </RouterLink>
  </div>
</template>
