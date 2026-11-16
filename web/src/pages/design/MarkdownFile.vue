<script setup lang="ts">
import type { DeliverableFiles } from "@/api/views";
import EmptyState from "@/components/common/EmptyState.vue";
import Loading from "@/components/common/Loading.vue";
import Markdown from "@/components/Markdown.vue";
import { markdownUrl } from "@/lib/artifacts";
import { useText } from "./viewer";

const props = defineProps<{ files: DeliverableFiles }>();
const { text, error } = useText(() => props.files.entry_url);
</script>

<template>
  <EmptyState v-if="error" title="文档加载失败" description="链接可能已过期，刷新页面重试。" />
  <Loading v-else-if="text === undefined" class="flex-1" />
  <article v-else class="mx-auto w-full max-w-[820px] rounded-md bg-panel px-6 py-8 shadow-[0_1px_3px_rgba(0,0,0,0.12)] sm:px-10">
    <Markdown :source="text" :url-transform="markdownUrl(files.base_url)" />
  </article>
</template>
