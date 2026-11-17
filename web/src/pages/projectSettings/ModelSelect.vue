<script setup lang="ts">
import ModelPicker from "@/components/app/models/ModelPicker.vue";

defineProps<{ value: string; label: string }>();
const emit = defineEmits<{ change: [value: string] }>();
</script>

<template>
  <ModelPicker :model-value="value" runtime="claude" :label="label" empty-label="跟随岗位设置" class="at-select-36 at-raised" @update:model-value="(v: string) => emit('change', v)" />
</template>
