<script setup lang="ts">
import { $t } from "@/i18n";
/** 任务: small-task progress and each main task (click to toggle done) with its small tasks. */
import { computed } from "vue";
import { useTasks } from "@/api/queries";
import type { ThreadView } from "@/api/views";
import Loading from "@/components/common/Loading.vue";
import { taskProgress } from "@/lib/live";
import ProgressBar from "../ProgressBar.vue";
import MainTaskLine from "./MainTaskLine.vue";
import TaskRows from "./TaskRows.vue";

const props = defineProps<{ thread: ThreadView }>();
const tasks = useTasks(() => props.thread.id);
const all = computed(() => tasks.data.value ?? []);
const p = computed(() => taskProgress(all.value));
const mains = computed(() => props.thread.main_tasks.slice().sort((a, b) => a.pos - b.pos));
const loose = computed(() => all.value.filter((t) => !t.main_task_id && t.status !== "cancelled"));
</script>

<template>
  <Loading v-if="tasks.isLoading.value" class="py-6" />
  <template v-else>
    <span class="flex items-center gap-2.5 text-xs text-muted-foreground">
      <span class="whitespace-nowrap">{{ $t("小任务 {done}/{total}", { done: p.done, total: p.total }) }}</span>
      <ProgressBar :pct="`${p.pct}%`" tone="#2F6FDB" />
      <span v-if="p.running > 0" class="whitespace-nowrap">{{ $t("进行中 {running}", { running: p.running }) }}</span>
      <span v-if="p.failed > 0" class="whitespace-nowrap text-destructive">{{ $t("失败 {failed}", { failed: p.failed }) }}</span>
    </span>
    <p v-if="mains.length === 0" class="text-[13px] text-muted-foreground">{{ $t("组长还在列主要任务…") }}</p>
    <section v-for="m in mains" :key="m.id" class="flex flex-col gap-1">
      <MainTaskLine :thread="thread" :id="m.id" :title="m.title" :status="m.status" />
      <TaskRows :thread="thread" :tasks="all.filter((t) => t.main_task_id === m.id)" :all="all" />
    </section>
    <section v-if="loose.length > 0" class="flex flex-col gap-1">
      <span class="text-xs text-muted-foreground">{{ $t("其他小任务") }}</span>
      <TaskRows :thread="thread" :tasks="loose" :all="all" />
    </section>
  </template>
</template>
