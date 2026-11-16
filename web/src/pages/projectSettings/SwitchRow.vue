<script setup lang="ts">
import Row from "./Row.vue";

defineProps<{ title: string; desc?: string; checked: boolean }>();
const emit = defineEmits<{ change: [value: boolean] }>();
</script>

<template>
  <Row :title="title" :desc="desc">
    <el-switch :model-value="checked" :aria-label="title" class="at-switch-blue" @change="(v: string | number | boolean) => emit('change', Boolean(v))" />
  </Row>
</template>
