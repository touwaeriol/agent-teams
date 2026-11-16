<script setup lang="ts">
import { computed } from "vue";
import type { DeliverableView } from "@/api/views";
import EmptyState from "@/components/common/EmptyState.vue";
import { embeddableUrl } from "@/lib/safeurl";
import { FRAME_CLASS } from "./frame";

const props = defineProps<{ item: DeliverableView }>();
const src = computed(() => embeddableUrl(props.item.url, window.location.origin));
</script>

<template>
  <EmptyState v-if="!src" title="这个链接不能嵌入预览" description="用右上角“新窗口打开”查看。" />
  <iframe v-else :title="item.title" :src="src" sandbox="allow-scripts allow-same-origin allow-popups" :class="FRAME_CLASS" />
</template>
