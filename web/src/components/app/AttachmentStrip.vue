<script setup lang="ts">
import { $t } from "@/i18n";
/**
 * Thumbnails of the attached images (file cards for everything else), each with a remove button
 * (which also cancels a running upload) and a progress bar while it uploads; a click on a
 * thumbnail opens the in-page preview.
 */
import { computed, ref } from "vue";
import { X } from "lucide-vue-next";
import { typeLabel } from "@/lib/images";
import { formatBytes } from "@/lib/bytes";
import FileIcon from "./FileIcon.vue";
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
  <div v-if="a.items.value.length > 0" :class="cn('flex flex-wrap gap-2', $props.class)" :aria-label="$t('附件')">
    <span
      v-for="x in a.items.value"
      :key="x.key"
      :class="cn('group relative h-14 shrink-0 overflow-hidden rounded-lg border bg-raised', x.kind === 'image' ? 'w-14' : 'w-52', x.error && 'border-destructive')"
      :title="x.error ?? x.name"
    >
      <button v-if="x.kind === 'image'" type="button" :aria-label="$t('查看 {name}', { name: x.name })" class="block size-full cursor-zoom-in" @click="open(x.key)">
        <img :src="x.preview" :alt="x.name" :class="cn('size-full object-cover', !x.id && 'opacity-60')" />
      </button>
      <span v-else :class="cn('flex size-full items-center gap-2 py-2 pl-2.5 pr-6 text-xs', !x.id && 'opacity-70')">
        <FileIcon :kind="x.kind" :name="x.name" class="size-5 shrink-0" />
        <span class="flex min-w-0 flex-col">
          <span class="truncate font-medium">{{ x.name }}</span>
          <span class="truncate text-[11px] text-muted-foreground">
            {{ x.kind === "pdf" ? "PDF" : x.kind === "text" ? $t("文本") : typeLabel("", x.name) }}<template v-if="x.size !== undefined"> · {{ formatBytes(x.size) }}</template>
            <template v-if="!x.id && !x.error"> · {{ (x.progress ?? 0) >= 1 ? $t("正在完成…") : `${Math.min(99, Math.round((x.progress ?? 0) * 100))}%` }}</template>
          </span>
        </span>
      </span>
      <span v-if="!x.id && !x.error && x.kind === 'image'" class="pointer-events-none absolute inset-0 flex items-center justify-center"><Spinner class="size-4" /></span>
      <span
        v-if="!x.id && !x.error"
        class="pointer-events-none absolute inset-x-0 bottom-0 h-1 bg-black/10"
        role="progressbar"
        :aria-label="$t('{name} 上传进度', { name: x.name })"
        :aria-valuenow="Math.round((x.progress ?? 0) * 100)"
        aria-valuemin="0"
        aria-valuemax="100"
      >
        <span class="block h-full bg-brand transition-[width] duration-200" :style="{ width: `${Math.round((x.progress ?? 0) * 100)}%` }" />
      </span>
      <span v-if="x.error" class="pointer-events-none absolute inset-x-0 bottom-0 bg-destructive/90 px-1 text-center text-[10px] text-white">{{ $t("失败") }}</span>
      <button
        type="button"
        :aria-label="!x.id && !x.error ? $t('取消上传 {name}', { name: x.name }) : $t('移除 {name}', { name: x.name })"
        :title="!x.id && !x.error ? $t('取消上传') : $t('移除')"
        class="absolute right-0.5 top-0.5 flex size-5 items-center justify-center rounded-full bg-black/60 text-white opacity-90 hover:opacity-100"
        @click.stop="a.remove(x.key)"
      >
        <X class="size-3" :stroke-width="2.5" />
      </button>
    </span>
    <ImagePreview :urls="urls" :index="viewing" @close="viewing = null" />
  </div>
</template>
