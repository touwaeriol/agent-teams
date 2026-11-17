<script setup lang="ts">
/** Thumbnails of the attached images (file chips for PDFs and text files), each with a remove button; a click on a thumbnail opens the in-page preview. */
import { computed, ref } from "vue";
import { FileText, X } from "lucide-vue-next";
import Spinner from "@/components/common/Spinner.vue";
import { cn } from "@/lib/utils";
import type { Attachments } from "./attachments";
import ImagePreview from "./ImagePreview.vue";

const props = defineProps<{ a: Attachments; class?: string }>();
/** Every image attachment, uploading ones included (their preview is an object URL). */
const images = computed(() => props.a.items.value.filter((x) => x.kind === "image" && x.preview));
const urls = computed(() => images.value.map((x) => x.preview));
const viewing = ref<number | null>(null);
const open = (key: string) => {
  const i = images.value.findIndex((x) => x.key === key);
  if (i >= 0) viewing.value = i;
};
</script>

<template>
  <div v-if="a.items.value.length > 0" :class="cn('flex flex-wrap gap-2', $props.class)" aria-label="附件">
    <span
      v-for="x in a.items.value"
      :key="x.key"
      :class="cn('group relative h-14 shrink-0 overflow-hidden rounded-lg border bg-raised', x.kind === 'image' ? 'w-14' : 'w-40', x.error && 'border-destructive')"
      :title="x.error ?? x.name"
    >
      <button v-if="x.kind === 'image'" type="button" :aria-label="`查看 ${x.name}`" class="block size-full cursor-zoom-in" @click="open(x.key)">
        <img :src="x.preview" :alt="x.name" :class="cn('size-full object-cover', !x.id && 'opacity-60')" />
      </button>
      <span v-else :class="cn('flex size-full items-center gap-2 py-2 pl-2.5 pr-6 text-xs', !x.id && 'opacity-60')">
        <FileText class="size-5 shrink-0 text-muted-foreground" :stroke-width="1.6" />
        <span class="flex min-w-0 flex-col">
          <span class="truncate font-medium">{{ x.name }}</span>
          <span class="text-[11px] text-muted-foreground">{{ x.kind === "pdf" ? "PDF" : "文本" }}</span>
        </span>
      </span>
      <span v-if="!x.id && !x.error" class="pointer-events-none absolute inset-0 flex items-center justify-center"><Spinner class="size-4" /></span>
      <span v-if="x.error" class="pointer-events-none absolute inset-x-0 bottom-0 bg-destructive/90 px-1 text-center text-[10px] text-white">失败</span>
      <button
        type="button"
        :aria-label="`移除 ${x.name}`"
        class="absolute right-0.5 top-0.5 flex size-5 items-center justify-center rounded-full bg-black/60 text-white opacity-90 hover:opacity-100"
        @click.stop="a.remove(x.key)"
      >
        <X class="size-3" :stroke-width="2.5" />
      </button>
    </span>
    <ImagePreview :urls="urls" :index="viewing" @close="viewing = null" />
  </div>
</template>
