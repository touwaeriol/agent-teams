<script setup lang="ts">
// "交付物 ▾" at the top of the panel list: the project's newest deliverables as small cards
// (preview, title, relative time) and a 查看全部 card that opens the library.
import { ChevronDown } from "lucide-vue-next";
import { computed } from "vue";
import { useDeliverables } from "@/api/queries";
import { shortAgo } from "@/lib/threadList";
import DeliverablePreview from "./DeliverablePreview.vue";
import ItemLink from "./ItemLink.vue";

const props = defineProps<{ pid: string; folded: boolean; limit?: number }>();
const emit = defineEmits<{ fold: [folded: boolean]; all: [] }>();
const list = useDeliverables(() => props.pid, "", "");
const items = computed(() => (list.data.value?.items ?? []).slice(0, props.limit ?? 6));
</script>

<template>
  <section v-if="items.length > 0" class="flex flex-col gap-2" aria-label="交付物">
    <button
      type="button"
      :aria-expanded="!folded"
      class="-ml-1.5 flex items-center gap-1 self-start rounded-md px-1.5 py-[3px] text-[13px] text-muted-foreground hover:bg-raised hover:text-foreground"
      @click="emit('fold', !folded)"
    >
      交付物
      <ChevronDown class="size-3.5 transition-transform" :class="folded && '-rotate-90'" :stroke-width="1.8" />
    </button>
    <div v-if="!folded" class="scroll-thin -mx-1 flex gap-3 overflow-x-auto px-1 pb-1">
      <ItemLink v-for="d in items" :key="d.id" :pid="pid" :item="d" class="group flex w-[136px] shrink-0 flex-col gap-1.5" :title="d.title">
        <span class="block overflow-hidden rounded-xl border bg-panel shadow-[0_1px_2px_rgba(0,0,0,0.04)] group-hover:border-input">
          <DeliverablePreview :item="d" />
        </span>
        <span class="flex items-start justify-between gap-2 text-[13px] leading-[18px]">
          <span class="line-clamp-2 min-w-0 break-all">{{ d.title }}</span>
          <span class="shrink-0 text-xs text-muted-foreground">{{ shortAgo(d.updated_at) }}</span>
        </span>
      </ItemLink>
      <button
        type="button"
        class="flex aspect-[16/10] w-[136px] shrink-0 items-center justify-center rounded-xl border bg-panel text-[13px] text-foreground/80 shadow-[0_1px_2px_rgba(0,0,0,0.04)] hover:border-input hover:bg-raised"
        @click="emit('all')"
      >
        查看全部
      </button>
    </div>
  </section>
</template>
