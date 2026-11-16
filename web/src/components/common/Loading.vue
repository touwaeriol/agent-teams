<script setup lang="ts">
/** A block that stands in for content while it loads. */
import { cn } from "@/lib/utils";
import Spinner from "./Spinner.vue";

defineProps<{ class?: string }>();
</script>

<template>
  <div :class="cn('flex items-center justify-center p-10 text-muted-foreground', $props.class)"><Spinner /></div>
</template>
