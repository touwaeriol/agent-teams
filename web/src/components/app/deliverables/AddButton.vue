<script setup lang="ts">
import { $t } from "@/i18n";
/** “+ 添加”: pick files to upload into the library. */
import { Plus } from "lucide-vue-next";
import { ref } from "vue";
import { LIBRARY_ACCEPT } from "@/lib/library";
import { useUpload } from "./browser";

const props = defineProps<{ pid: string }>();
const input = ref<HTMLInputElement | null>(null);
const upload = useUpload(() => props.pid);
function picked(e: Event) {
  const el = e.target as HTMLInputElement;
  void upload(el.files ? Array.from(el.files) : null);
  el.value = "";
}
</script>

<template>
  <el-button type="primary" size="small" @click="input?.click()"><Plus /> {{ $t("添加") }}</el-button>
  <input ref="input" type="file" multiple :accept="LIBRARY_ACCEPT" class="hidden" :aria-label="$t('选择要添加的文件')" @change="picked" />
</template>
