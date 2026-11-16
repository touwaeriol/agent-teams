<script setup lang="ts">
import { computed } from "vue";
import { modelOptions } from "@/lib/projectSettings";
import { fromChoice, toChoice } from "./logic";

const props = defineProps<{ value: string; label: string }>();
const emit = defineEmits<{ change: [value: string] }>();
const options = computed(() => modelOptions(props.value));
</script>

<template>
  <el-select :model-value="toChoice(value)" :aria-label="label" class="at-select-36 at-raised" @update:model-value="(v: string) => emit('change', fromChoice(v))">
    <el-option v-for="o in options" :key="toChoice(o.value)" :value="toChoice(o.value)" :label="o.label" />
  </el-select>
</template>
