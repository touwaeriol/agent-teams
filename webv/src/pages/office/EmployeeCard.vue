<script setup lang="ts">
/** The selected person: who, where, what now, the last few things they did. */
import { computed } from "vue";
import { errorText } from "@/api/mutations";
import { useEmployee } from "@/api/queries";
import { memberDoing } from "@/lib/present";
import { DARK, hhmmss } from "./logic";

const props = defineProps<{ id: string | null }>();
const card = useEmployee(() => props.id);
const e = computed(() => card.data.value);
</script>

<template>
  <div v-if="id" class="flex flex-col gap-1 rounded-[10px] border border-[#4A4D59] px-3 py-2.5 text-xs" :style="{ background: DARK.page }">
    <span v-if="!e" :style="{ color: DARK.soft }">{{ card.error.value ? errorText(card.error.value) : "加载中…" }}</span>
    <template v-else>
      <span class="flex justify-between">
        <span class="text-[13px] font-semibold">{{ e.name }}</span>
        <span :style="{ color: DARK.soft }">{{ e.title || e.role }}{{ e.node ? ` · ${e.node.name}` : "" }}</span>
      </span>
      <span class="text-[#8FD3A2]">{{ memberDoing(e) }}</span>
      <span v-if="e.current_task" :style="{ color: DARK.text }">在做：{{ e.current_task.instruction.split("\n")[0] }}</span>
      <span v-for="(a, i) in e.recent_activity.slice(0, 3)" :key="i" class="text-[#7E8190]">{{ hhmmss(a.at) }} {{ a.text }}</span>
    </template>
  </div>
</template>
