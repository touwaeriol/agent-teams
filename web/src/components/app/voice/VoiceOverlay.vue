<script setup lang="ts">
/**
 * Paints the box's text while speech is being recognised, with the not-yet-final words grey.
 * Sits exactly over the textarea/input (same font, padding and wrapping); the field's own text is
 * made transparent meanwhile, so the caret and selection still come from the real field.
 */
import type { Part } from "@/lib/voice/transcript";

defineProps<{ parts: Part[]; scrollTop: number; scrollLeft: number; class?: string }>();
</script>

<template>
  <div aria-hidden="true" :class="['pointer-events-none absolute inset-0 overflow-hidden', $props.class]" data-testid="voice-overlay">
    <div :style="{ transform: `translate(${-scrollLeft}px, ${-scrollTop}px)` }"
      ><span v-for="(p, i) in parts" :key="i" :class="p.partial ? 'text-muted-foreground/70' : 'text-foreground'" :data-partial="p.partial || undefined">{{ p.text }}</span></div
    >
  </div>
</template>
