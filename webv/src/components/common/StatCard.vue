<script setup lang="ts">
/** One number or short fact with its label. */
defineProps<{ label: string; value?: string | number }>();
</script>

<template>
  <div class="flex flex-col gap-1 rounded-xl border bg-panel px-4 py-3.5">
    <span class="text-xs text-muted-foreground">{{ label }}</span>
    <span class="truncate text-xl font-semibold"><slot>{{ value }}</slot></span>
  </div>
</template>
