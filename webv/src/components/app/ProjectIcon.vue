<script setup lang="ts">
/** The project's icon: an emoji, or its initial on a soft tile. */
import { computed } from "vue";
import type { ProjectView } from "@/api/views";
import { iconLook } from "@/lib/projectIcon";
import { cn } from "@/lib/utils";

const props = withDefaults(defineProps<{ project: Pick<ProjectView, "name"> & { icon?: string }; size?: number; class?: string }>(), { size: 24, class: undefined });
const look = computed(() => iconLook(props.project.icon, props.project.name));
const box = computed(() => ({
  width: `${props.size}px`,
  height: `${props.size}px`,
  fontSize: `${Math.round(props.size * (look.value.kind === "emoji" ? 0.62 : 0.5))}px`,
}));
</script>

<template>
  <span v-if="look.kind === 'emoji'" aria-hidden="true" :class="cn('flex shrink-0 items-center justify-center rounded-md bg-raised leading-none', props.class)" :style="box">
    {{ look.emoji }}
  </span>
  <span v-else aria-hidden="true" :class="cn('flex shrink-0 items-center justify-center rounded-md font-semibold', props.class)" :style="{ ...box, background: look.bg, color: look.fg }">
    {{ look.letter }}
  </span>
</template>
