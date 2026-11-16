<script setup lang="ts">
/** The employee's TodoWrite checklist, updated in place; a spinner marks the item in progress. */
import { ChevronDown, ChevronRight, Circle, CircleCheck, ListTodo } from "lucide-vue-next";
import { computed, ref } from "vue";
import type { MessageView, TodoItem } from "@/api/views";
import { cn } from "@/lib/utils";
import Spin from "../Spin.vue";
import { CARD } from "./style";

const props = defineProps<{ message: MessageView }>();
const todos = computed<TodoItem[]>(() => props.message.meta.todos ?? []);
const done = computed(() => todos.value.filter((t) => t.status === "completed").length);
const open = ref(true);
</script>

<template>
  <section aria-label="待办清单" :class="cn(CARD, 'flex flex-col px-4 py-3')">
    <button type="button" :aria-expanded="open" class="flex items-center gap-2 text-left text-[13px] text-muted-foreground" @click="open = !open">
      <ListTodo aria-hidden="true" class="size-4" :stroke-width="1.8" />
      <span class="flex-1">{{ message.meta.employee ?? message.author }} 的待办 · {{ done }}/{{ todos.length }}{{ done === todos.length ? " 已完成" : "" }}</span>
      <ChevronDown v-if="open" class="size-3.5" />
      <ChevronRight v-else class="size-3.5" />
    </button>
    <ul v-if="open" class="mt-2.5 flex flex-col gap-1.5">
      <li v-for="(t, i) in todos" :key="i" class="flex items-start gap-2.5 text-sm leading-6">
        <CircleCheck v-if="t.status === 'completed'" aria-label="已完成" class="mt-1 size-4 shrink-0 text-[#2E7D4F]" :stroke-width="1.8" />
        <Spin v-else-if="t.status === 'in_progress'" color="#2F6FDB" :size="14" class="mt-[5px] shrink-0" label="进行中" />
        <Circle v-else aria-label="未开始" class="mt-1 size-4 shrink-0 text-[#B7B3AA]" :stroke-width="1.8" />
        <span :class="cn(t.status === 'completed' && 'text-muted-foreground line-through decoration-[#C9C5BC]', t.status === 'in_progress' && 'font-medium')">
          {{ t.status === "in_progress" && t.active_form ? t.active_form : t.content }}
        </span>
      </li>
    </ul>
  </section>
</template>
