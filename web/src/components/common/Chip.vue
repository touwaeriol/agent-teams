<script setup lang="ts">
/** Small monospace chip: an employee, node or ref, with an optional status dot. */
import { cn } from "@/lib/utils";
import type { Tone } from "./tone";
import ToneDot from "./ToneDot.vue";

defineProps<{ tone?: Tone; title?: string; class?: string }>();
</script>

<template>
  <span :title="title" :class="cn('inline-flex h-8 items-center gap-1.5 rounded-md bg-raised px-2.5 font-mono text-xs', $props.class)">
    <ToneDot v-if="tone" :tone="tone" />
    <slot />
  </span>
</template>
