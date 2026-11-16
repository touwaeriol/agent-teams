<script setup lang="ts">
/** Thin progress bar (`Bar` in the React app). */
withDefaults(defineProps<{ pct: string; tone: string; height?: number }>(), { height: 4 });
</script>

<template>
  <span class="flex flex-1 rounded-sm bg-[#EFEDE8]" :style="{ height: `${height}px` }">
    <span class="rounded-sm" :style="{ width: pct, background: tone }" />
  </span>
</template>
