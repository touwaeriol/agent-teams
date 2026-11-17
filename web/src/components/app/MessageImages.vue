<script setup lang="ts">
/** Attachments of a sent message: images as thumbnails that open the in-page preview, PDFs and text files as chips. */
import { computed, ref } from "vue";
import { FileText } from "lucide-vue-next";
import type { AttachmentRef } from "@/api/views";
import { attachmentUrl, isImageType } from "@/lib/images";
import { cn } from "@/lib/utils";
import ImagePreview from "./ImagePreview.vue";

const props = withDefaults(defineProps<{ items: AttachmentRef[] | undefined; align?: "start" | "end"; /** smaller thumbnails (quoted messages) */ small?: boolean }>(), { align: "end" });
const images = computed(() => (props.items ?? []).filter((x) => isImageType(x.content_type)));
const files = computed(() => (props.items ?? []).filter((x) => !isImageType(x.content_type)));
const urls = computed(() => images.value.map((x) => attachmentUrl(x.id)));
const viewing = ref<number | null>(null);
</script>

<template>
  <div v-if="items?.length" :class="cn('flex flex-wrap gap-2', align === 'end' ? 'justify-end self-end' : 'self-start')">
    <button
      v-for="(x, i) in images"
      :key="x.id"
      type="button"
      :class="cn('block cursor-zoom-in overflow-hidden border bg-raised', small ? 'size-14 rounded-lg' : 'size-24 rounded-xl')"
      :title="x.name"
      :aria-label="`查看图片 ${x.name}`"
      @click="viewing = i"
    >
      <img :src="urls[i]" :alt="x.name" loading="lazy" class="size-full object-cover" />
    </button>
    <a
      v-for="x in files"
      :key="x.id"
      :href="attachmentUrl(x.id)"
      target="_blank"
      rel="noreferrer"
      class="flex h-12 max-w-[220px] items-center gap-2 rounded-xl border bg-panel px-3 text-xs hover:bg-raised"
      :title="x.name"
    >
      <FileText class="size-4 shrink-0 text-muted-foreground" :stroke-width="1.6" />
      <span class="truncate">{{ x.name }}</span>
      <span class="shrink-0 text-muted-foreground">{{ x.content_type === "application/pdf" ? "PDF" : "文本" }}</span>
    </a>
    <ImagePreview :urls="urls" :index="viewing" @close="viewing = null" />
  </div>
</template>
