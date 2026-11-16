<script setup lang="ts">
/** A status shown as a colored pill; `spinning` for work in progress. */
import { cn } from "@/lib/utils";
import { BADGE_TONE, type Tone } from "./tone";
import ToneDot from "./ToneDot.vue";

defineProps<{ tone: Tone; label: string; dot?: boolean; spinning?: boolean; class?: string }>();
</script>

<template>
  <span :class="cn('inline-flex items-center gap-1.5 whitespace-nowrap rounded-full border px-2.5 py-0.5 text-xs font-medium', BADGE_TONE[tone], $props.class)">
    <span v-if="spinning" class="inline-block size-2.5 animate-spin rounded-full border-2 border-current border-r-transparent" aria-hidden="true" />
    <ToneDot v-if="dot" :tone="tone" />
    <slot>{{ label }}</slot>
  </span>
</template>
