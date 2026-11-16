<script setup lang="ts">
import { computed } from "vue";
import type { DeliverableView } from "@/api/views";
import { deliverableKind } from "@/lib/present";
import { timeAgo } from "@/lib/utils";
import DeliverableThumb from "../DeliverableThumb.vue";
import ItemLink from "../ItemLink.vue";

const props = defineProps<{ pid: string; item: DeliverableView }>();
const look = computed(() => deliverableKind(props.item.kind));
</script>

<template>
  <ItemLink :pid="pid" :item="item" class="flex flex-col overflow-hidden rounded-xl border bg-panel text-left hover:border-input hover:shadow-sm">
    <DeliverableThumb :item="item" />
    <span class="flex flex-col gap-0.5 px-2.5 pb-2.5 pt-2">
      <span class="line-clamp-2 text-[13px] font-medium leading-[18px]">{{ item.title }}</span>
      <span class="text-[11px] text-muted-foreground">{{ look.label }} · {{ timeAgo(item.updated_at) }}</span>
    </span>
  </ItemLink>
</template>
