<script setup lang="ts">
/** Label / value rows (values are monospace unless `mono: false`). */
import { cn } from "@/lib/utils";

defineProps<{ items: { label: string; value: string | number; mono?: boolean }[] }>();
</script>

<template>
  <dl class="flex flex-col gap-1.5 text-[13px]">
    <div v-for="(item, i) in items" :key="i" class="flex justify-between gap-3 border-b border-border/60 pb-1.5">
      <dt class="text-muted-foreground">{{ item.label }}</dt>
      <dd :class="cn('text-right', item.mono !== false && 'font-mono text-xs')">{{ item.value }}</dd>
    </div>
  </dl>
</template>
