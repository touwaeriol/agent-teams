<script setup lang="ts">
/** The in-progress mark: a ring with a rotating arc (`.at-spin`; a gentle pulse with reduced motion). */
import { computed } from "vue";
import { cn } from "@/lib/utils";

const props = withDefaults(defineProps<{ color: string; size?: number; class?: string; label?: string }>(), { size: 8, class: undefined, label: undefined });
const px = computed(() => Math.max(props.size, 8));
</script>

<template>
  <svg
    :width="px"
    :height="px"
    viewBox="0 0 16 16"
    :role="label ? 'img' : undefined"
    :aria-label="label"
    :aria-hidden="label ? undefined : true"
    data-spin=""
    :class="cn('at-spin shrink-0', props.class)"
  >
    <circle cx="8" cy="8" r="6" fill="none" :stroke="color" stroke-opacity="0.22" stroke-width="2.6" />
    <path d="M8 2a6 6 0 0 1 6 6" fill="none" :stroke="color" stroke-width="2.6" stroke-linecap="round" />
  </svg>
</template>
