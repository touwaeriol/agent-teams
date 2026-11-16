<script setup lang="ts">
// Drag handle between the chat and the right panel: drag to resize, arrow keys nudge,
// double-click resets. The width is clamped and remembered per user (lib/panelWidth.ts).
import { ref } from "vue";
import { dragWidth, PANEL_DEFAULT } from "@/lib/panelWidth";
import { cn } from "@/lib/utils";

const props = defineProps<{ width: number; label: string }>();
const emit = defineEmits<{ change: [width: number] }>();
let drag: { x: number; w: number } | null = null;
const active = ref(false);

function down(e: PointerEvent) {
  e.preventDefault();
  (e.currentTarget as HTMLElement).setPointerCapture(e.pointerId);
  drag = { x: e.clientX, w: props.width };
  active.value = true;
}
function move(e: PointerEvent) {
  if (drag) emit("change", dragWidth(drag.w, drag.x, e.clientX, window.innerWidth));
}
function end() {
  drag = null;
  active.value = false;
}
const KEYS: Record<string, (step: number) => number> = {
  ArrowLeft: (s) => props.width + s,
  ArrowRight: (s) => props.width - s,
  Home: () => PANEL_DEFAULT,
  Enter: () => PANEL_DEFAULT,
};
function key(e: KeyboardEvent) {
  const next = KEYS[e.key];
  if (!next) return;
  e.preventDefault();
  emit("change", next(e.shiftKey ? 64 : 16));
}
</script>

<template>
  <div
    role="separator"
    aria-orientation="vertical"
    :aria-label="label"
    :aria-valuenow="width"
    tabindex="0"
    title="拖动调整宽度，双击恢复默认"
    class="group relative z-10 -mr-[3px] hidden w-[6px] shrink-0 cursor-col-resize touch-none outline-none lg:block"
    @pointerdown="down"
    @pointermove="move"
    @pointerup="end"
    @pointercancel="end"
    @dblclick="emit('change', PANEL_DEFAULT)"
    @keydown="key"
  >
    <span :class="cn('absolute inset-y-0 left-[2px] w-[2px] transition-colors group-hover:bg-[#2F6FDB]/50 group-focus-visible:bg-[#2F6FDB]', active && 'bg-[#2F6FDB]')" />
  </div>
</template>
