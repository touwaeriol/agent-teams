<script setup lang="ts">
// The frame of a waiting card: head row (slot `head`), optional code line, then the body.
import { cn } from "@/lib/utils";
import { CODE, FRAME, type CardVariant } from "./variant";

defineProps<{ variant: CardVariant; code?: string; label: string }>();
</script>

<template>
  <section :aria-label="label" :class="cn('flex flex-col gap-2.5 rounded-xl border', FRAME[variant])">
    <div class="flex items-start justify-between gap-2 text-[13px]"><slot name="head" /></div>
    <code v-if="code" :class="cn('block whitespace-pre-wrap break-words [overflow-wrap:anywhere] rounded-lg px-2.5 py-2 font-mono text-xs', CODE[variant])">{{ code }}</code>
    <slot />
  </section>
</template>
