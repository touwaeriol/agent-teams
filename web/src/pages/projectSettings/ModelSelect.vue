<script setup lang="ts">
/** A project model field: "" follows the roles, else any typed id (suggestions: the roles' models and common ids). */
import ModelPicker from "@/components/app/models/ModelPicker.vue";
import type { Runtime } from "@/lib/models";

withDefaults(defineProps<{ value: string; label: string; runtime: Runtime; placeholder?: string }>(), { placeholder: "跟随岗位配置" });
const emit = defineEmits<{ change: [value: string] }>();
</script>

<template>
  <ModelPicker :model-value="value" :runtime="runtime" :label="label" :placeholder="placeholder" class="at-select-36 at-raised" @update:model-value="(v: string) => emit('change', v)" />
</template>
