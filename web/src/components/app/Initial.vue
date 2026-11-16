<script setup lang="ts">
/** Initial-letter tile. */
import { computed } from "vue";
import { cn } from "@/lib/utils";

const props = withDefaults(defineProps<{ name: string; bg: string; fg: string; size?: number; round?: boolean }>(), { size: 22 });
const letter = computed(() => (Array.from(props.name.trim())[0] ?? "?").toUpperCase());
</script>

<template>
  <span
    aria-hidden="true"
    :class="cn('flex shrink-0 items-center justify-center font-semibold', round ? 'rounded-full' : 'rounded-md')"
    :style="{ width: `${size}px`, height: `${size}px`, background: bg, color: fg, fontSize: `${size >= 30 ? 14 : 12}px` }"
  >
    {{ letter }}
  </span>
</template>
