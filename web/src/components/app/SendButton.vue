<script setup lang="ts">
/** Ink square send button (submits its form). */
import { ArrowUp } from "lucide-vue-next";
import Spinner from "@/components/common/Spinner.vue";

withDefaults(defineProps<{ label: string; busy?: boolean; disabled?: boolean; size?: number }>(), { size: 32 });
</script>

<template>
  <button
    type="submit"
    :aria-label="label"
    :disabled="disabled || busy"
    class="flex shrink-0 items-center justify-center rounded-lg bg-primary text-primary-foreground transition-opacity disabled:opacity-40"
    :style="{ width: `${size}px`, height: `${size}px` }"
  >
    <Spinner v-if="busy" class="size-4" />
    <ArrowUp v-else class="size-4" :stroke-width="2" />
  </button>
</template>
