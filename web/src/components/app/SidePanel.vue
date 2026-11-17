<script setup lang="ts">
// The resizable right panel container: drag handle + an aside of the given width
// (desktop ≥ lg only; the page owns the width and decides what goes inside).
import ResizeHandle from "./ResizeHandle.vue";

defineProps<{ width: number; label: string; handleLabel: string; min?: number; max?: number }>();
const emit = defineEmits<{ change: [width: number]; reset: [] }>();
</script>

<template>
  <ResizeHandle :width="width" :min="min" :max="max" :label="handleLabel" @change="(w) => emit('change', w)" @reset="emit('reset')" />
  <aside :aria-label="label" tabindex="-1" :style="{ width: `${width}px` }" class="hidden h-full min-w-0 shrink-0 flex-col border-l bg-panel outline-none lg:flex"><slot /></aside>
</template>
