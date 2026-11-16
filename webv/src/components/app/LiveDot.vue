<script setup lang="ts">
/** Someone working: a spinner; otherwise a static dot. */
import Dot from "./Dot.vue";
import Spin from "./Spin.vue";

withDefaults(defineProps<{ color: string; on: boolean; size?: number; class?: string }>(), { size: 8, class: undefined });
</script>

<template>
  <Spin v-if="on" :color="color" :size="size + 2" :class="$props.class" />
  <Dot v-else :color="color" :size="size" :class="$props.class" />
</template>
