<script setup lang="ts">
/**
 * A plan as a checklist card (like Claude's todo card): one line per step with its status
 * icon. Collapsed, it shows only the current step (first doing, else first todo; when nothing
 * is left, "全部完成" if every step is done, else "已结束") and the progress x/y.
 *
 * Props: `steps` (status is a string: todo / doing / done / failed / skipped, anything else
 * renders as todo); `collapsible` (default true; false hides 收起 and always shows every step);
 * `defaultOpen` (default true, the starting state when `open` isn't bound); `open` with
 * `update:open` (v-model:open) to own the state; `listClass` for the step list (e.g. a max
 * height with scrolling).
 */
import { CircleSlash } from "lucide-vue-next";
import { computed, ref, watch } from "vue";
import { currentStep, stepsProgress } from "@/lib/threadList";
import { cn } from "@/lib/utils";
import { CircleDone, stepLook } from "./steps";

export interface PlanStep {
  id: string;
  title: string;
  status: string;
}

const props = withDefaults(
  defineProps<{ steps: readonly PlanStep[]; collapsible?: boolean; defaultOpen?: boolean; open?: boolean; listClass?: string }>(),
  { collapsible: true, defaultOpen: true, open: undefined, listClass: undefined },
);
const emit = defineEmits<{ "update:open": [open: boolean] }>();

const local = ref(props.open ?? props.defaultOpen);
watch(
  () => props.open,
  (v) => v !== undefined && (local.value = v),
);
const isOpen = computed(() => !props.collapsible || local.value);
function toggle() {
  local.value = !local.value;
  emit("update:open", local.value);
}

const current = computed(() => currentStep(props.steps));
/** Nothing left and every step done; otherwise (some failed / skipped) the plan merely ended. */
const allDone = computed(() => props.steps.every((s) => s.status === "done"));
const progress = computed(() => stepsProgress(props.steps).label);
</script>

<template>
  <section class="rounded-2xl border border-border bg-panel text-[13.5px] leading-5" data-plan-checklist :data-open="isOpen ? '' : undefined">
    <ul v-if="isOpen" :class="cn('scroll-thin flex flex-col gap-2 overflow-y-auto px-4 pb-1 pt-3', !collapsible && 'pb-3', listClass)">
      <li v-for="s in steps" :key="s.id" class="flex items-start gap-2.5" :data-status="stepLook(s.status).status">
        <component :is="stepLook(s.status).icon" :size="16" :class="cn('mt-0.5 shrink-0', stepLook(s.status).iconClass)" :aria-label="stepLook(s.status).label" role="img" />
        <span :class="cn('min-w-0 break-words', stepLook(s.status).textClass)">{{ s.title }}</span>
      </li>
    </ul>
    <div v-else class="flex items-start gap-2.5 px-4 pb-1 pt-3" data-current>
      <template v-if="current">
        <component :is="stepLook(current.status).icon" :size="16" :class="cn('mt-0.5 shrink-0', stepLook(current.status).iconClass)" :aria-label="stepLook(current.status).label" role="img" />
        <span :class="cn('min-w-0 flex-1 truncate', stepLook(current.status).textClass)" :title="current.title">{{ current.title }}</span>
      </template>
      <template v-else>
        <CircleDone v-if="allDone" :size="16" class="mt-0.5 shrink-0 text-muted-foreground" aria-label="已完成" />
        <CircleSlash v-else :size="16" class="mt-0.5 shrink-0 text-muted-foreground" aria-label="已结束" role="img" />
        <span class="min-w-0 flex-1 text-muted-foreground" :data-finished="allDone ? 'done' : 'ended'">{{ allDone ? "全部完成" : "已结束" }}</span>
      </template>
    </div>
    <div v-if="collapsible" class="flex items-center justify-between px-4 pb-2 pt-0.5 text-[12px] text-muted-foreground">
      <span class="tabular-nums" data-plan-progress>{{ progress }}</span>
      <button type="button" class="rounded px-1 py-0.5 hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring" :aria-expanded="isOpen" @click="toggle">
        {{ isOpen ? "收起" : "展开" }}
      </button>
    </div>
  </section>
</template>
