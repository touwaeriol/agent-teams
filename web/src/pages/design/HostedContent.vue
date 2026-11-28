<script setup lang="ts">
// Hosted files from short-lived signed URLs: HTML in a sandboxed iframe (no allow-same-origin
// unless files have their own origin), Markdown rendered safely, images inline, else a file list.
import { $t } from "@/i18n";
import type { DeliverableFiles } from "@/api/views";
import EmptyState from "@/components/common/EmptyState.vue";
import Loading from "@/components/common/Loading.vue";
import { fileUrl, frameSandbox, renderLabel } from "@/lib/artifacts";
import { formatBytes } from "@/lib/bytes";
import MarkdownFile from "./MarkdownFile.vue";
import { FRAME_CLASS } from "./frame";

defineProps<{ title: string; files?: DeliverableFiles; loading: boolean; failed: boolean }>();
</script>

<template>
  <Loading v-if="loading" class="flex-1" />
  <EmptyState v-else-if="failed || !files" :title="$t('文件加载失败')" :description="$t('刷新页面重试。')" />
  <iframe
    v-else-if="files.render === 'html'"
    :key="files.entry_url"
    :title="title"
    :src="files.entry_url"
    :sandbox="frameSandbox(files.isolated)"
    referrerpolicy="no-referrer"
    :class="FRAME_CLASS"
  />
  <MarkdownFile v-else-if="files.render === 'markdown'" :files="files" />
  <div v-else-if="files.render === 'image'" class="flex flex-1 items-start justify-center overflow-auto">
    <img :src="files.entry_url" :alt="title" referrerpolicy="no-referrer" class="max-w-full rounded-md bg-white shadow-[0_1px_3px_rgba(0,0,0,0.12)]" />
  </div>
  <div v-else class="flex w-full flex-col gap-2 rounded-md bg-panel p-5">
    <span class="text-sm font-medium">{{ $t("{render} · {filesCount} 个文件", { render: renderLabel(files.render), filesCount: files.files.length }) }}</span>
    <a
      v-for="f in files.files"
      :key="f.path"
      :href="fileUrl(files.base_url, f.path)"
      target="_blank"
      rel="noreferrer noopener"
      class="flex justify-between gap-3 text-[13px] hover:underline"
    >
      <span class="truncate font-mono">{{ f.path }}</span>
      <span class="shrink-0 text-muted-foreground">{{ formatBytes(f.size) }}</span>
    </a>
  </div>
</template>
