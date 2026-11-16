<script setup lang="ts">
/** The @ references picked for this message; each one goes with it while its token is in the text. */
import { AtSign, X } from "lucide-vue-next";
import { cn } from "@/lib/utils";
import type { ComposerAssist } from "./useComposerAssist";

defineProps<{ assist: ComposerAssist; class?: string }>();
const KIND: Record<string, string> = { file: "文件", thread: "讨论串", deliverable: "交付物", memory: "记忆" };
</script>

<template>
  <div v-if="assist.picked.value.length" :class="cn('flex flex-wrap gap-1.5', $props.class)" aria-label="引用">
    <span v-for="(r, i) in assist.picked.value" :key="i" class="inline-flex h-7 max-w-[260px] items-center gap-1 rounded-md bg-raised pl-2 pr-1 text-xs" :title="`${KIND[r.kind]}：${r.label}`">
      <AtSign class="size-3 shrink-0 text-muted-foreground" />
      <span class="truncate">{{ r.label }}</span>
      <button type="button" :aria-label="`移除引用 ${r.label}`" class="flex size-5 items-center justify-center rounded text-muted-foreground hover:text-foreground" @click="assist.removeRef(i)">
        <X class="size-3" />
      </button>
    </span>
  </div>
</template>
