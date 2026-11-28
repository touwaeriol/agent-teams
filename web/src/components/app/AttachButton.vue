<script setup lang="ts">
import { $t } from "@/i18n";
/** The file picker button (images, PDFs, text files); `open()` lets the "+" menu reuse its picker. */
import { ImagePlus } from "lucide-vue-next";
import { ref } from "vue";
import { ATTACH_ACCEPT } from "@/lib/images";
import type { Attachments } from "./attachments";

const props = withDefaults(defineProps<{ a: Attachments; size?: number; hideButton?: boolean }>(), { size: 32 });
const input = ref<HTMLInputElement | null>(null);
defineExpose({ open: () => input.value?.click() });
function picked(e: Event) {
  const el = e.target as HTMLInputElement;
  props.a.add(Array.from(el.files ?? []));
  el.value = "";
}
</script>

<template>
  <button
    v-if="!hideButton"
    type="button"
    :aria-label="$t('添加文件或照片')"
    :title="$t('添加文件或照片（也可以粘贴或拖进来）')"
    class="flex shrink-0 items-center justify-center rounded-lg text-muted-foreground hover:bg-raised hover:text-foreground"
    :style="{ width: `${size}px`, height: `${size}px` }"
    @click="input?.click()"
  >
    <ImagePlus class="size-[18px]" :stroke-width="1.8" />
  </button>
  <input ref="input" type="file" :accept="ATTACH_ACCEPT" multiple hidden @change="picked" />
</template>
