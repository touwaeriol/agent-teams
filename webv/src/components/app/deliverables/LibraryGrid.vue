<script setup lang="ts">
import type { DeliverableView } from "@/api/views";
import { fileIcon, formatBytes, ownerLabel, sizeOf, typeLabel } from "@/lib/library";
import { FILE_ICONS } from "../fileIcons";
import ItemLink from "../ItemLink.vue";
import ItemMenu from "./ItemMenu.vue";

defineProps<{ pid: string; items: DeliverableView[] }>();
</script>

<template>
  <div class="grid grid-cols-[repeat(auto-fill,minmax(150px,1fr))] gap-3">
    <div v-for="d in items" :key="d.id" class="group relative">
      <ItemLink :pid="pid" :item="d" class="flex flex-col overflow-hidden rounded-xl border bg-panel hover:border-input">
        <span class="flex h-24 flex-col items-center justify-center gap-1.5 bg-raised text-muted-foreground">
          <component :is="FILE_ICONS[fileIcon(d)]" class="size-7" :stroke-width="1.4" />
          <span class="text-[11px] font-medium tracking-wide">{{ typeLabel(d) }}</span>
        </span>
        <span class="flex flex-col gap-0.5 px-3 py-2">
          <span class="truncate pr-5 text-[13px] font-medium">{{ d.title }}</span>
          <span class="truncate text-xs text-muted-foreground">{{ ownerLabel(d.published_by) }} · {{ formatBytes(sizeOf(d)) }}</span>
        </span>
      </ItemLink>
      <span class="absolute bottom-2 right-1"><ItemMenu :item="d" :pid="pid" /></span>
    </div>
  </div>
</template>
