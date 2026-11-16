<script setup lang="ts">
// The resizable right panel container: drag handle + an aside of the remembered width
// (desktop ≥ lg only; the page decides what goes inside).
import { usePanelWidth } from "./usePanelWidth";
import ResizeHandle from "./ResizeHandle.vue";

const props = defineProps<{ userId: string; label: string; handleLabel: string }>();
const [width, setWidth] = usePanelWidth(() => props.userId);
</script>

<template>
  <ResizeHandle :width="width" :label="handleLabel" @change="setWidth" />
  <aside :aria-label="label" :style="{ width: `${width}px` }" class="hidden h-full shrink-0 flex-col border-l bg-panel lg:flex"><slot :width="width" :set-width="setWidth" /></aside>
</template>
