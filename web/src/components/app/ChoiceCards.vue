<script setup lang="ts" generic="T extends string">
/** Segmented choice used for work mode (worktree / clone). `v-model`. */
import { cn } from "@/lib/utils";

defineProps<{ options: { id: T; title: string; desc: string }[]; label: string }>();
const value = defineModel<T>({ required: true });
</script>

<template>
  <div role="radiogroup" :aria-label="label" class="grid gap-3 sm:grid-cols-2">
    <button
      v-for="o in options"
      :key="o.id"
      type="button"
      role="radio"
      :aria-checked="o.id === value"
      :class="cn('flex flex-col gap-1.5 rounded-xl border-[1.5px] px-4 py-3.5 text-left', o.id === value ? 'border-foreground bg-background' : 'border-border bg-panel hover:border-input')"
      @click="value = o.id"
    >
      <span class="font-semibold">{{ o.title }}</span>
      <span class="text-xs leading-[19px] text-[#4A4843]">{{ o.desc }}</span>
    </button>
  </div>
</template>
