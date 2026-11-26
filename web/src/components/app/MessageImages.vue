<script setup lang="ts">
/**
 * Attachments of a sent message: images as thumbnails that open the in-page preview; PDFs and any
 * other file as a card (icon, name, type, size) with download and "存到项目文件". An attachment whose
 * retention ran out (410) shows "已过期" and offers nothing.
 */
import { computed, inject, reactive, ref } from "vue";
import { routeLocationKey } from "vue-router";
import { Download, FolderInput } from "lucide-vue-next";
import type { AttachmentRef } from "@/api/views";
import { attachmentState } from "@/lib/attachmentActions";
import { attachKind, attachmentDownloadUrl, attachmentUrl, isImageType, typeLabel } from "@/lib/images";
import { formatBytes } from "@/lib/bytes";
import { cn } from "@/lib/utils";
import FileIcon from "./FileIcon.vue";
import ImagePreview from "./ImagePreview.vue";
import SaveSharedDialog from "./SaveSharedDialog.vue";

type Item = AttachmentRef & { expired?: boolean };
const props = withDefaults(
  defineProps<{ items: Item[] | undefined; align?: "start" | "end"; /** smaller thumbnails (quoted messages) */ small?: boolean; /** offer "存到项目文件" (default: inside a project page) */ canSave?: boolean }>(),
  { align: "end", canSave: undefined },
);
// Not useRoute(): the component also renders outside a router (tests, previews) without warnings.
const route = inject(routeLocationKey, null);
const saveable = computed(() => props.canSave ?? typeof route?.params?.pid === "string");
/** Ids found expired on the page (the list itself marks the rest): a failed thumbnail or a refused save. */
const expired = reactive(new Set<string>());
const isExpired = (x: Item) => x.expired === true || expired.has(x.id);
const images = computed(() => (props.items ?? []).filter((x) => isImageType(x.content_type) && !isExpired(x)));
const files = computed(() => (props.items ?? []).filter((x) => !isImageType(x.content_type) || isExpired(x)));
const urls = computed(() => images.value.map((x) => attachmentUrl(x.id)));
const viewing = ref<number | null>(null);
const saving = ref<Item | null>(null);
const savingOpen = ref(false);

/** A thumbnail that does not load: ask once whether its attachment expired (it then becomes a card). */
function imageFailed(x: Item) {
  void attachmentState(x.id).then((s) => s === "expired" && expired.add(x.id));
}
function save(x: Item) {
  saving.value = x;
  savingOpen.value = true;
}
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
      <img :src="urls[i]" :alt="x.name" loading="lazy" class="size-full object-cover" @error="imageFailed(x)" />
    </button>
    <div
      v-for="x in files"
      :key="x.id"
      :class="cn('flex h-14 w-[240px] max-w-full items-center gap-2.5 rounded-xl border bg-panel pl-3 pr-1.5 text-xs', isExpired(x) && 'opacity-70')"
      :title="x.name"
      data-attachment-card
      :data-expired="isExpired(x) ? '' : undefined"
    >
      <FileIcon :kind="attachKind({ type: x.content_type, name: x.name })" :name="x.name" class="size-6 shrink-0" />
      <span class="flex min-w-0 flex-1 flex-col">
        <span class="truncate font-medium">{{ x.name }}</span>
        <span class="truncate text-[11px] text-muted-foreground">
          <template v-if="isExpired(x)"><span class="text-destructive">已过期</span> · 超过保留期已删除</template>
          <template v-else>{{ typeLabel(x.content_type, x.name) }}<template v-if="x.size"> · {{ formatBytes(x.size) }}</template></template>
        </span>
      </span>
      <template v-if="!isExpired(x)">
        <a
          :href="attachmentDownloadUrl(x.id)"
          class="flex size-8 shrink-0 items-center justify-center rounded-lg text-muted-foreground hover:bg-raised hover:text-foreground"
          :aria-label="`下载 ${x.name}`"
          title="下载"
        >
          <Download class="size-4" :stroke-width="1.8" />
        </a>
        <button
          v-if="saveable"
          type="button"
          class="flex size-8 shrink-0 items-center justify-center rounded-lg text-muted-foreground hover:bg-raised hover:text-foreground"
          :aria-label="`把 ${x.name} 存到项目文件`"
          title="存到项目文件"
          @click="save(x)"
        >
          <FolderInput class="size-4" :stroke-width="1.8" />
        </button>
      </template>
    </div>
    <ImagePreview :urls="urls" :index="viewing" @close="viewing = null" />
    <SaveSharedDialog v-if="saveable" v-model:open="savingOpen" :attachment="saving" @expired="(id) => expired.add(id)" />
  </div>
</template>
