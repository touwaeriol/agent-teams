<script setup lang="ts">
/** A white card with the design's hairline border (14px radius). */
import { cn } from "@/lib/utils";

withDefaults(defineProps<{ class?: string; tone?: "ask" | "danger"; as?: "section" | "div" | "article" | "aside" }>(), { as: "section", class: undefined, tone: undefined });
const BORDER = { ask: "border-[#F0C9A8]", danger: "border-[#F1C8C4]" } as const;
</script>

<template>
  <component :is="as" :class="cn('flex flex-col rounded-[14px] border bg-panel p-5', tone ? BORDER[tone] : 'border-border', $props.class)"><slot /></component>
</template>
