<script setup lang="ts">
/** Inline loading indicator; inherits the text color. */
import { cn } from "@/lib/utils";

defineProps<{ class?: string }>();
</script>

<template>
  <span :class="cn('inline-block size-4 animate-spin rounded-full border-2 border-current border-r-transparent', $props.class)" role="status" aria-label="加载中" />
</template>
