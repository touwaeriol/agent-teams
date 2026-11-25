<script setup lang="ts">
/** One deliverable as a compact card (icon, title, type, publisher) that opens it: the viewer for hosted files, the link otherwise. */
import { computed } from "vue";
import type { DeliverableView } from "@/api/views";
import { fileIcon } from "@/lib/library";
import { deliverableKind } from "@/lib/present";
import { cn } from "@/lib/utils";
import { CARD } from "./chatcards/style";
import { FILE_ICONS } from "./fileIcons";
import ItemLink from "./ItemLink.vue";

const props = defineProps<{ pid: string; item: DeliverableView }>();
const look = computed(() => deliverableKind(props.item.kind));
const icon = computed(() => FILE_ICONS[fileIcon(props.item)]);
const by = computed(() => (props.item.published_by && props.item.published_by !== "user" ? ` · ${props.item.published_by} 发布` : ""));
const cls = cn(CARD, "flex w-full max-w-[420px] items-center gap-3 px-3 py-2 hover:border-input");
</script>

<template>
  <ItemLink :pid="pid" :item="item" :class="cls" :aria-label="`打开交付物 ${item.title}`" data-deliverable-chip :data-id="item.id">
    <span class="flex size-9 shrink-0 items-center justify-center rounded-xl" :style="{ background: look.bg, color: look.fg }">
      <component :is="icon" aria-hidden="true" class="size-[18px]" :stroke-width="1.7" />
    </span>
    <span class="flex min-w-0 flex-1 flex-col">
      <span class="truncate text-sm font-medium">{{ item.title }}</span>
      <span class="truncate text-xs text-muted-foreground">{{ look.label }}{{ by }}</span>
    </span>
  </ItemLink>
</template>
