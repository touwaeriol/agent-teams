<script setup lang="ts">
/** A state's mark in lists: spinner while in progress, static dot otherwise. */
import Dot from "./Dot.vue";
import Spin from "./Spin.vue";

withDefaults(defineProps<{ look: { tone: string; spin?: boolean }; size?: number }>(), { size: 7 });
</script>

<template>
  <Spin v-if="look.spin" :color="look.tone" :size="size + 3" />
  <Dot v-else :color="look.tone" :size="size" />
</template>
