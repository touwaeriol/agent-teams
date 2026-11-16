<script setup lang="ts">
/** The image picker button. */
import { ImagePlus } from "lucide-vue-next";
import { ref } from "vue";
import { IMAGE_ACCEPT } from "@/lib/images";
import type { Attachments } from "./attachments";

const props = withDefaults(defineProps<{ a: Attachments; size?: number }>(), { size: 32 });
const input = ref<HTMLInputElement | null>(null);
function picked(e: Event) {
  const el = e.target as HTMLInputElement;
  props.a.add(Array.from(el.files ?? []));
  el.value = "";
}
</script>

<template>
  <button
    type="button"
    aria-label="添加图片"
    title="添加图片（也可以粘贴或拖进来）"
    class="flex shrink-0 items-center justify-center rounded-lg text-muted-foreground hover:bg-raised hover:text-foreground"
    :style="{ width: `${size}px`, height: `${size}px` }"
    @click="input?.click()"
  >
    <ImagePlus class="size-[18px]" :stroke-width="1.8" />
  </button>
  <input ref="input" type="file" :accept="IMAGE_ACCEPT" multiple hidden @change="picked" />
</template>
