<script setup lang="ts">
// The first lines of a published Markdown document, rendered small for a preview card.
import { computed } from "vue";
import Markdown from "@/components/Markdown.vue";
import { markdownUrl } from "@/lib/artifacts";
import { useText } from "@/pages/design/viewer";

const props = defineProps<{ url: string; baseUrl: string }>();
const { text } = useText(() => props.url);
// only the head is visible; keep the render cheap for long documents
const head = computed(() => (text.value ?? "").slice(0, 1200));
</script>

<template>
  <span v-if="text === undefined" class="block" aria-hidden="true"><slot /></span>
  <span v-else class="block w-[250%] origin-top-left scale-[0.4]" aria-hidden="true">
    <Markdown :source="head" :url-transform="markdownUrl(baseUrl)" class="pointer-events-none leading-5 [&_img]:hidden" />
  </span>
</template>
