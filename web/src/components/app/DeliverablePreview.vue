<script setup lang="ts">
// The preview of a deliverable card in the 交付物 strip: a stack of paper on a soft grey ground.
// The top sheet shows the document itself: a published Markdown file its first lines, a page
// scaled down in a sandboxed inert iframe, an image cropped to fit; anything else a blank sheet.
// fresh (updated since last opened) tints the ground light blue.
import { computed, h } from "vue";
import { useDeliverableFiles } from "@/api/queries";
import type { DeliverableView } from "@/api/views";
import { frameSandbox, isHosted } from "@/lib/artifacts";
import MarkdownPeek from "./MarkdownPeek.vue";

const props = defineProps<{ item: DeliverableView; fresh?: boolean }>();
const render = computed(() => (isHosted(props.item) ? (props.item.detail.render ?? "") : ""));
const live = computed(() => render.value === "html" || render.value === "image" || render.value === "markdown");
const files = useDeliverableFiles(() => props.item.id, 0, live);
/** Grey placeholder lines on the top sheet (also while a Markdown text loads or when it failed). */
const PaperLines = () =>
  h("span", { "data-blank": "", class: "flex flex-col gap-[5px] px-2.5 pt-3", "aria-hidden": "true" }, [
    h("span", { class: "h-[5px] w-3/5 rounded-full bg-foreground/15" }),
    ...["w-full", "w-11/12", "w-4/5", "w-full"].map((w) => h("span", { class: `h-[3px] ${w} rounded-full bg-foreground/[0.08]` })),
  ]);
const entry = computed(() => (live.value ? files.data.value?.entry_url : undefined));
</script>

<template>
  <span class="relative block h-[90px] w-full overflow-hidden rounded-xl" :class="fresh ? 'bg-[#EAF1FD] dark:bg-info/15' : 'bg-muted dark:bg-well'">
    <!-- the sheets underneath, slightly askew -->
    <span class="absolute inset-x-[24px] top-[7px] h-full -rotate-[6deg] rounded-[5px] border border-black/[0.08] bg-[#FBFAF8] dark:border-white/[0.07] dark:bg-[hsl(228_22%_14%)]" />
    <span class="absolute inset-x-[22px] top-[10px] h-full rotate-[5deg] rounded-[5px] border border-black/[0.08] bg-[#FDFDFC] dark:border-white/[0.07] dark:bg-[hsl(228_22%_15.5%)]" />
    <span
      data-paper=""
      class="absolute inset-x-[18px] top-[14px] h-full overflow-hidden rounded-[5px] border border-black/[0.07] bg-white shadow-[0_1px_3px_rgba(0,0,0,0.08)] dark:border-white/[0.08] dark:bg-raised"
    >
      <img v-if="entry && files.data.value?.render === 'image'" :src="entry" alt="" loading="lazy" referrerpolicy="no-referrer" class="size-full object-cover object-top" />
      <iframe
        v-else-if="entry && files.data.value?.render === 'html'"
        :src="entry"
        :title="item.title"
        :sandbox="frameSandbox(files.data.value.isolated)"
        referrerpolicy="no-referrer"
        loading="lazy"
        tabindex="-1"
        aria-hidden="true"
        class="pointer-events-none absolute left-0 top-0 h-[500%] w-[500%] origin-top-left scale-[0.2] border-0 bg-white"
      />
      <span v-else-if="entry && files.data.value?.render === 'markdown'" class="block px-2 pt-2">
        <MarkdownPeek :url="entry" :base-url="files.data.value.base_url"><PaperLines class="-mx-2 -mt-2" /></MarkdownPeek>
      </span>
      <!-- PDF, .txt, design links, or no signed URL yet: a blank sheet with grey lines. PDFs are not
           rendered here on purpose: that needs the browser's PDF viewer (heavy, not inert) or a PDF library. -->
      <PaperLines v-else />
    </span>
  </span>
</template>
