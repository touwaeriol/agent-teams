<script setup lang="ts">
import { $t } from "@/i18n";
import { computed } from "vue";
import { threads as threadActions } from "@/api/actions";
import { useAction } from "@/api/mutations";
import { keys } from "@/api/queries";
import type { TaskView, ThreadView } from "@/api/views";
import { taskLook, taskTitle } from "@/lib/present";
import { cn } from "@/lib/utils";
import StateMark from "../StateMark.vue";

const props = defineProps<{ thread: ThreadView; task: TaskView; all: TaskView[] }>();
const look = computed(() => taskLook(props.task, props.all));
const retry = useAction(() => threadActions.retryTask(props.thread.id, props.task.id), { invalidate: () => [keys.tasks(props.thread.id)], success: $t("已重新放回任务池") });
</script>

<template>
  <span class="flex min-h-[26px] items-center gap-2 text-[13px]">
    <StateMark :look="look" :size="6" />
    <span :class="cn('min-w-0 flex-1 truncate', task.status === 'done' && 'text-[#8A877F] line-through')" :title="task.instruction">{{ taskTitle(task) }}</span>
    <span class="font-mono text-[11px] text-[#8A877F]">{{ task.claimed_by || $t("待领取") }}</span>
    <button
      v-if="task.status === 'failed'"
      type="button"
      :disabled="retry.isPending.value"
      class="min-w-10 text-right text-[11px] text-destructive underline-offset-2 hover:underline"
      @click="retry.mutate()"
    >
      {{ $t("失败 · 重试") }}
    </button>
    <span v-else class="min-w-10 text-right text-[11px]" :style="{ color: look.tone }">{{ look.label }}</span>
  </span>
</template>
