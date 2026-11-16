<script setup lang="ts">
/** "选择文件夹": picks a folder and emits its text files (hidden files left out). */
import { FolderUp } from "lucide-vue-next";
import { ref } from "vue";
import type { SkillFile } from "@/api/views";
import { readFolder } from "./upload";

defineProps<{ pending: boolean }>();
const emit = defineEmits<{ files: [files: SkillFile[]] }>();
const input = ref<HTMLInputElement | null>(null);

async function picked(e: Event) {
  const el = e.target as HTMLInputElement;
  const list = el.files ? Array.from(el.files) : [];
  el.value = "";
  if (list.length > 0) emit("files", await readFolder(list));
}
</script>

<template>
  <input ref="input" type="file" multiple hidden webkitdirectory directory @change="picked" />
  <el-button size="small" class="!ml-0" :disabled="pending" @click="input?.click()"><FolderUp /> 选择文件夹</el-button>
</template>
