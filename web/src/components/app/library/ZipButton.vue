<script setup lang="ts">
/** "上传 … zip": picks one zip file and emits it as base64. */
import { Upload } from "lucide-vue-next";
import { ref } from "vue";
import { zipOf } from "./upload";

defineProps<{ label: string; pending: boolean }>();
const emit = defineEmits<{ zip: [zip: string] }>();
const input = ref<HTMLInputElement | null>(null);

async function picked(e: Event) {
  const el = e.target as HTMLInputElement;
  const f = el.files?.[0];
  el.value = "";
  if (f) emit("zip", await zipOf(f));
}
</script>

<template>
  <input ref="input" type="file" accept=".zip,application/zip" hidden @change="picked" />
  <el-button size="small" :disabled="pending" @click="input?.click()"><Upload /> {{ label }}</el-button>
</template>
