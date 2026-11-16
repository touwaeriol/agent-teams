<script setup lang="ts">
import { computed } from "vue";
import type { TaskView, ThreadView } from "@/api/views";
import TaskRow from "./TaskRow.vue";

const props = defineProps<{ thread: ThreadView; tasks: TaskView[]; all: TaskView[] }>();
const visible = computed(() => props.tasks.filter((t) => t.status !== "cancelled"));
</script>

<template>
  <div v-if="visible.length > 0" class="ml-[7px] flex flex-col border-l pl-[17px]">
    <TaskRow v-for="t in visible" :key="t.id" :thread="thread" :task="t" :all="all" />
  </div>
</template>
