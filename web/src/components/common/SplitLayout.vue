<script setup lang="ts">
// Main column plus a side column that stacks below it on narrow screens.
import { cn } from "@/lib/utils";

withDefaults(defineProps<{ sideWidth?: string; class?: string }>(), { sideWidth: "400px" });
</script>

<template>
  <div :class="cn('flex flex-wrap items-start gap-5', $props.class)">
    <div class="flex min-w-0 flex-[999_1_560px] flex-col gap-4"><slot name="main" /></div>
    <aside class="w-full min-w-0 lg:sticky lg:top-24" :style="{ flex: `1 1 ${sideWidth}` }"><slot name="side" /></aside>
  </div>
</template>
