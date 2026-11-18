<script setup lang="ts">
import { computed } from "vue";
import { RouterLink } from "vue-router";
import type { ThreadView } from "@/api/views";
import { DARK, scopes } from "./logic";

const props = defineProps<{ pid: string; name: string; threads: ThreadView[]; focus: number | null; replaying: boolean; summary?: string }>();
const emit = defineEmits<{ focus: [no: number | null] }>();
const list = computed(() => scopes(props.threads));
const style = (on: boolean) => ({ background: on ? "#F0EDE6" : "transparent", color: on ? "#1F1E1C" : DARK.text, fontWeight: on ? 600 : 400 });
</script>

<template>
  <header class="flex flex-wrap items-center justify-between gap-2.5">
    <span class="flex items-center gap-2.5">
      <RouterLink :to="`/projects/${pid}`" class="text-[13px] text-[#A8A49B] hover:text-white">{{ name }}</RouterLink>
      <span class="text-[#5C5F6B]">/</span>
      <span class="text-base font-semibold">办公室</span>
      <span class="flex items-center gap-1.5 text-xs" :style="{ color: replaying ? '#F2C14E' : '#8FD3A2' }">
        <span class="size-[7px] rounded-full" :style="{ background: replaying ? '#F2C14E' : '#5FD08A' }" />
        {{ replaying ? "回放" : "实时" }}
      </span>
      <span v-if="summary" class="text-xs text-[#A8A49B]" data-testid="office-summary">{{ summary }}</span>
    </span>
    <div role="group" aria-label="显示范围" class="flex flex-wrap gap-1 rounded-[10px] bg-[#2A2D36] p-[3px]">
      <button v-for="[id, label] in list" :key="label" type="button" :aria-pressed="focus === id" class="h-[30px] rounded-lg px-3 text-[13px]" :style="style(focus === id)" @click="emit('focus', id)">
        {{ label }}
      </button>
    </div>
  </header>
</template>
