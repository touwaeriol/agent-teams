<script setup lang="ts">
/** A small on/off chip (skills, plugins, outfits, accessories). */
import { cn } from "@/lib/utils";

defineProps<{ on: boolean }>();
const emit = defineEmits<{ click: [] }>();
</script>

<template>
  <button type="button" :aria-pressed="on" :class="cn('h-6 rounded-md border px-2 text-xs', on ? 'border-foreground bg-foreground/5' : 'border-input bg-white')" @click="emit('click')">
    <slot />
  </button>
</template>
