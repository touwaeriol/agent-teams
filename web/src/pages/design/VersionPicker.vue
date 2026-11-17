<script setup lang="ts">
import { computed } from "vue";
import type { DeliverableFiles } from "@/api/views";
import { pickedVersion, versionLabel } from "./viewer";

const props = defineProps<{ files: DeliverableFiles; threadNo?: number | null }>();
const emit = defineEmits<{ pick: [version: number] }>();
const latest = computed(() => props.files.versions[0]?.version ?? props.files.version);
</script>

<template>
  <el-select
    :model-value="files.version"
    aria-label="版本"
    size="small"
    class="at-version-select"
    popper-class="at-select-popper"
    :fit-input-width="false"
    @update:model-value="(v: number) => emit('pick', pickedVersion(files, v))"
  >
    <el-option v-for="v in files.versions" :key="v.version" :value="v.version" :label="versionLabel(v, latest, threadNo)" />
  </el-select>
</template>

<style>
.at-version-select { width: auto; }
.at-version-select .el-select__wrapper { min-height: 32px; height: 32px; font-size: 13px; gap: 8px; background: transparent; }
.at-version-select .el-select__selection { flex: 0 1 auto; }
</style>
