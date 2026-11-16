<script setup lang="ts">
import { threads as threadActions } from "@/api/actions";
import { useAction } from "@/api/mutations";
import { keys } from "@/api/queries";
import type { MainTaskStatus, ThreadView } from "@/api/views";
import { cn } from "@/lib/utils";
import CheckMark from "../CheckMark.vue";

const props = defineProps<{ thread: ThreadView; id: string; title: string; status: MainTaskStatus }>();
const toggle = useAction(() => threadActions.setMainTask(props.thread.id, props.id, props.status === "done" ? "doing" : "done"), {
  invalidate: () => [keys.threads(props.thread.project_id)],
});
</script>

<template>
  <span class="flex items-center gap-2.5 text-[13px]">
    <button type="button" :title="status === 'done' ? '取消完成' : '标记为完成'" class="rounded-full" @click="toggle.mutate()"><CheckMark :status="status" /></button>
    <span :class="cn('min-w-0 flex-1', status === 'done' && 'text-[#8A877F] line-through')">{{ title }}</span>
  </span>
</template>
