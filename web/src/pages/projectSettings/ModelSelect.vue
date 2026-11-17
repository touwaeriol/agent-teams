<script setup lang="ts">
/** A project model field: "" follows the roles, else one of the user's models of the runtime. */
import ModelPicker from "@/components/app/models/ModelPicker.vue";
import type { Runtime } from "@/lib/models";

defineProps<{ value: string; label: string; runtime: Runtime }>();
const emit = defineEmits<{ change: [value: string] }>();
</script>

<template>
  <ModelPicker :model-value="value" :runtime="runtime" :label="label" empty-label="跟随岗位配置" class="at-select-36 at-raised" @update:model-value="(v: string) => emit('change', v)" />
</template>
