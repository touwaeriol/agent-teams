<script setup lang="ts">
import type { CardVariant } from "./variant";

defineProps<{ variant: CardVariant; count: number }>();
</script>

<template>
  <span v-if="variant === 'mobile'" class="grid gap-1.5" :style="{ gridTemplateColumns: `repeat(${count}, minmax(0, 1fr))` }"><slot /></span>
  <span v-else class="flex flex-wrap gap-2"><slot /></span>
</template>
