<script setup lang="ts">
import { $t } from "@/i18n";
/** Replaces the project-chat composer while selecting messages: 已选 N 条 · 转发到讨论串 · 取消 (Esc). */
import { Forward } from "lucide-vue-next";
import { onBeforeUnmount, onMounted } from "vue";
import { MAX_FORWARD } from "@/lib/forward";

defineProps<{ count: number }>();
const emit = defineEmits<{ forward: []; cancel: [] }>();
// Esc leaves the selection (unless a dialog or the image preview is open: it closes first).
const onKey = (e: KeyboardEvent) => e.key === "Escape" && !document.querySelector(".el-overlay-dialog, .el-image-viewer__wrapper") && emit("cancel");
onMounted(() => window.addEventListener("keydown", onKey));
onBeforeUnmount(() => window.removeEventListener("keydown", onKey));
</script>

<template>
  <div class="shrink-0 bg-background px-3 pb-3 sm:px-6 sm:pb-5">
    <div class="mx-auto flex max-w-[700px] items-center gap-3 rounded-2xl border bg-panel px-4 py-3 shadow-sm" role="toolbar" :aria-label="$t('转发消息')">
      <span class="text-sm" data-count>{{ $t("已选 {count} 条", { count }) }}</span>
      <span v-if="count >= MAX_FORWARD" class="text-xs text-muted-foreground">{{ $t("最多 {MAX_FORWARD} 条", { MAX_FORWARD }) }}</span>
      <span class="flex-1" />
      <el-button type="primary" :disabled="count === 0" @click="emit('forward')">
        <Forward class="mr-1.5 size-4" />
        {{ $t("转发到讨论串") }}
      </el-button>
      <el-button @click="emit('cancel')">{{ $t("取消") }}</el-button>
    </div>
  </div>
</template>
