<script setup lang="ts">
/** Inline loading indicator; inherits the text color. `decorative`: next to text that already says it (no status role). */
import { cn } from "@/lib/utils";

defineProps<{ class?: string; decorative?: boolean }>();
</script>

<template>
  <span
    :class="cn('inline-block size-4 animate-spin rounded-full border-2 border-current border-r-transparent', $props.class)"
    :role="decorative ? undefined : 'status'"
    :aria-label="decorative ? undefined : '加载中'"
    :aria-hidden="decorative || undefined"
  />
</template>
