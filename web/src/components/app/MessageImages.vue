<script setup lang="ts">
/** Attachments of a sent message: images as thumbnails that open the full image, PDFs and text files as chips. */
import { computed } from "vue";
import { FileText } from "lucide-vue-next";
import type { AttachmentRef } from "@/api/views";
import { attachmentUrl, isImageType } from "@/lib/images";
import { cn } from "@/lib/utils";

const props = withDefaults(defineProps<{ items: AttachmentRef[] | undefined; align?: "start" | "end"; /** smaller thumbnails (quoted messages) */ small?: boolean }>(), { align: "end" });
const images = computed(() => (props.items ?? []).filter((x) => isImageType(x.content_type)));
const files = computed(() => (props.items ?? []).filter((x) => !isImageType(x.content_type)));
</script>

<template>
  <div v-if="items?.length" :class="cn('flex flex-wrap gap-2', align === 'end' ? 'justify-end self-end' : 'self-start')">
    <a v-for="x in images" :key="x.id" :href="attachmentUrl(x.id)" target="_blank" rel="noreferrer" :class="cn('block overflow-hidden border bg-raised', small ? 'size-14 rounded-lg' : 'size-24 rounded-xl')" :title="x.name">
      <img :src="attachmentUrl(x.id)" :alt="x.name" loading="lazy" class="size-full object-cover" />
    </a>
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
  </div>
</template>
