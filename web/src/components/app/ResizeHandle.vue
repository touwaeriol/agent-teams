<script setup lang="ts">
// Drag handle between the chat and the right panel: drag to resize, arrow keys nudge,
// double-click / Home / Enter reset. The parent clamps and remembers the width (lib/panelWidth.ts).
import { $t } from "@/i18n";
import { ref } from "vue";
import { dragWidth } from "@/lib/panelWidth";
import { cn } from "@/lib/utils";

const props = defineProps<{ width: number; label: string; min?: number; max?: number }>();
const emit = defineEmits<{ change: [width: number]; reset: [] }>();
let drag: { x: number; w: number } | null = null;
const active = ref(false);

function down(e: PointerEvent) {
  e.preventDefault();
  (e.currentTarget as HTMLElement).setPointerCapture?.(e.pointerId);
  drag = { x: e.clientX, w: props.width };
  active.value = true;
}
function move(e: PointerEvent) {
  if (drag) emit("change", dragWidth(drag.w, drag.x, e.clientX));
}
function end() {
  drag = null;
  active.value = false;
}
const KEYS: Record<string, (step: number) => number | null> = {
  ArrowLeft: (s) => props.width + s,
  ArrowRight: (s) => props.width - s,
  Home: () => null,
  Enter: () => null,
};
function key(e: KeyboardEvent) {
  const next = KEYS[e.key];
  if (!next) return;
  e.preventDefault();
  const w = next(e.shiftKey ? 64 : 16);
  if (w === null) emit("reset");
  else emit("change", w);
}
</script>

<template>
  <div
    role="separator"
    aria-orientation="vertical"
    :aria-label="label"
    :aria-valuenow="width"
    :aria-valuemin="min"
    :aria-valuemax="max"
    tabindex="0"
    :title="$t('拖动调整宽度，双击恢复默认')"
    class="group relative z-10 -mr-[3px] hidden w-[6px] shrink-0 cursor-col-resize touch-none outline-none lg:block"
    @pointerdown="down"
    @pointermove="move"
    @pointerup="end"
    @pointercancel="end"
    @dblclick="emit('reset')"
    @keydown="key"
  >
    <span :class="cn('absolute inset-y-0 left-[2px] w-[2px] transition-colors group-hover:bg-[#2F6FDB]/50 group-focus-visible:bg-[#2F6FDB]', active && 'bg-[#2F6FDB]')" />
  </div>
</template>
