<script setup lang="ts">
/** Outlined status pill (进行中 / 等你 / 待验收 …); in-progress pills lead with a spinner. */
import { cn } from "@/lib/utils";
import Spin from "./Spin.vue";

defineProps<{ tone: string; label?: string; class?: string; spin?: boolean }>();
</script>

<template>
  <span :class="cn('inline-flex items-center gap-1.5 whitespace-nowrap rounded-full border px-2.5 py-0.5 text-xs', $props.class)" :style="{ borderColor: tone, color: tone }">
    <Spin v-if="spin" :color="tone" :size="10" />
    <slot>{{ label }}</slot>
  </span>
</template>
