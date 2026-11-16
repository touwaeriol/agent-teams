<script setup lang="ts">
// A small picture of a deliverable for list cards: a hosted HTML page scaled down in a sandboxed,
// inert iframe, a hosted image itself, anything else (docs, PRs, links) its type icon on the kind's tint.
import { computed } from "vue";
import { useDeliverableFiles } from "@/api/queries";
import type { DeliverableView } from "@/api/views";
import { frameSandbox, hasPicture } from "@/lib/artifacts";
import { fileIcon, typeLabel } from "@/lib/library";
import { deliverableKind } from "@/lib/present";
import { FILE_ICONS } from "./fileIcons";

const props = defineProps<{ item: DeliverableView }>();
const live = computed(() => hasPicture(props.item));
const files = useDeliverableFiles(() => props.item.id, 0, live);
const look = computed(() => deliverableKind(props.item.kind));
const icon = computed(() => FILE_ICONS[fileIcon(props.item)]);
</script>

<template>
  <span class="relative block aspect-[16/10] w-full overflow-hidden bg-[#F4F3F0]">
    <template v-if="live && files.data.value?.entry_url">
      <img v-if="files.data.value.render === 'image'" :src="files.data.value.entry_url" alt="" loading="lazy" referrerpolicy="no-referrer" class="size-full object-cover object-top" />
      <iframe
        v-else
        :src="files.data.value.entry_url"
        :title="item.title"
        :sandbox="frameSandbox(files.data.value.isolated)"
        referrerpolicy="no-referrer"
        loading="lazy"
        tabindex="-1"
        aria-hidden="true"
        class="pointer-events-none absolute left-0 top-0 h-[400%] w-[400%] origin-top-left scale-[0.25] border-0 bg-white"
      />
    </template>
    <span v-else class="flex size-full flex-col items-center justify-center gap-1" :style="{ background: look.block }">
      <span class="flex size-8 items-center justify-center rounded-lg" :style="{ background: look.bg, color: look.fg }">
        <component :is="icon" aria-hidden="true" class="size-4" :stroke-width="1.8" />
      </span>
      <span class="text-[10px] text-muted-foreground">{{ typeLabel(item) }}</span>
    </span>
  </span>
</template>
