<script setup lang="ts">
// Markdown with the app's prose look (same classes as web/src/components/Markdown.tsx).
import { computed } from "vue";
import { renderMarkdown, type UrlTransform } from "@/lib/markdown";
import { cn } from "@/lib/utils";

const props = defineProps<{ source: string; class?: string; urlTransform?: UrlTransform }>();
const Rendered = computed(() => () => renderMarkdown(props.source, props.urlTransform));
const classes = computed(() =>
  cn(
    "prose prose-sm dark:prose-invert max-w-none break-words leading-6",
    "prose-p:my-2 prose-ul:my-2 prose-ol:my-2 prose-li:my-0.5 prose-headings:mb-2 prose-headings:mt-4 prose-headings:text-foreground",
    "prose-a:text-brand prose-strong:text-foreground prose-code:rounded prose-code:bg-raised prose-code:px-1 prose-code:py-0.5 prose-code:font-mono prose-code:text-[12px] prose-code:before:content-none prose-code:after:content-none",
    "prose-pre:scroll-thin prose-pre:rounded-lg prose-pre:border prose-pre:bg-well prose-pre:text-[12px]",
    "prose-table:text-[13px] prose-th:border-b prose-th:border-border prose-td:border-b prose-td:border-border/60",
    "text-foreground [&>*:first-child]:mt-0 [&>*:last-child]:mb-0",
    props.class,
  ),
);
</script>

<template>
  <div :class="classes"><component :is="Rendered" /></div>
</template>
