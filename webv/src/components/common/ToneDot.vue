<script setup lang="ts">
import { cn } from "@/lib/utils";
import { DOT_TONE, type Tone } from "./tone";

withDefaults(defineProps<{ tone?: Tone; class?: string }>(), { tone: "neutral" });
</script>

<template>
  <span aria-hidden="true" :class="cn('inline-block size-2 shrink-0 rounded-full', DOT_TONE[tone], $props.class)" />
</template>
