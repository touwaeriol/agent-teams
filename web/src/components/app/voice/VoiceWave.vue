<script setup lang="ts">
/**
 * Live microphone level beside the mic button: a row of thin bars, the newest on the right (next to
 * the button), scrolling left 20 times a second. Silence is a row of dots; speech makes the bars
 * jump. With prefers-reduced-motion the bars don't scroll or animate:
 * they all show the current level, updated a few times a second.
 */
import { onBeforeUnmount, ref, watch } from "vue";
import { barHeight, WAVE_TICK_MS } from "./wave";

const props = withDefaults(defineProps<{ level: number; bars?: number; max?: number }>(), { bars: 12, max: 18 });

const reduced = typeof window !== "undefined" && Boolean(window.matchMedia?.("(prefers-reduced-motion: reduce)").matches);
const levels = ref<number[]>(Array.from({ length: props.bars }, () => 0));

// Sampled on our own clock rather than on change: a level that stays put (true silence) still
// scrolls the louder readings out instead of freezing them on screen.
const timer = setInterval(
  () => {
    const level = props.level;
    levels.value = reduced ? levels.value.map(() => level) : [...levels.value.slice(1 - props.bars), level];
  },
  reduced ? WAVE_TICK_MS * 4 : WAVE_TICK_MS,
);
onBeforeUnmount(() => clearInterval(timer));
watch(
  () => props.bars,
  (n) => (levels.value = [...Array.from({ length: n }, () => 0), ...levels.value].slice(-n)),
);
</script>

<template>
  <span class="flex shrink-0 items-center gap-[2px]" :style="{ height: `${max}px` }" aria-hidden="true" data-testid="voice-wave">
    <span
      v-for="(l, i) in levels"
      :key="i"
      :class="['w-[2.5px] rounded-full bg-[#2F6FDB]', !reduced && 'transition-[height] duration-75 ease-out']"
      :style="{ height: `${barHeight(l, max)}px`, opacity: 0.45 + 0.55 * ((i + 1) / levels.length) }"
      data-testid="voice-wave-bar"
    />
  </span>
</template>
