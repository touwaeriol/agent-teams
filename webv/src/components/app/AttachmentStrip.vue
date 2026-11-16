<script setup lang="ts">
/** Thumbnails of the attached images, each with a remove button. */
import { X } from "lucide-vue-next";
import Spinner from "@/components/common/Spinner.vue";
import { cn } from "@/lib/utils";
import type { Attachments } from "./attachments";

defineProps<{ a: Attachments; class?: string }>();
</script>

<template>
  <div v-if="a.items.value.length > 0" :class="cn('flex flex-wrap gap-2', $props.class)" aria-label="附带的图片">
    <span
      v-for="x in a.items.value"
      :key="x.key"
      :class="cn('group relative size-14 shrink-0 overflow-hidden rounded-lg border bg-raised', x.error && 'border-destructive')"
      :title="x.error ?? x.name"
    >
      <img :src="x.preview" :alt="x.name" :class="cn('size-full object-cover', !x.id && 'opacity-60')" />
      <span v-if="!x.id && !x.error" class="absolute inset-0 flex items-center justify-center"><Spinner class="size-4" /></span>
      <span v-if="x.error" class="absolute inset-x-0 bottom-0 bg-destructive/90 px-1 text-center text-[10px] text-white">失败</span>
      <button
        type="button"
        :aria-label="`移除 ${x.name}`"
        class="absolute right-0.5 top-0.5 flex size-5 items-center justify-center rounded-full bg-black/60 text-white opacity-90 hover:opacity-100"
        @click="a.remove(x.key)"
      >
        <X class="size-3" :stroke-width="2.5" />
      </button>
    </span>
  </div>
</template>
