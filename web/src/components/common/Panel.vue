<script setup lang="ts">
// The surface every block sits on. With a title it gets a header row
// (icon slot, title, optional action slot on the right).
import { cn } from "@/lib/utils";

const TONE = { default: "", highlight: "border-brand/60", danger: "border-destructive/30" };
const PAD = { sm: "p-4 gap-2.5", md: "p-5 gap-4", none: "" };
withDefaults(
  defineProps<{
    title?: string;
    description?: string;
    tone?: keyof typeof TONE;
    padding?: keyof typeof PAD;
    as?: "section" | "article" | "div" | "aside" | "nav";
    label?: string;
    class?: string;
  }>(),
  { tone: "default", padding: "md", as: "section" },
);
</script>

<template>
  <component :is="as" :aria-label="label" :class="cn('flex flex-col rounded-xl border bg-panel', PAD[padding], TONE[tone], $props.class)">
    <div v-if="title || $slots.title || $slots.action" class="flex flex-wrap items-start justify-between gap-2">
      <div class="flex min-w-0 flex-col gap-1">
        <h2 v-if="title || $slots.title" class="flex items-center gap-2 text-[15px] font-semibold [&_svg]:size-4 [&_svg]:text-muted-foreground">
          <slot name="icon" />
          <slot name="title">{{ title }}</slot>
        </h2>
        <p v-if="description || $slots.description" class="text-[13px] leading-5 text-muted-foreground"><slot name="description">{{ description }}</slot></p>
      </div>
      <slot name="action" />
    </div>
    <slot />
  </component>
</template>
