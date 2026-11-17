<script setup lang="ts">
/** A thread the speaker opened, with its live status underneath while it runs, and its plan progress "x/y" with the current step. */
import { ChevronRight, ListChecks } from "lucide-vue-next";
import { computed } from "vue";
import type { ThreadView } from "@/api/views";
import { featured, isWorking, liveLine } from "@/lib/live";
import { markTone } from "@/lib/present";
import { currentStep, mainProgress } from "@/lib/threadList";
import { cn } from "@/lib/utils";
import Dot from "./Dot.vue";
import LiveDot from "./LiveDot.vue";

const props = defineProps<{ thread?: ThreadView; no: number }>();
const emit = defineEmits<{ open: [no: number] }>();
const tone = computed(() => (props.thread ? markTone(props.thread) : "#8A877F"));
const line = computed(() => (props.thread ? liveLine(props.thread) : ""));
const plan = computed(() => (props.thread ? mainProgress(props.thread) : null));
const step = computed(() => (props.thread ? currentStep(props.thread.main_tasks) : null));
const asks = computed(() => (props.thread?.counts.waiting ?? 0) > 0);
const working = computed(() => {
  const f = props.thread ? featured(props.thread) : undefined;
  return Boolean(f && isWorking(f));
});
</script>

<template>
  <button
    type="button"
    class="flex min-h-8 max-w-full flex-col items-start gap-0.5 self-start rounded-lg border bg-panel px-3 py-1.5 text-left text-[13px] hover:border-input"
    @click="emit('open', no)"
  >
    <span class="flex items-center gap-2">
      <Dot :color="tone" />
      讨论串 #{{ no }} {{ thread?.title ?? "" }}
      <ChevronRight class="size-3 text-[#8A877F]" :stroke-width="2" />
    </span>
    <span v-if="line" :class="cn('flex min-w-0 max-w-full items-center gap-1.5 pl-[15px] text-xs', asks ? 'text-[#B4530B]' : 'text-muted-foreground')">
      <LiveDot v-if="working && !asks" color="#2F6FDB" :on="true" :size="6" />
      <span class="truncate">{{ line }}</span>
    </span>
    <span v-if="plan" class="flex min-w-0 max-w-full items-center gap-1.5 pl-[15px] text-xs text-muted-foreground" data-chip-plan="">
      <span class="inline-flex shrink-0 items-center gap-1 tabular-nums" :title="`主任务完成 ${plan.label}`"><ListChecks class="size-3" :stroke-width="1.8" />{{ plan.label }}</span>
      <span v-if="step" class="truncate" :title="step.title" data-chip-step="">{{ step.title }}</span>
    </span>
  </button>
</template>
