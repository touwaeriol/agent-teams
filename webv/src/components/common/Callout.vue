<script setup lang="ts">
/** A one-line notice above content: hints, warnings and errors. */
import { cn } from "@/lib/utils";

const TONE = {
  info: "border-info/40 bg-info/10 text-info",
  warn: "border-warn/40 bg-warn/10 text-warn",
  bad: "border-bad/40 bg-bad/10 text-bad",
};
withDefaults(defineProps<{ tone?: keyof typeof TONE; class?: string }>(), { tone: "info" });
</script>

<template>
  <div :class="cn('flex items-start gap-2 rounded-lg border px-3 py-2 text-[13px] leading-5 [&_a]:underline [&_svg]:mt-0.5 [&_svg]:size-4 [&_svg]:shrink-0', TONE[tone], $props.class)">
    <slot name="icon" />
    <div class="min-w-0"><slot /></div>
  </div>
</template>
