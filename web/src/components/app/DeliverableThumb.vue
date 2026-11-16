<script setup lang="ts">
// Card thumbnail of a deliverable: hosted images show themselves (short-lived signed URL),
// everything else a tile in the kind's colours with its file icon and type.
import { computed } from "vue";
import { useDeliverableFiles } from "@/api/queries";
import type { DeliverableView } from "@/api/views";
import { extensionOf, fileIcon } from "@/lib/library";
import { deliverableKind } from "@/lib/present";
import { FILE_ICONS } from "./fileIcons";

const props = defineProps<{ item: DeliverableView }>();
const image = computed(() => props.item.detail.hosted === true && fileIcon(props.item) === "image");
const files = useDeliverableFiles(() => props.item.id, 0, image);
const look = computed(() => deliverableKind(props.item.kind));
const icon = computed(() => FILE_ICONS[fileIcon(props.item)]);
const ext = computed(() => (props.item.detail.hosted && props.item.detail.entry ? extensionOf(String(props.item.detail.entry)).toUpperCase() : ""));
</script>

<template>
  <span v-if="image && files.data.value?.entry_url" class="flex aspect-[4/3] items-center justify-center overflow-hidden bg-[#F4F3F0]">
    <img :src="files.data.value.entry_url" alt="" loading="lazy" class="size-full object-cover" />
  </span>
  <span v-else class="flex aspect-[4/3] flex-col items-center justify-center gap-1.5" :style="{ background: look.block }">
    <span class="flex size-10 items-center justify-center rounded-xl" :style="{ background: look.bg, color: look.fg }">
      <component :is="icon" aria-hidden="true" class="size-5" :stroke-width="1.7" />
    </span>
    <span v-if="ext" class="font-mono text-[10px] text-muted-foreground">{{ ext }}</span>
  </span>
</template>
