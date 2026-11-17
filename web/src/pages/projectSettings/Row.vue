<script setup lang="ts">
/** A settings row: title (and description) on the left, the control on the right. */
import { cn } from "@/lib/utils";

defineProps<{ title?: string; desc?: string; class?: string }>();
</script>

<template>
  <div :class="cn('flex flex-wrap items-center justify-between gap-x-4 gap-y-2 py-2.5', $props.class)">
    <span class="flex min-w-0 flex-1 basis-48 flex-col gap-0.5">
      <span class="break-words"><slot name="title">{{ title }}</slot></span>
      <span v-if="desc" class="break-words text-[13px] leading-5 text-muted-foreground">{{ desc }}</span>
    </span>
    <span v-if="$slots.default" class="ml-auto flex max-w-full shrink-0 items-center gap-2"><slot /></span>
  </div>
</template>
