<script setup lang="ts">
/** A group of fields with a caption, on a slightly raised surface. */
import { cn } from "@/lib/utils";

defineProps<{ legend: string; class?: string }>();
</script>

<template>
  <fieldset :class="cn('flex flex-col gap-3 rounded-xl border bg-raised/40 p-4', $props.class)">
    <legend class="px-1 text-[13px] font-medium text-foreground/85">{{ legend }}</legend>
    <slot />
  </fieldset>
</template>
