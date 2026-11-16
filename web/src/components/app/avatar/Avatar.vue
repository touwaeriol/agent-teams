<script setup lang="ts">
/** An employee's look, drawn with the office renderer's own composer, with its name tag below. */
import { computed } from "vue";
import { avatarPixels, type AvatarInput } from "./layout";

const props = withDefaults(defineProps<AvatarInput & { scale?: number }>(), { scale: 4, raised: false });
const art = computed(() => avatarPixels(props));
const tag = computed(() => props.look.color ?? "#4A4843");
</script>

<template>
  <span class="relative flex flex-col items-center" :style="{ width: `${Math.max(56, art.w * scale)}px` }">
    <svg :width="art.w * scale" :height="art.h * scale" :viewBox="`0 0 ${art.w} ${art.h}`" shape-rendering="crispEdges" role="img" :aria-label="`${label} 的形象`">
      <rect v-for="(p, i) in art.pixels" :key="i" :x="p.x" :y="p.y" width="1" height="1" :fill="p.color" />
    </svg>
    <span class="-mt-0.5 max-w-[64px] truncate rounded border bg-white px-[5px] py-px text-[10px] leading-3 text-foreground" :style="{ borderColor: tag }">{{ label }}</span>
  </span>
</template>
