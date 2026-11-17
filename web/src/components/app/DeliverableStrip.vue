<script setup lang="ts">
// "交付物 ⌄" at the top of the panel list, in the style of an artifacts shelf: the project's newest
// documents as paper-stack cards (preview, title, relative time) in a row that scrolls sideways with
// round ‹ › buttons, a "新内容" tag on the ones updated since this viewer last opened them, and
// 查看全部 (opens the library) on the title line. PRs are not shown here; they live on the thread.
import { ChevronDown, ChevronLeft, ChevronRight } from "lucide-vue-next";
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from "vue";
import { useDeliverables } from "@/api/queries";
import { isStripDeliverable, scrollEdges, useSeenDeliverables } from "@/lib/deliverableStrip";
import { shortAgo } from "@/lib/threadList";
import DeliverablePreview from "./DeliverablePreview.vue";
import ItemLink from "./ItemLink.vue";

const props = defineProps<{ pid: string; folded: boolean; limit?: number }>();
const emit = defineEmits<{ fold: [folded: boolean]; all: [] }>();
const list = useDeliverables(() => props.pid, "", "", true);
const items = computed(() => (list.data.value?.items ?? []).filter(isStripDeliverable).slice(0, props.limit ?? 8));
const seen = useSeenDeliverables(() => props.pid);

const scroller = ref<HTMLElement | null>(null);
const edges = ref({ left: false, right: false });
function measure() {
  const el = scroller.value;
  edges.value = el ? scrollEdges(el.scrollLeft, el.clientWidth, el.scrollWidth) : { left: false, right: false };
}
function page(dir: 1 | -1) {
  const el = scroller.value;
  if (el) el.scrollBy({ left: dir * el.clientWidth, behavior: "smooth" });
}

let ro: ResizeObserver | null = null;
function observe() {
  ro?.disconnect();
  if (!scroller.value || typeof ResizeObserver === "undefined") return;
  ro = new ResizeObserver(measure);
  ro.observe(scroller.value);
  if (scroller.value.firstElementChild) ro.observe(scroller.value.firstElementChild);
}
onMounted(observe);
onBeforeUnmount(() => ro?.disconnect());
watch([scroller, () => items.value.length, () => props.folded], () => nextTick(() => (observe(), measure())));
</script>

<template>
  <section v-if="items.length > 0" class="flex flex-col gap-2.5" aria-label="交付物">
    <div class="flex items-center justify-between gap-2">
      <button
        type="button"
        :aria-expanded="!folded"
        class="flex items-center gap-1 rounded-full bg-raised py-[3px] pl-2.5 pr-2 text-[13px] text-muted-foreground hover:bg-accent hover:text-foreground"
        @click="emit('fold', !folded)"
      >
        交付物
        <ChevronDown class="size-3.5 transition-transform" :class="folded && '-rotate-90'" :stroke-width="1.8" />
      </button>
      <button type="button" class="rounded-md px-1.5 py-0.5 text-xs text-muted-foreground hover:bg-raised hover:text-foreground" @click="emit('all')">查看全部</button>
    </div>
    <div v-if="!folded" class="relative -mx-1">
      <div ref="scroller" data-scroller="" class="overflow-x-auto px-1 pb-1 pt-0.5 [scrollbar-width:none] [&::-webkit-scrollbar]:hidden" @scroll.passive="measure">
        <div class="flex w-max gap-3">
          <ItemLink
            v-for="d in items"
            :key="d.id"
            :pid="pid"
            :item="d"
            class="group flex w-[140px] shrink-0 flex-col gap-1.5 outline-none"
            :title="d.title"
            @click="seen.markSeen(d)"
          >
            <span
              class="relative block rounded-xl ring-1 ring-transparent transition-shadow group-hover:ring-input group-focus-visible:ring-2 group-focus-visible:ring-ring"
            >
              <DeliverablePreview :item="d" :fresh="seen.isNew(d)" />
              <span v-if="seen.isNew(d)" class="absolute left-1.5 top-1.5 rounded-full bg-info px-1.5 py-px text-[10px] font-medium leading-4 text-white dark:text-background">新内容</span>
            </span>
            <span class="flex items-start justify-between gap-2 px-0.5 text-[13px] leading-[18px]">
              <span class="line-clamp-2 min-w-0 break-all">{{ d.title }}</span>
              <span class="shrink-0 text-xs text-muted-foreground">{{ shortAgo(d.updated_at) }}</span>
            </span>
          </ItemLink>
        </div>
      </div>
      <button
        v-if="edges.left"
        type="button"
        aria-label="向左滚动"
        class="absolute left-0 top-[33px] flex size-7 items-center justify-center rounded-full border bg-panel text-foreground shadow-[0_1px_4px_rgba(0,0,0,0.12)] hover:bg-raised"
        @click="page(-1)"
      >
        <ChevronLeft class="size-4" :stroke-width="2" />
      </button>
      <button
        v-if="edges.right"
        type="button"
        aria-label="向右滚动"
        class="absolute right-0 top-[33px] flex size-7 items-center justify-center rounded-full border bg-panel text-foreground shadow-[0_1px_4px_rgba(0,0,0,0.12)] hover:bg-raised"
        @click="page(1)"
      >
        <ChevronRight class="size-4" :stroke-width="2" />
      </button>
    </div>
  </section>
</template>
