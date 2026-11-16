<script setup lang="ts">
import { cn } from "@/lib/utils";

withDefaults(defineProps<{ color: string; size?: number; class?: string }>(), { size: 7, class: undefined });
</script>

<template>
  <span aria-hidden="true" :class="cn('inline-block shrink-0 rounded-full', $props.class)" :style="{ width: `${size}px`, height: `${size}px`, background: color }" />
</template>
